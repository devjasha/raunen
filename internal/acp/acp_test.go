package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"raunen/internal/agent"
	"raunen/internal/provider"
	"raunen/internal/session"
	"raunen/internal/tools"
)

// endpoint stands in for a model, replying with scripted frames so a turn can
// be driven down a chosen path without a real provider.
type endpoint struct {
	*httptest.Server
	frames []string
	calls  int
	status int
	mu     sync.Mutex
}

func newEndpoint(t *testing.T, frames ...string) *endpoint {
	t.Helper()
	e := &endpoint{frames: frames}
	e.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		status := e.status
		i := min(e.calls, len(e.frames)-1)
		e.calls++
		e.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"refused"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, e.frames[i])
	}))
	t.Cleanup(e.Close)
	return e
}

func textFrame(reply string) string {
	f, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"delta":         map[string]any{"content": reply},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
	})
	return fmt.Sprintf("data: %s\n\ndata: [DONE]\n\n", f)
}

func toolFrame(name, args string) string {
	f, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"delta": map[string]any{"tool_calls": []map[string]any{{
				"index": 0, "id": "c1", "type": "function",
				"function": map[string]any{"name": name, "arguments": args},
			}}},
			"finish_reason": "tool_calls",
		}},
	})
	return fmt.Sprintf("data: %s\n\ndata: [DONE]\n\n", f)
}

// client drives a server over an in-memory pipe, which is what the editor would
// be on the other end of stdio.
type client struct {
	t       *testing.T
	toAgent io.WriteCloser
	fromAg  *json.Decoder

	mu      sync.Mutex
	updates []map[string]any
	waiting chan message
	// permit answers a permission request with this option id. Empty rejects.
	permit string
	seq    int
	root   string
}

// newClient starts a server against a temporary workspace and returns a client
// wired to it.
func newClient(t *testing.T, e *endpoint, mode agent.Mode) *client {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	clientR, agentW := io.Pipe()
	agentR, clientW := io.Pipe()

	build := func(cwd string) (*agent.Agent, *session.Session, Expander, error) {
		ag := agent.New(provider.New(e.URL+"/v1", "", "fake"),
			tools.Default(cwd, tools.OutputBudget(8192)), "")
		ag.SetRef("fake/fake")
		ag.SetMode(mode)
		return ag, session.New(cwd, "fake/fake"), nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = Serve(ctx, agentR, agentW, "test", build)
		agentW.Close()
	}()

	c := &client{t: t, toAgent: clientW, fromAg: json.NewDecoder(clientR), permit: "allow"}
	c.root = root
	go c.read()
	return c
}

// read pumps frames from the agent, answering permission requests and
// collecting updates. Responses go to whoever is waiting.
func (c *client) read() {
	for {
		var m message
		if err := c.fromAg.Decode(&m); err != nil {
			return
		}
		switch {
		case m.Method == "session/update":
			var n struct {
				Update map[string]any `json:"update"`
			}
			_ = json.Unmarshal(m.Params, &n)
			c.mu.Lock()
			c.updates = append(c.updates, n.Update)
			c.mu.Unlock()
		case m.Method == "session/request_permission":
			out := PermissionOutcome{Outcome: "selected", OptionID: c.permit}
			if c.permit == "" {
				out = PermissionOutcome{Outcome: "cancelled"}
			}
			b, _ := json.Marshal(RequestPermissionResponse{Outcome: out})
			_ = c.write(message{ID: m.ID, Result: b})
		default:
			c.mu.Lock()
			if c.waiting != nil {
				c.waiting <- m
			}
			c.mu.Unlock()
		}
	}
}

func (c *client) write(m message) error {
	m.JSONRPC = "2.0"
	b, _ := json.Marshal(m)
	_, err := c.toAgent.Write(append(b, '\n'))
	return err
}

// call sends a request and waits for its response.
func (c *client) call(method string, params any) message {
	c.t.Helper()
	reply := make(chan message, 1)
	c.mu.Lock()
	c.waiting = reply
	c.seq++
	id, _ := json.Marshal(c.seq)
	c.mu.Unlock()

	b, _ := json.Marshal(params)
	if err := c.write(message{ID: id, Method: method, Params: b}); err != nil {
		c.t.Fatalf("%s: %v", method, err)
	}
	select {
	case m := <-reply:
		return m
	case <-time.After(20 * time.Second):
		c.t.Fatalf("%s: timed out", method)
		return message{}
	}
}

// notify sends a notification, which expects no reply.
func (c *client) notify(method string, params any) {
	b, _ := json.Marshal(params)
	_ = c.write(message{Method: method, Params: b})
}

// seen returns the updates of one kind.
func (c *client) seen(kind string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, u := range c.updates {
		if u["sessionUpdate"] == kind {
			out = append(out, u)
		}
	}
	return out
}

// text joins every streamed message chunk, which is the reply as the editor
// would assemble it.
func (c *client) text() string {
	var b strings.Builder
	for _, u := range c.seen(updateAgentMessage) {
		if content, ok := u["content"].(map[string]any); ok {
			if s, ok := content["text"].(string); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

// openSession runs the handshake and returns the session id.
func (c *client) openSession() string {
	c.t.Helper()
	c.call("initialize", InitializeRequest{ProtocolVersion: Version})
	var res NewSessionResponse
	m := c.call("session/new", NewSessionRequest{Cwd: c.root})
	if m.Error != nil {
		c.t.Fatalf("session/new: %v", m.Error)
	}
	if err := json.Unmarshal(m.Result, &res); err != nil {
		c.t.Fatal(err)
	}
	return res.SessionID
}

// TestInitializeNegotiates is the handshake every connection begins with.
func TestInitializeNegotiates(t *testing.T) {
	c := newClient(t, newEndpoint(t, textFrame("hi")), agent.ModeAuto)

	m := c.call("initialize", InitializeRequest{ProtocolVersion: Version})
	if m.Error != nil {
		t.Fatalf("initialize failed: %v", m.Error)
	}
	var res InitializeResponse
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.ProtocolVersion != Version {
		t.Errorf("protocolVersion = %d, want %d", res.ProtocolVersion, Version)
	}
	if !res.AgentCapabilities.LoadSession {
		t.Error("loadSession should be advertised: saved sessions already exist")
	}
	// Image blocks are forwarded to the model as attachments, so the capability
	// is advertised. Audio is not: nothing downstream could act on it, and an
	// editor should grey the button out rather than send into a void.
	if !res.AgentCapabilities.PromptCapabilities.Image {
		t.Error("image support should be advertised")
	}
	if res.AgentCapabilities.PromptCapabilities.Audio {
		t.Error("audio support is advertised but not implemented")
	}
	if res.AgentInfo == nil || res.AgentInfo.Name != "raunen" {
		t.Errorf("agentInfo = %+v", res.AgentInfo)
	}
}

// TestPromptStreamsAndAnswers is the core exchange: a prompt, streamed chunks,
// and a stop reason with the tokens it cost.
func TestPromptStreamsAndAnswers(t *testing.T) {
	c := newClient(t, newEndpoint(t, textFrame("Hello.")), agent.ModeAuto)
	sid := c.openSession()

	m := c.call("session/prompt", PromptRequest{
		SessionID: sid, Prompt: []ContentBlock{TextBlock("hi")},
	})
	if m.Error != nil {
		t.Fatalf("session/prompt: %v", m.Error)
	}
	var res PromptResponse
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopEndTurn {
		t.Errorf("stopReason = %q, want %q", res.StopReason, StopEndTurn)
	}
	if got := c.text(); got != "Hello." {
		t.Errorf("streamed text = %q, want %q", got, "Hello.")
	}
	if res.Usage == nil || res.Usage.TotalTokens != 18 {
		t.Errorf("usage = %+v, want 18 total", res.Usage)
	}
}

// TestToolCallsArePairedByID is what lets an editor match a completion to the
// call it announced. raunen's own events carry no id, so one is minted here.
func TestToolCallsArePairedByID(t *testing.T) {
	e := newEndpoint(t, toolFrame("list", `{"path":"."}`), textFrame("Done."))
	c := newClient(t, e, agent.ModeAuto)
	sid := c.openSession()
	c.call("session/prompt", PromptRequest{SessionID: sid, Prompt: []ContentBlock{TextBlock("list")}})

	starts, ends := c.seen(updateToolCall), c.seen(updateToolCallEnd)
	if len(starts) != 1 || len(ends) != 1 {
		t.Fatalf("got %d starts and %d ends, want one of each", len(starts), len(ends))
	}
	if starts[0]["toolCallId"] != ends[0]["toolCallId"] {
		t.Errorf("ids do not match: %v vs %v", starts[0]["toolCallId"], ends[0]["toolCallId"])
	}
	if starts[0]["kind"] != "read" {
		t.Errorf("kind = %v, want read", starts[0]["kind"])
	}
	if ends[0]["status"] != statusCompleted {
		t.Errorf("status = %v, want completed", ends[0]["status"])
	}
}

// TestPermissionApproval covers the call that travels agent-to-client during a
// turn: the editor is asked, says yes, and the tool runs.
func TestPermissionApproval(t *testing.T) {
	e := newEndpoint(t, toolFrame("write", `{"path":"out.txt","content":"x"}`), textFrame("Done."))
	c := newClient(t, e, agent.ModeAccept)
	c.permit = "allow"
	sid := c.openSession()
	c.call("session/prompt", PromptRequest{SessionID: sid, Prompt: []ContentBlock{TextBlock("write")}})

	ends := c.seen(updateToolCallEnd)
	if len(ends) != 1 || ends[0]["status"] != statusCompleted {
		t.Fatalf("approved call did not complete: %+v", ends)
	}
}

// TestPermissionRejection is the other half, and the one that matters: a client
// that says no must stop the tool.
func TestPermissionRejection(t *testing.T) {
	e := newEndpoint(t, toolFrame("write", `{"path":"out.txt","content":"x"}`), textFrame("Done."))
	c := newClient(t, e, agent.ModeAccept)
	c.permit = "" // cancelled
	sid := c.openSession()
	c.call("session/prompt", PromptRequest{SessionID: sid, Prompt: []ContentBlock{TextBlock("write")}})

	ends := c.seen(updateToolCallEnd)
	if len(ends) != 1 {
		t.Fatalf("no completion reported: %+v", ends)
	}
	if ends[0]["status"] != statusFailed {
		t.Errorf("status = %v, want failed when the client refuses", ends[0]["status"])
	}
}

// TestSetModeIsAcknowledged and reported back, so an editor showing the mode in
// two places keeps both in step.
func TestSetModeIsAcknowledged(t *testing.T) {
	c := newClient(t, newEndpoint(t, textFrame("hi")), agent.ModeAuto)
	sid := c.openSession()

	if m := c.call("session/set_mode", SetModeRequest{SessionID: sid, ModeID: modePlan}); m.Error != nil {
		t.Fatalf("set_mode: %v", m.Error)
	}
	ups := c.seen(updateCurrentMode)
	if len(ups) != 1 || ups[0]["currentModeId"] != modePlan {
		t.Errorf("mode update = %+v, want plan", ups)
	}
}

// TestUnknownModeIsRejected rather than silently ignored, which would leave the
// editor showing a mode the agent is not in.
func TestUnknownModeIsRejected(t *testing.T) {
	c := newClient(t, newEndpoint(t, textFrame("hi")), agent.ModeAuto)
	sid := c.openSession()

	if m := c.call("session/set_mode", SetModeRequest{SessionID: sid, ModeID: "turbo"}); m.Error == nil {
		t.Error("an unknown mode was accepted")
	}
}

// TestUnknownSessionIsRejected with a message naming the id, since a client
// holding several sessions needs to know which one went missing.
func TestUnknownSessionIsRejected(t *testing.T) {
	c := newClient(t, newEndpoint(t, textFrame("hi")), agent.ModeAuto)
	c.call("initialize", InitializeRequest{ProtocolVersion: Version})

	m := c.call("session/prompt", PromptRequest{
		SessionID: "nope", Prompt: []ContentBlock{TextBlock("hi")},
	})
	if m.Error == nil {
		t.Fatal("a prompt against an unknown session was accepted")
	}
	if !strings.Contains(m.Error.Message, "nope") {
		t.Errorf("error does not name the session: %q", m.Error.Message)
	}
}

// TestEmptyPromptIsRejected: an empty turn would cost a request and produce
// nothing, and it is far more likely to be a client bug than an intention.
func TestEmptyPromptIsRejected(t *testing.T) {
	c := newClient(t, newEndpoint(t, textFrame("hi")), agent.ModeAuto)
	sid := c.openSession()

	if m := c.call("session/prompt", PromptRequest{SessionID: sid}); m.Error == nil {
		t.Error("an empty prompt was accepted")
	}
}

// TestUnknownMethodIsAnError rather than silence, which reads as a hung agent.
func TestUnknownMethodIsAnError(t *testing.T) {
	c := newClient(t, newEndpoint(t, textFrame("hi")), agent.ModeAuto)

	m := c.call("session/teleport", struct{}{})
	if m.Error == nil {
		t.Fatal("an unknown method was accepted")
	}
	if m.Error.Code != codeMethodNotFound {
		t.Errorf("code = %d, want %d", m.Error.Code, codeMethodNotFound)
	}
}

// TestFailedTurnIsRefusalNotTransportError. The connection is fine; the client
// still wants the usage and the output already streamed.
func TestFailedTurnIsRefusal(t *testing.T) {
	// A 400 rather than a dropped connection: a connection failure is retried
	// with backoff, which would spend five seconds proving something about
	// which stop reason is reported.
	e := newEndpoint(t, textFrame("unused"))
	e.status = http.StatusBadRequest
	c := newClient(t, e, agent.ModeAuto)
	sid := c.openSession()

	m := c.call("session/prompt", PromptRequest{
		SessionID: sid, Prompt: []ContentBlock{TextBlock("hi")},
	})
	if m.Error != nil {
		t.Fatalf("a failed turn should answer, not error: %v", m.Error)
	}
	var res PromptResponse
	_ = json.Unmarshal(m.Result, &res)
	if res.StopReason != StopRefusal {
		t.Errorf("stopReason = %q, want %q", res.StopReason, StopRefusal)
	}
}

// TestPromptTextFlattening covers what a prompt is turned into.
func TestPromptTextFlattening(t *testing.T) {
	res, _ := json.Marshal(map[string]any{"uri": "file:///p/a.go", "text": "package a"})

	got := promptText([]ContentBlock{
		TextBlock("look at"),
		{Type: "resource_link", URI: "file:///p/main.go"},
		{Type: "resource", Resource: res},
	})

	// A link becomes its path: raunen has tools to read what it is pointed at.
	if !strings.Contains(got, "/p/main.go") {
		t.Errorf("resource link was dropped: %q", got)
	}
	// An embedded resource is already inlined by the editor, and dropping it
	// would lose context the user deliberately attached.
	if !strings.Contains(got, "package a") {
		t.Errorf("embedded resource was dropped: %q", got)
	}
}

// TestGrantPatternStaysNarrow: approving one command must never become "bash
// may run anything".
func TestGrantPatternStaysNarrow(t *testing.T) {
	for _, tc := range []struct{ tool, target, want string }{
		{"bash", `git commit -m "x"`, "git commit *"},
		{"bash", "ls -la", "ls *"},
		{"write", "internal/app/main.go", "internal/app/main.go"},
	} {
		if got := grantPattern(tc.tool, tc.target); got != tc.want {
			t.Errorf("grantPattern(%q, %q) = %q, want %q", tc.tool, tc.target, got, tc.want)
		}
	}
	if got := grantPattern("bash", "rm -rf /"); got == "" || got == "*" {
		t.Errorf("granting a command produced %q, which covers everything", got)
	}
}

// TestToolKindAndTitle is what an editor shows and which icon it picks.
func TestToolKindAndTitle(t *testing.T) {
	for _, tc := range []struct{ name, args, kind, title string }{
		{"read", `{"path":"main.go"}`, "read", "read main.go"},
		{"write", `{"path":"a.go"}`, "edit", "write a.go"},
		{"grep", `{"pattern":"func"}`, "search", "grep func"},
		{"bash", `{"command":"go test"}`, "execute", "bash go test"},
		{"mcp_thing", `{}`, "other", "mcp_thing"},
	} {
		if got := toolKind(tc.name); got != tc.kind {
			t.Errorf("toolKind(%q) = %q, want %q", tc.name, got, tc.kind)
		}
		if got := toolTitle(tc.name, tc.args); got != tc.title {
			t.Errorf("toolTitle(%q) = %q, want %q", tc.name, got, tc.title)
		}
	}
}

// An editor sends an attachment inline, as base64 with its type. It has to
// reach the model as an image rather than as a wall of base64 in the prose.
func TestPromptImagesAreDecoded(t *testing.T) {
	png := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...))

	got := promptImages([]ContentBlock{
		TextBlock("what is this"),
		{Type: "image", Data: png, MIMEType: "image/png", Name: "shot.png"},
	})

	if len(got) != 1 {
		t.Fatalf("decoded %d images, want 1", len(got))
	}
	if got[0].MIME != "image/png" || got[0].Name != "shot.png" {
		t.Errorf("image = %+v", got[0])
	}
	// The prose is unchanged: an image block is not text and must not become
	// any.
	if txt := promptText([]ContentBlock{TextBlock("what is this"),
		{Type: "image", Data: png, MIMEType: "image/png"}}); txt != "what is this" {
		t.Errorf("promptText = %q, want the prose alone", txt)
	}
}

// One unreadable attachment must not cost the whole prompt: the question is
// still worth answering, and refusing it outright is the harsher outcome.
func TestUndecodableImageIsSkippedNotFatal(t *testing.T) {
	got := promptImages([]ContentBlock{
		{Type: "image", Data: "not base64 at all!!", MIMEType: "image/png"},
		{Type: "image", Data: base64.StdEncoding.EncodeToString([]byte("GIF89a-------")), MIMEType: "image/gif"},
	})
	if len(got) != 1 || got[0].MIME != "image/gif" {
		t.Errorf("got %+v, want only the readable one", got)
	}
}
