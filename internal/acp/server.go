package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"raunen/internal/agent"
	"raunen/internal/attach"
	"raunen/internal/permission"
	"raunen/internal/provider"
	"raunen/internal/session"
)

// Expander rewrites an outgoing message, returning it with any named skills
// appended and the names it used. It matches config.ExpandSkills, which is what
// the terminal and a one-shot run both call: an editor can type #review as
// readily as a terminal can, and a skill should not stop working because of
// which front end sent the message.
type Expander func(string) (string, []string)

// Builder makes an agent for a working directory.
//
// The server does not know how to assemble one — that means config, providers,
// MCP servers, skills, project instructions and the fallback ladder, all of
// which main already wires together for the terminal. Passing a function keeps
// that in one place and keeps this package free of every one of those imports.
type Builder func(cwd string) (*agent.Agent, *session.Session, Expander, error)

// Server answers ACP over one connection.
type Server struct {
	conn  *conn
	build Builder
	// info is what this agent calls itself in the handshake.
	info Implementation

	mu       sync.Mutex
	sessions map[string]*acpSession
}

// acpSession is one conversation the client is holding.
type acpSession struct {
	id     string
	agent  *agent.Agent
	saved  *session.Session
	expand Expander

	// mu guards cancel, which is written when a turn starts and read when the
	// client asks to cancel it.
	mu     sync.Mutex
	cancel context.CancelFunc
	// cancelled records that the stop was asked for, so the turn can report
	// "cancelled" rather than the error cancellation produced.
	cancelled bool
}

// Serve runs the protocol over r and w until the input closes.
func Serve(ctx context.Context, r io.Reader, w io.Writer, version string, build Builder) error {
	s := &Server{
		conn:  newConn(r, w),
		build: build,
		info: Implementation{
			Name: "raunen", Title: "raunen", Version: version,
		},
		sessions: map[string]*acpSession{},
	}

	s.conn.handle("initialize", s.initialize)
	s.conn.handle("authenticate", s.authenticate)
	s.conn.handle("session/new", s.newSession)
	s.conn.handle("session/load", s.loadSession)
	s.conn.handle("session/prompt", s.prompt)
	s.conn.handle("session/cancel", s.cancel)
	s.conn.handle("session/set_mode", s.setMode)

	return s.conn.serve(ctx)
}

// initialize negotiates the version and reports what this agent can do.
func (s *Server) initialize(_ context.Context, raw json.RawMessage) (any, error) {
	var req InitializeRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, errorf(codeInvalidParams, "initialize: %v", err)
		}
	}
	// Answer with what we speak, which may be older than what was offered.
	// That is the point of the exchange; a client that cannot live with it
	// will say so.
	return InitializeResponse{
		ProtocolVersion: Version,
		AgentCapabilities: AgentCapabilities{
			LoadSession: true,
			PromptCapabilities: PromptCapabilities{
				// An image block is forwarded to the model as an attachment,
				// the same as one attached in the terminal. Audio is not: there
				// is nothing downstream that could act on it.
				Image:           true,
				Audio:           false,
				EmbeddedContext: true,
			},
			MCPCapabilities: MCPCapabilities{HTTP: true, SSE: true},
		},
		// None. raunen talks to whatever endpoint its config names, and a key
		// belongs in the config or the environment rather than in a protocol
		// handshake.
		AuthMethods: []AuthMethod{},
		AgentInfo:   &s.info,
	}, nil
}

// authenticate exists so a client that calls it gets a clear answer rather than
// "method not found", which reads as a broken agent.
func (s *Server) authenticate(context.Context, json.RawMessage) (any, error) {
	return nil, errorf(codeAuthRequired,
		"raunen has no authentication: configure a provider in its config file instead")
}

// newSession starts a conversation rooted at the requested directory.
func (s *Server) newSession(_ context.Context, raw json.RawMessage) (any, error) {
	var req NewSessionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, errorf(codeInvalidParams, "session/new: %v", err)
	}
	if strings.TrimSpace(req.Cwd) == "" {
		return nil, errorf(codeInvalidParams, "session/new: cwd is required")
	}

	// MCP servers offered by the client are deliberately not started. raunen
	// reads its own servers from mcp.json, which is a trusted file the user
	// controls; taking a server definition off the wire would let whatever is
	// driving the connection run a subprocess of its choosing.
	ag, saved, expand, err := s.build(req.Cwd)
	if err != nil {
		return nil, errorf(codeInternalError, "%s", err)
	}

	sess := &acpSession{id: saved.ID, agent: ag, saved: saved, expand: expand}
	s.mu.Lock()
	s.sessions[sess.id] = sess
	s.mu.Unlock()

	return NewSessionResponse{SessionID: sess.id, Modes: modeState(ag.Mode())}, nil
}

// loadSession reopens a session saved on disk, replaying its transcript into a
// fresh agent so the conversation carries on where it stopped.
func (s *Server) loadSession(_ context.Context, raw json.RawMessage) (any, error) {
	var req LoadSessionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, errorf(codeInvalidParams, "session/load: %v", err)
	}

	saved, err := session.Load(req.SessionID)
	if err != nil {
		return nil, errorf(codeInvalidParams, "no such session: %s", req.SessionID)
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = saved.Root
	}
	// The session built here is discarded: what is wanted is the agent, which
	// then takes the saved transcript. That is exactly what --continue does in
	// the terminal.
	ag, _, expand, err := s.build(cwd)
	if err != nil {
		return nil, errorf(codeInternalError, "%s", err)
	}
	ag.Restore(saved.Messages)

	sess := &acpSession{id: saved.ID, agent: ag, saved: saved, expand: expand}
	s.mu.Lock()
	s.sessions[sess.id] = sess
	s.mu.Unlock()

	return LoadSessionResponse{Modes: modeState(ag.Mode())}, nil
}

// setMode switches what the agent may do without asking.
func (s *Server) setMode(_ context.Context, raw json.RawMessage) (any, error) {
	var req SetModeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, errorf(codeInvalidParams, "session/set_mode: %v", err)
	}
	sess, err := s.session(req.SessionID)
	if err != nil {
		return nil, err
	}
	m, ok := modeByID(req.ModeID)
	if !ok {
		return nil, errorf(codeInvalidParams, "no such mode: %s", req.ModeID)
	}
	sess.agent.SetMode(m)

	// Told back to the client as well as answered, so an editor that offers the
	// modes in two places keeps both in step.
	_ = s.notifySession(sess.id, CurrentModeUpdate{
		SessionUpdate: updateCurrentMode,
		CurrentModeID: req.ModeID,
	})
	return nil, nil
}

// cancel asks the running turn to stop. A notification, so there is nothing to
// answer: the turn itself returns with a cancelled stop reason.
func (s *Server) cancel(_ context.Context, raw json.RawMessage) (any, error) {
	var req CancelNotification
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, nil
	}
	sess, err := s.session(req.SessionID)
	if err != nil {
		return nil, nil
	}
	sess.mu.Lock()
	sess.cancelled = true
	cancel := sess.cancel
	sess.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil, nil
}

// prompt runs one turn, streaming what happens as it happens.
func (s *Server) prompt(ctx context.Context, raw json.RawMessage) (any, error) {
	var req PromptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, errorf(codeInvalidParams, "session/prompt: %v", err)
	}
	sess, err := s.session(req.SessionID)
	if err != nil {
		return nil, err
	}

	text := promptText(req.Prompt)
	images := promptImages(req.Prompt)
	// A prompt that is nothing but an attachment is a real request — "what is
	// this" with the picture selected — so emptiness is judged on both.
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return nil, errorf(codeInvalidParams, "session/prompt: the prompt is empty")
	}
	if sess.expand != nil {
		text, _ = sess.expand(text)
	}

	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess.mu.Lock()
	sess.cancel = cancel
	sess.cancelled = false
	sess.mu.Unlock()

	events := make(chan agent.Event, 64)
	go sess.agent.RunWith(turnCtx, text, images, events)

	fw := newForwarder(func(u any) error { return s.notifySession(sess.id, u) })

	var (
		usage  Usage
		failed error
	)
	for ev := range events {
		switch e := ev.(type) {
		case agent.Usage:
			// Summed rather than replaced: escalation and the tool loop mean a
			// turn is many requests, and only the total is the bill.
			usage.InputTokens += e.Prompt
			usage.OutputTokens += e.Completion
			usage.TotalTokens += e.Total
		case agent.Approval:
			// The editor decides. Answering on the same connection is why the
			// read loop dispatches on goroutines: this blocks until the reply
			// arrives, and the reply has to be read while it does.
			e.Reply <- s.askPermission(turnCtx, sess, e)
		case agent.Failed:
			failed = e.Err
		default:
			fw.event(ev)
		}
	}

	// The transcript is saved whatever happened, so a turn that failed halfway
	// is still resumable — what the model did before it stopped is usually most
	// of the work.
	sess.saved.Messages = sess.agent.Messages()
	_ = sess.saved.Save()

	sess.mu.Lock()
	cancelled := sess.cancelled
	sess.cancel = nil
	sess.mu.Unlock()

	resp := PromptResponse{StopReason: StopEndTurn}
	if usage.TotalTokens > 0 {
		resp.Usage = &usage
	}
	switch {
	case cancelled:
		resp.StopReason = StopCancelled
	case failed != nil:
		// A turn that could not finish is reported as a refusal rather than as
		// a transport error: the connection is fine, and the client still wants
		// the usage and the partial output it already streamed.
		resp.StopReason = StopRefusal
		fw.thought("[" + failed.Error() + "]")
	}
	return resp, nil
}

// askPermission puts a tool call to the editor and waits.
//
// "Allow always" records a session grant through the same permission set the
// terminal uses, so the rule holds for sub-agents too and the next identical
// call never reaches the editor.
func (s *Server) askPermission(ctx context.Context, sess *acpSession, ap agent.Approval) bool {
	req := RequestPermissionRequest{
		SessionID: sess.id,
		ToolCall: ToolCallUpdate{
			ToolCallID: "permission",
			Status:     statusPending,
			Content: []ToolCallContent{{
				Type:    "content",
				Content: TextBlock(toolTitle(ap.Name, ap.Args)),
			}},
		},
		Options: []PermissionOption{
			{OptionID: "allow", Name: "Allow", Kind: kindAllowOnce},
			{OptionID: "always", Name: "Allow and don't ask again", Kind: kindAllowAlways},
			{OptionID: "reject", Name: "Reject", Kind: kindRejectOnce},
		},
	}

	var resp RequestPermissionResponse
	if err := s.conn.call(ctx, "session/request_permission", req, &resp); err != nil {
		// A client that cannot answer is not a client that meant yes.
		return false
	}
	if resp.Outcome.Outcome != "selected" {
		return false
	}
	switch resp.Outcome.OptionID {
	case "allow":
		return true
	case "always":
		if p := sess.agent.Permissions(); p != nil {
			p.Grant(ap.Name, grantPattern(ap.Name, targetOf(ap.Args)), permission.Allow)
		}
		return true
	}
	return false
}

// grantPattern works out what a single "always" should cover.
//
// Deliberately narrow, and the same rule the terminal uses: a command grants
// its verb, since the arguments vary between calls while the verb is what was
// read; a path grants that file alone.
func grantPattern(tool, target string) string {
	if target == "" {
		return ""
	}
	if tool != "bash" {
		return target
	}
	fields := strings.Fields(target)
	if len(fields) == 0 {
		return ""
	}
	if len(fields) > 1 && !strings.HasPrefix(fields[1], "-") && !strings.ContainsAny(fields[1], "/.") {
		return fields[0] + " " + fields[1] + " *"
	}
	return fields[0] + " *"
}

// promptText flattens a prompt into the string the agent takes.
//
// A resource link becomes its path rather than its contents: raunen has tools
// to read what it is pointed at, and choosing what to read out of a file is
// exactly the sort of thing they are for. An embedded resource is different —
// the editor has already inlined the text, and dropping it would lose context
// the user deliberately attached.
func promptText(blocks []ContentBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		switch blk.Type {
		case "text":
			b.WriteString(blk.Text)
		case "resource_link":
			if blk.URI != "" {
				b.WriteString(" " + strings.TrimPrefix(blk.URI, "file://"))
			}
		case "resource":
			var res struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			}
			if json.Unmarshal(blk.Resource, &res) == nil && res.Text != "" {
				name := strings.TrimPrefix(res.URI, "file://")
				b.WriteString("\n\n" + name + ":\n" + res.Text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// promptImages collects the attachments an editor sent inline.
//
// A block that fails to decode, or carries a format no model reads, is skipped
// rather than failing the turn: the prose is still a question worth answering,
// and refusing the whole prompt over one bad attachment is the harsher of the
// two outcomes.
func promptImages(blocks []ContentBlock) []provider.Image {
	var out []provider.Image
	for i, blk := range blocks {
		if blk.Type != "image" || blk.Data == "" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(blk.Data)
		if err != nil {
			continue
		}
		name := blk.Name
		if name == "" {
			name = fmt.Sprintf("image %d", i+1)
		}
		img, err := attach.FromBytes(data, name)
		if err != nil {
			continue
		}
		out = append(out, img)
	}
	return out
}

// notifySession sends one streamed update.
func (s *Server) notifySession(id string, update any) error {
	return s.conn.notify("session/update", SessionNotification{SessionID: id, Update: update})
}

// session looks one up, with an error a client can act on.
func (s *Server) session(id string) (*acpSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, errorf(codeInvalidParams, "no such session: %s", id)
	}
	return sess, nil
}

// Mode ids, as they appear on the wire. They match what the terminal calls
// them, so a user reading both sees one vocabulary.
const (
	modeAuto   = "auto"
	modeAccept = "accept-edits"
	modePlan   = "plan"
)

// modeState describes the modes for a handshake.
func modeState(current agent.Mode) *SessionModeState {
	return &SessionModeState{
		CurrentModeID: modeID(current),
		AvailableModes: []SessionMode{
			{ID: modeAuto, Name: "Auto", Description: "Every tool runs immediately."},
			{ID: modeAccept, Name: "Accept edits", Description: "Ask before anything that changes state."},
			{ID: modePlan, Name: "Plan", Description: "Refuse changes; investigate and propose."},
		},
	}
}

func modeID(m agent.Mode) string {
	switch m {
	case agent.ModeAccept:
		return modeAccept
	case agent.ModePlan:
		return modePlan
	default:
		return modeAuto
	}
}

func modeByID(id string) (agent.Mode, bool) {
	switch id {
	case modeAuto:
		return agent.ModeAuto, true
	case modeAccept:
		return agent.ModeAccept, true
	case modePlan:
		return agent.ModePlan, true
	}
	return agent.ModeAuto, false
}
