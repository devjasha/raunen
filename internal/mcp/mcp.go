// Package mcp connects to Model Context Protocol servers and turns the tools
// they advertise into raunen tools.
//
// An MCP server is apeer that speaks JSON-RPC 2.0. raunen supports two transports
// behind the same Client surface, chosen by the server's "type":
//
//   - "stdio" (the default): a subprocess that speaks JSON-RPC on its
//     stdin/stdout, one JSON object per line. The protocol is described at
//     https://modelcontextprotocol.io.
//   - "http": a remote server over Streamable HTTP — POST a JSON-RPC body to the
//     URL, accept either application/json or text/event-stream back, and carry
//     the Mcp-Session-Id header returned by initialize onto later requests.
//   - "sse": the legacy Streamable SSE pattern — a long-lived GET opens the
//     server→client event stream (Accept: text/event-stream) and each JSON-RPC
//     call is a POST to the same URL; the server may answer the POST in JSON or
//     with its own SSE frames, or defer the result onto the GET stream.
//
// Both remote transports carry the Mcp-Session-Id returned during initialize so
// the server keeps the same session. A server that advertises
// Tools.ListChanged sends notifications/tools/list_changed when its toolset
// changes, and the Client re-lists and reports the new set through OnToolsChanged.
//
// Only the parts raunen needs are implemented — initialize, tools/list,
// tools/call — so a server that offers resources or prompts is fine, those are
// simply ignored. The transport (the wire) and the Client (the tool conversion)
// are separate: a transport only knows how to send a request and get a response
// back, and the Client drives the handshake and turns the result into tools.Tool.
//
// Each tool the server lists becomes a tools.Tool whose Run forwards the model's
// arguments over the wire. They flow through exactly the same path as the
// built-in read/write/bash tools: the same modes, the same approval prompt, the
// same output budget. A server is just a different kind of backend.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"raunen/internal/tools"
)

// Server is the configuration for one MCP server, as it appears in either the
// user's config ("mcp") or the defaults.
type Server struct {
	// Command is the program to run for a stdio server, resolved on PATH like
	// any other command.
	Command string `json:"command"`
	// Args are passed to Command after its name.
	Args []string `json:"args,omitempty"`
	// Env is extra environment for the server process — typically a token it
	// needs. Inherited from the parent, so PATH carries over. Ignored for http.
	Env map[string]string `json:"env,omitempty"`
	// Type selects the transport: "" or "stdio" for a subprocess, "http" for a
	// remote Streamable-HTTP server, or "sse" for the legacy GET-stream / POST
	// pattern. Empty means stdio, matching older configs.
	Type string `json:"type,omitempty"`
	// URL is the Streamable-HTTP endpoint, used when Type is "http".
	URL string `json:"url,omitempty"`
	// Headers are extra HTTP headers for the remote server — an Authorization
	// bearer token, say. Forwarded verbatim on every request.
	Headers map[string]string `json:"headers,omitempty"`
	// Enabled is consulted by the caller, not here: a server can be defined but
	// left off so it is not started until the user asks for it.
	Enabled bool `json:"enabled,omitempty"`
	// OAuth, when present, turns on OAuth 2.1 for a remote server: a 401 starts
	// discovery and a browser login instead of failing the request. An empty
	// block is the normal case — everything is discovered from the server. Its
	// absence means no OAuth at all, so an existing config behaves as before.
	OAuth *OAuth `json:"oauth,omitempty"`
	// TokenStore is where tokens are persisted. Nil uses the on-disk store at
	// TokenPath; tests inject their own.
	TokenStore TokenStore `json:"-"`
	// OpenBrowser opens the authorization URL. Nil uses the platform opener.
	OpenBrowser func(string) error `json:"-"`
}

// transport is everything that differs between stdio and http. A Client drives
// the protocol on top of one; the transport just turns a request into a response
// and delivers notifications, with no idea what method it is carrying.
type transport interface {
	// request sends a JSON-RPC call with an id and waits for the matching
	// response, returning the peeled {"result": ...} body. An error here is a
	// protocol or transport failure, not a tool error.
	request(ctx context.Context, method string, params any) (json.RawMessage, error)
	// notify sends a notification (no id, no reply awaited).
	notify(ctx context.Context, method string, params any) error
	// OnNotification registers a callback invoked for every JSON-RPC notification
	// the server sends (a message with no id). It is safe to call once before any
	// request; the callback may be invoked from a transport read goroutine.
	OnNotification(cb func(method string, params json.RawMessage))
	// close stops the transport and any backend it owns.
	close() error
}

// spec is the shape a tool arrives in from tools/list. The annotations are
// optional hints; only readOnlyHint is used to gate behaviour.
type spec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// InputSchema is a JSON Schema object; it is forwarded to the model
	// unchanged so the server receives what it expects.
	InputSchema map[string]any `json:"inputSchema"`
	// Annotations are advisory metadata from the server.
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
}

// Client is a live connection to one MCP server. It owns the transport and the
// tools discovered from it; Close stops the transport.
type Client struct {
	name string
	s    Server
	t    transport

	// mu guards tools and onToolsChanged. A tools/list_changed notification is
	// delivered on the transport's read goroutine, so a live refresh can rewrite
	// the toolset while the agent is reading it.
	mu sync.Mutex

	tools []tools.Tool

	// listings caches resource and prompt lists until the server says they
	// changed. A list is re-read (a round trip per page) on every model call
	// that asks for it, so without the cache a large server costs a request per
	// page each time. Guarded by mu because list_changed refreshes it from the
	// transport's read goroutine.
	listings *listings

	// serverProtocolVersion is the version the server agreed to speak, from the
	// initialize result. Decoded so later code can adapt to what the server
	// actually supports rather than assume our request won.
	serverProtocolVersion string
	// caps is the server's advertised capabilities from the initialize result.
	caps ServerCapabilities

	// onToolsChanged, when set, is invoked after a tools/list_changed notification
	// triggers a successful refresh of the toolset. It carries the server name and
	// the refreshed tools so the registry can swap them in live.
	onToolsChanged func(name string, tools []tools.Tool)
}

// Start launches the server with the transport its type asks for and performs
// the MCP handshake. It returns an error if the server cannot be reached or does
// not initialize, in which case the (possibly started) transport is closed.
func Start(ctx context.Context, name string, s Server) (*Client, error) {
	var t transport
	var err error
	switch strings.ToLower(strings.TrimSpace(s.Type)) {
	case "", "stdio":
		if s.Command == "" {
			return nil, fmt.Errorf("mcp %q: no command", name)
		}
		t, err = newStdio(name, s)
	case "http":
		if s.URL == "" {
			return nil, fmt.Errorf("mcp %q: http type requires a url", name)
		}
		t, err = newHTTP(name, s)
	case "sse":
		if s.URL == "" {
			return nil, fmt.Errorf("mcp %q: sse type requires a url", name)
		}
		t, err = newSSE(name, s)
	default:
		return nil, fmt.Errorf("mcp %q: unknown type %q", name, s.Type)
	}
	if err != nil {
		return nil, err
	}
	c := &Client{name: name, t: t, s: s, listings: &listings{}}
	if err := c.initialize(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("mcp %q: %w", name, err)
	}
	if err := c.listTools(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("mcp %q: %w", name, err)
	}
	// Register a live notification handler so a server advertising ListChanged can
	// refresh our toolset without polling.
	//
	// The refresh runs on its own goroutine, and deliberately not on the one that
	// delivered the notification: that is the transport's read loop, and the
	// tools/list it issues can only be answered by that same loop, so calling it
	// inline deadlocks until the context expires. The Start context is not reused
	// either — it covers startup and is usually cancelled by the time a server
	// changes its mind — so the re-list gets a fresh timeout of its own.
	// A server that advertises ListChanged may revise its toolset, its resources,
	// or its prompts while we are running. The notifications arrive on the
	// transport's read loop, and re-listing the same kind of thing can only be
	// answered by that loop — so handling them inline would deadlock; a fresh
	// goroutine with its own timeout is used instead. The Start context is not
	// reused: it covers startup and is usually cancelled by the time a server
	// changes its mind. The cached resource and prompt listings are dropped on
	// their notifications, since a cached list is only safe until the server says
	// it changed.
	if c.caps.Tools.ListChanged || (c.caps.Resources != nil && c.caps.Resources.ListChanged) ||
		(c.caps.Prompts != nil && c.caps.Prompts.ListChanged) {
		c.t.OnNotification(func(method string, params json.RawMessage) {
			switch method {
			case "notifications/tools/list_changed":
				go c.refreshTools()
			case "notifications/resources/list_changed":
				go c.dropResources()
			case "notifications/prompts/list_changed":
				go func() {
					c.mu.Lock()
					l := c.listings
					c.mu.Unlock()
					if l != nil {
						l.setPrompts(nil)
					}
				}()
			}
		})
	}
	return c, nil
}

// refreshTools re-lists the toolset after a tools/list_changed notification and
// hands the result to the registered callback, if any.
func (c *Client) refreshTools() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.listTools(ctx); err != nil {
		return
	}
	c.mu.Lock()
	cb := c.onToolsChanged
	c.mu.Unlock()
	if cb != nil {
		cb(c.name, c.Tools())
	}
}

// dropResources forgets the cached resource listing, which is all a listing is
// good for until the server says it changed. Prompts have their own field.
func (c *Client) dropResources() {
	c.mu.Lock()
	l := c.listings
	c.mu.Unlock()
	if l != nil {
		l.setResources(nil)
	}
}

// Tools returns the tools this server advertised, ready to register. The slice
// is a copy, so a live refresh cannot rewrite it underneath a caller.
func (c *Client) Tools() []tools.Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]tools.Tool(nil), c.tools...)
}

// Name reports the server's configured name.
func (c *Client) Name() string { return c.name }

// SetOnToolsChanged installs a callback that fires after a tools/list_changed
// notification triggers a live refresh of the server's toolset. It is a no-op
// until the transport delivers such a notification.
func (c *Client) SetOnToolsChanged(cb func(name string, tools []tools.Tool)) {
	c.mu.Lock()
	c.onToolsChanged = cb
	c.mu.Unlock()
}

// ServerProtocolVersion reports the protocol version the server negotiated during
// initialize. Useful for diagnostics and for follow-up transports that must
// speak the same version.
func (c *Client) ServerProtocolVersion() string { return c.serverProtocolVersion }

// Capabilities reports the server's advertised capabilities from initialize.
func (c *Client) Capabilities() ServerCapabilities { return c.caps }

// Close stops the server. The agent keeps running; its tools simply stop
// answering.
func (c *Client) Close() error { return c.t.close() }

// Ping sends a JSON-RPC ping to the server and returns the error. A nil error
// means the connection is live; a non-nil error means the transport is
// dead/unreachable and the caller should consider restarting.
func (c *Client) Ping(ctx context.Context) error {
	return c.call(ctx, "ping", nil, &struct{}{})
}

// restart rebuilds the transport from the saved server config and re-runs the
// handshake, re-discovering tools. It closes the old transport first so a
// crashed/dead subprocess is released, then swaps in the new one. This is the
// building block behind lazy reconnect and live tool refresh.
func (c *Client) restart(ctx context.Context) error {
	var nt transport
	var err error
	switch strings.ToLower(strings.TrimSpace(c.s.Type)) {
	case "", "stdio":
		if c.s.Command == "" {
			return fmt.Errorf("mcp %q: no command", c.name)
		}
		nt, err = newStdio(c.name, c.s)
	case "http":
		if c.s.URL == "" {
			return fmt.Errorf("mcp %q: http type requires a url", c.name)
		}
		nt, err = newHTTP(c.name, c.s)
	case "sse":
		if c.s.URL == "" {
			return fmt.Errorf("mcp %q: sse type requires a url", c.name)
		}
		nt, err = newSSE(c.name, c.s)
	default:
		return fmt.Errorf("mcp %q: unknown type %q", c.name, c.s.Type)
	}
	if err != nil {
		return err
	}
	// Release the old transport (guard against a nil one during early failures).
	if c.t != nil {
		_ = c.t.close()
	}
	c.t = nt
	if err := c.initialize(ctx); err != nil {
		nt.close()
		return fmt.Errorf("mcp %q: %w", c.name, err)
	}
	if err := c.listTools(ctx); err != nil {
		nt.close()
		return fmt.Errorf("mcp %q: %w", c.name, err)
	}
	return nil
}

// Reload rebuilds the connection and returns a fresh set of tools after the
// server reports tools/list_changed. It is the live-refresh entry point.
func (c *Client) Reload(ctx context.Context) ([]tools.Tool, error) {
	if err := c.restart(ctx); err != nil {
		return nil, err
	}
	return c.Tools(), nil
}

func (c *Client) initialize(ctx context.Context) error {
	// call already peels the {"result": ...} envelope, so this decodes the result
	// body directly. Wrapping it in another "result" field would silently leave
	// every field zero — and a zero capability set means no live tool refresh.
	var res InitializeResult
	if err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		// We advertise the tools capability so a server knows we will call
		// tools/list and tools/call. We do not claim prompts/resources/logging,
		// since raunen does not use them.
		"capabilities": map[string]any{"tools": map[string]any{}},
		"clientInfo":   map[string]any{"name": "raunen", "version": "dev"},
	}, &res); err != nil {
		return err
	}
	// Record what the server actually agreed to so later code can adapt. A
	// protocol-level error would have surfaced from call as an error already.
	c.serverProtocolVersion = res.ProtocolVersion
	c.caps = res.Capabilities
	// initialize is followed by an optional initialized notification; we do not
	// wait for a reply to it, so just send it and move on.
	_ = c.t.notify(ctx, "notifications/initialized", map[string]any{})
	return nil
}

func (c *Client) listTools(ctx context.Context) error {
	res := struct {
		Error *rpcError `json:"error"`
		Tools []spec    `json:"tools"`
	}{}
	if err := c.call(ctx, "tools/list", map[string]any{}, &res); err != nil {
		return err
	}
	if res.Error != nil {
		return res.Error
	}
	// Build the new set before taking the lock, so a reader never observes a
	// half-written toolset during a live refresh.
	var built []tools.Tool
	for _, t := range res.Tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		name := t.Name
		desc := t.Description
		// A tool's mutating status is taken from its readOnlyHint when the server
		// sends one: a hint of false (or its absence) means "can change state",
		// which is the safe default for an untrusted peer and is gated by
		// plan/accept exactly like bash. A hint of true means the server promises
		// the tool only reads, so the tool is allowed in plan mode. The other
		// hints (destructive/openWorld) are advisory only and never relax this.
		mutates := true
		if t.Annotations != nil && t.Annotations.ReadOnlyHint != nil {
			mutates = !*t.Annotations.ReadOnlyHint
		}
		// Capture per-tool state in values, so each closure sees its own.
		built = append(built, tools.Tool{
			Name:        name,
			Description: "[" + c.name + "] " + desc,
			Params:      schema,
			Mutates:     mutates,
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				return c.callTool(ctx, name, args)
			},
		})
	}
	// Replace the tool set rather than appending, so a reload (after a restart or
	// a tools/list_changed notification) reflects the server's current tools
	// exactly, instead of duplicating the previous set.
	c.mu.Lock()
	c.tools = built
	c.mu.Unlock()
	return nil
}

func (c *Client) callTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if len(strings.TrimSpace(string(args))) == 0 {
		args = json.RawMessage("{}")
	}
	// call already peeled the {"result": ...} envelope, so this decodes the
	// result body directly: {content: [...], isError: bool}.
	res := struct {
		Error   *rpcError `json:"error"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}{}
	// Issue the call. If it fails because the transport is dead or unreachable,
	// attempt exactly ONE restart and retry once — no loop. This keeps a crashed
	// stdio server or a dropped HTTP connection from failing the turn outright.
	err := c.call(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	}, &res)
	if isTransportError(err) {
		if rerr := c.restart(ctx); rerr == nil {
			err = c.call(ctx, "tools/call", map[string]any{
				"name":      name,
				"arguments": args,
			}, &res)
		}
	}
	if err != nil {
		return "", err
	}
	if res.Error != nil {
		// The server returned a JSON-RPC error object; include its code so the
		// caller (and anyone debugging) can tell protocol errors apart.
		return "", fmt.Errorf("mcp %q tool %q: [%d] %s", c.name, name, res.Error.Code, res.Error.Message)
	}
	var sb strings.Builder
	for i, part := range res.Content {
		if part.Type == "text" {
			if i > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(part.Text)
		}
	}
	out := sb.String()
	if res.IsError {
		// The server flagged the tool as failed. Surface its message to the
		// caller as a tool error (not a panic) so the agent can show it to the
		// model as a result.
		return out, fmt.Errorf("mcp %q tool %q: %s", c.name, name, out)
	}
	return out, nil
}

// call sends one request and decodes its result into dst. Result carries the
// transport-agnostic half of the old per-transport call: peel the response
// envelope so the caller deals only with the result body.
func (c *Client) call(ctx context.Context, method string, params any, dst any) error {
	raw, err := c.t.request(ctx, method, params)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcError is a JSON-RPC error object. Its Code is the protocol-level error code
// (e.g. -32602 invalid params), so callers can branch on it if they need to.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

// isTransportError reports whether err looks like a transport-level failure
// (dead/unreachable connection) rather than a tool or protocol error. We detect
// via substring on the error string so the transport interface need not change.
func isTransportError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "closed") ||
		strings.Contains(msg, "connection") ||
		strings.Contains(msg, "server exited")
}
