package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
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
	if len(c.Tools()) != 1 {
		t.Fatalf("tools = %d, want 1", len(c.Tools()))
	}
	tool := c.Tools()[0]
	if tool.Name != "ping" {
		t.Errorf("name = %q, want ping", tool.Name)
	}
	if !tool.Mutates {
		t.Error("MCP tools should be mutating by default")
	}
	if tool.Description == "" {
		t.Error("description should be carried over")
	}
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
