package main

import (
	"path/filepath"
	"testing"
	"time"

	"raunen/internal/config"
	"raunen/internal/mcp"
	"raunen/internal/tools"
)

// mockDef is the stdio mock server the mcp package's own tests use. Running it
// with `go run` is slow enough (a compile on the first call) to make the point
// of these tests: startup must not be waiting for it.
func mockDef() config.MCP {
	return config.MCP{Command: "go", Args: []string{"run", "./internal/mcp/testdata/mockserver"}}
}

// slowDef is the same mock server behind a delay, so a test can tell "returned
// before the handshake finished" from "the handshake was simply quick".
func slowDef(d string) config.MCP {
	return config.MCP{
		Command: "sh",
		Args:    []string{"-c", "sleep " + d + "; exec go run ./internal/mcp/testdata/mockserver"},
	}
}

// startMCP must return before the servers have finished connecting. That is the
// whole reason it exists in this shape: connecting is a round trip per server,
// and paying for it before the first frame is what made raunen feel slow to
// start. The server is deliberately slow, or a quick handshake would let a
// blocking implementation pass too.
func TestStartMCPDoesNotBlock(t *testing.T) {
	cfg := &config.Config{MCP: map[string]config.MCP{"slow": slowDef("3")}}

	start := time.Now()
	ss := startMCP(cfg)
	defer ss.Close()
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Errorf("startMCP blocked for %v; it should return before the servers connect", elapsed)
	}
}

// Servers are dialled concurrently, so several slow ones cost what the slowest
// one costs rather than the sum. A server that will not answer must not hold up
// the ones behind it.
func TestServersDialConcurrently(t *testing.T) {
	cfg := &config.Config{MCP: map[string]config.MCP{
		"a": slowDef("3"),
		"b": slowDef("3"),
		"c": slowDef("3"),
	}}

	ss := startMCP(cfg)
	defer ss.Close()

	start := time.Now()
	ss.Wait()
	elapsed := time.Since(start)

	// Sequentially this would be at least nine seconds. Well clear of both.
	if elapsed > 6*time.Second {
		t.Errorf("three servers took %v; they are being dialled one after another", elapsed)
	}
	if n := len(ss.clients); n != 3 {
		t.Errorf("connected to %d servers, want 3", n)
	}
}

// Attach folds the tools into the registry the agent is already holding, so a
// tool that arrives after the terminal has drawn is still callable. The agent
// re-reads its toolset every step, which is what makes the late arrival safe.
func TestAttachAddsToolsInPlace(t *testing.T) {
	cfg := &config.Config{MCP: map[string]config.MCP{"mock": mockDef()}}
	ss := startMCP(cfg)
	defer ss.Close()

	reg := tools.Default(t.TempDir(), tools.OutputBudget(0))
	before := len(reg.Names())

	ss.Attach(reg)

	if got := len(reg.Names()); got <= before {
		t.Fatalf("Attach added no tools: %d -> %d", before, got)
	}
	if counts := ss.Counts(); counts["mock"] == 0 {
		t.Errorf("the mock server contributed no tools: %v", counts)
	}
}

// Waiting must be idempotent and safe from several goroutines: the UI waits on
// readiness before its first turn while the background wiring is still running.
func TestWaitIsRepeatable(t *testing.T) {
	cfg := &config.Config{MCP: map[string]config.MCP{"mock": mockDef()}}
	ss := startMCP(cfg)
	defer ss.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		ss.Wait()
		ss.Wait()
	}()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("Wait did not return")
	}
}

// With no servers configured there is nothing to wait for, and Wait must not
// block on a channel that will never be closed.
func TestWaitWithNoServersReturns(t *testing.T) {
	ss := startMCP(&config.Config{})
	defer ss.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		ss.Wait()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait blocked with no servers configured")
	}
}

// A server that fails to start must say why somewhere the user can still read
// it. Connecting now finishes after the alternate screen is open, so stderr
// alone is not enough — anything written there from that point is wiped when
// the screen is handed back. The reason is kept for /mcp instead.
func TestFailureReasonIsKept(t *testing.T) {
	cfg := &config.Config{MCP: map[string]config.MCP{
		"broken": {Command: "/nonexistent/mcp-server"},
	}}
	ss := startMCP(cfg)
	defer ss.Close()
	ss.Wait()

	fails := ss.Failures()
	if fails["broken"] == "" {
		t.Fatalf("no reason recorded for a server that could not start: %v", fails)
	}
	if len(ss.clients) != 0 {
		t.Errorf("a server that failed to start was kept as a client")
	}
}

// A server that may open a browser has to connect before the terminal draws:
// its flow prints an authorization URL and then waits for a human, and once the
// alternate screen is open that instruction is written where it cannot be read.
// Everything else connects in the background.
func TestOnlyUnauthorizedOAuthServersAreEager(t *testing.T) {
	defs := map[string]config.MCP{
		"plain": mockDef(),
		"needs": {Type: "http", URL: "https://example.invalid/mcp", OAuth: &config.MCPOAuth{Issuer: "https://issuer.invalid"}},
	}
	eager, deferred := splitInteractive(defs)

	if _, ok := eager["needs"]; !ok {
		t.Error("an OAuth server with no stored token must connect before the first frame")
	}
	if _, ok := deferred["plain"]; !ok {
		t.Error("a server that cannot prompt should not hold up the first frame")
	}
	if _, ok := eager["plain"]; ok {
		t.Error("a plain server was made eager, which is the wait we removed")
	}
}

// An OAuth server that has been authorized before refreshes without asking
// anything, so it belongs in the background like any other.
func TestAuthorizedOAuthServerIsDeferred(t *testing.T) {
	dir := t.TempDir()
	store := mcp.NewFileStore(filepath.Join(dir, "tokens.json"))
	if err := store.Save("https://issuer.invalid", "", &mcp.Token{AccessToken: "cached"}); err != nil {
		t.Fatal(err)
	}
	def := config.MCP{Type: "http", URL: "https://example.invalid/mcp",
		OAuth: &config.MCPOAuth{Issuer: "https://issuer.invalid"}}

	if !hasToken(store, def) {
		t.Fatal("a saved token was not found, so every run would wait for a browser")
	}
}
