package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"raunen/internal/tools"
)

// newMock starts the testdata server as an MCP server and fails the test if it
// does not come up.
func newMock(t *testing.T) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Start(ctx, "mock", Server{Command: "go", Args: []string{"run", "./testdata/mockserver"}})
	if err != nil {
		t.Fatalf("start mock server: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// The handshake must complete and the advertised tool must be converted to a
// raunen tool with the server name baked into its description.
func TestStartListsTools(t *testing.T) {
	c := newMock(t)
	if len(c.Tools()) != 2 {
		t.Fatalf("tools = %d, want 2", len(c.Tools()))
	}
	// ping is advertised without any annotations, so it keeps the safe default
	// of Mutates:true.
	ping := findTool(t, c, "ping")
	if ping.Name != "ping" {
		t.Errorf("name = %q, want ping", ping.Name)
	}
	if !ping.Mutates {
		t.Error("MCP tools without annotations should be mutating by default")
	}
	if ping.Description == "" {
		t.Error("description should be carried over")
	}
}

// A server that sends readOnlyHint:true on a tool must yield a non-mutating
// raunen tool, while a tool with no annotation stays mutating. This exercises
// the end-to-end path through tools/list.
func TestReadOnlyHint(t *testing.T) {
	c := newMock(t)

	ping := findTool(t, c, "ping")
	if !ping.Mutates {
		t.Error("ping has no annotations, so it should be Mutates:true")
	}

	lookup := findTool(t, c, "lookup")
	if lookup.Mutates {
		t.Error("lookup declares readOnlyHint:true, so it should be Mutates:false")
	}
}

// findTool returns the tool with the given server name, failing the test if it
// is missing. Names are matched by the raw server name before the registry ever
// prefixes them with the server name on collision.
func findTool(t *testing.T, c *Client, name string) tools.Tool {
	t.Helper()
	for _, tool := range c.Tools() {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found among %d tools: %v", name, len(c.Tools()), c.Tools())
	return tools.Tool{}
}

// Calling a tool must forward the arguments and return the server's text.
func TestCallToolRoundTrips(t *testing.T) {
	c := newMock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := c.Tools()[0].Run(ctx, json.RawMessage(`{"msg":"hello"}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "pong: hello" {
		t.Errorf("result = %q, want %q", out, "pong: hello")
	}
}

// A server-reported error must come back as a tool error, not a panic, so the
// agent can hand it to the model as a result.
func TestCallToolSurfacesServerError(t *testing.T) {
	c := newMock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.Tools()[0].Run(ctx, json.RawMessage(`{"msg":"boom"}`)); err == nil {
		t.Fatal("expected an error from a failing tool")
	}
}

// An empty argument list is tolerated: the protocol wants an object, so a bare
// call must not break the server round-trip.
func TestCallToolEmptyArgs(t *testing.T) {
	c := newMock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// ping requires msg, but the client must at least not choke on "{}".
	_, _ = c.Tools()[0].Run(ctx, json.RawMessage(`{}`))
}

// A server that does not respond must make the call fail rather than hang
// forever, so a dead backend is reported instead of stalling the turn.
func TestDeadServerFailsCall(t *testing.T) {
	// "false" exits immediately, so initialize never completes.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Start(ctx, "dead", Server{Command: "false"})
	if err == nil {
		t.Fatal("expected start to fail for a server that exits immediately")
	}
}

// InitializeResult and ServerCapabilities must decode the fields raunen relies
// on, including the nested ListChanged hint, without disturbing each other.
func TestInitializeResultDecodes(t *testing.T) {
	raw := `{
		"protocolVersion": "2024-11-05",
		"capabilities": {"tools": {"listChanged": true}},
		"serverInfo": {"name": "mockserver"}
	}`
	var res InitializeResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.ProtocolVersion != "2024-11-05" {
		t.Errorf("protocolVersion = %q, want 2024-11-05", res.ProtocolVersion)
	}
	if !res.Capabilities.Tools.ListChanged {
		t.Error("Capabilities.Tools.ListChanged = false, want true")
	}
	if res.ServerInfo.Name != "mockserver" {
		t.Errorf("serverInfo.name = %q, want mockserver", res.ServerInfo.Name)
	}
}

// ToolAnnotations must decode each hint independently, with pointers so an
// absent hint is distinguishable from an explicit false.
func TestToolAnnotationsDecodes(t *testing.T) {
	raw := `{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": true}`
	var a ToolAnnotations
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if a.ReadOnlyHint == nil || !*a.ReadOnlyHint {
		t.Error("readOnlyHint should decode to *true")
	}
	if a.DestructiveHint == nil || *a.DestructiveHint {
		t.Error("destructiveHint should decode to *false")
	}
	if a.OpenWorldHint == nil || !*a.OpenWorldHint {
		t.Error("openWorldHint should decode to *true")
	}
}

// Ping must succeed against a live mock server, confirming the connection is
// healthy end to end.
func TestPingSucceeds(t *testing.T) {
	c := newMock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

// Reload must rebuild the connection and return a fresh tool set whose "ping"
// tool keeps its original shape.
func TestReloadRestoresTools(t *testing.T) {
	c := newMock(t)

	before := findTool(t, c, "ping")
	beforeDesc := before.Description
	beforeMutates := before.Mutates

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tools, err := c.Reload(ctx)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	// The freshly discovered set should still contain the same tools.
	if len(tools) != 2 {
		t.Fatalf("reloaded tools = %d, want 2", len(tools))
	}
	reloaded := findTool(t, c, "ping")
	if reloaded.Description != beforeDesc {
		t.Errorf("reloaded ping description = %q, want %q", reloaded.Description, beforeDesc)
	}
	if reloaded.Mutates != beforeMutates {
		t.Errorf("reloaded ping Mutates = %v, want %v", reloaded.Mutates, beforeMutates)
	}

	// And it must still round-trip a tool call after the restart.
	out, err := reloaded.Run(ctx, json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatalf("run after reload: %v", err)
	}
	if out != "pong: hi" {
		t.Errorf("result after reload = %q, want %q", out, "pong: hi")
	}
}

// After a manual restart, a tool call must still round-trip through the new
// transport. This exercises the same rebuild path the lazy reconnect uses.
func TestCallToolAfterRestart(t *testing.T) {
	c := newMock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.restart(ctx); err != nil {
		t.Fatalf("restart: %v", err)
	}
	out, err := c.Tools()[0].Run(ctx, json.RawMessage(`{"msg":"again"}`))
	if err != nil {
		t.Fatalf("run after restart: %v", err)
	}
	if out != "pong: again" {
		t.Errorf("result = %q, want %q", out, "pong: again")
	}
}

// An "sse" server without a URL must be rejected with an error mentioning the
// missing url, just like the "http" type. This needs no live server.
func TestSSETypeRequiresURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Start(ctx, "x", Server{Type: "sse"})
	if err == nil {
		t.Fatal("expected an error for sse with no url")
	}
	if !strings.Contains(err.Error(), "url") {
		t.Errorf("error = %q, want it to mention url", err.Error())
	}
}

// newSSE must construct without a live server (the GET stream is opened in the
// background) and Close must not error. We point it at a port nothing listens on
// so the background connection simply fails without panicking.
func TestSSEConstruction(t *testing.T) {
	tr, err := newSSE("x", Server{URL: "http://localhost:1/sse"})
	if err != nil {
		t.Fatalf("newSSE: %v", err)
	}
	if tr == nil {
		t.Fatal("newSSE returned nil transport")
	}
	if err := tr.close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// A tools/list_changed notification must drive a re-list and hand the refreshed
// set to the registered callback. This is the whole point of the live refresh:
// /mcp and /status should reflect a server that grew a tool without a restart.
func TestToolsChangedRefreshes(t *testing.T) {
	c := newMock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The server only advertises the extra tool after "announce", so the initial
	// listing must not contain it.
	if len(c.Tools()) != 2 {
		t.Fatalf("tools before = %d, want 2", len(c.Tools()))
	}

	got := make(chan []tools.Tool, 1)
	c.SetOnToolsChanged(func(name string, ts []tools.Tool) {
		if name != "mock" {
			t.Errorf("callback server = %q, want mock", name)
		}
		// Non-blocking: the notification may arrive more than once.
		select {
		case got <- ts:
		default:
		}
	})

	// announce makes the server emit notifications/tools/list_changed.
	if _, err := c.callTool(ctx, "announce", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("announce: %v", err)
	}

	select {
	case ts := <-got:
		if len(ts) != 3 {
			t.Errorf("refreshed tools = %d, want 3", len(ts))
		}
		var found bool
		for _, tool := range ts {
			if tool.Name == "extra" {
				found = true
			}
		}
		if !found {
			t.Errorf("refreshed set is missing the new tool: %v", ts)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the tools/list_changed callback")
	}
}
