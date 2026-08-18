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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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
	// remote Streamable-HTTP server. Empty means stdio, matching older configs.
	Type string `json:"type,omitempty"`
	// URL is the Streamable-HTTP endpoint, used when Type is "http".
	URL string `json:"url,omitempty"`
	// Headers are extra HTTP headers for the remote server — an Authorization
	// bearer token, say. Forwarded verbatim on every request.
	Headers map[string]string `json:"headers,omitempty"`
	// Enabled is consulted by the caller, not here: a server can be defined but
	// left off so it is not started until the user asks for it.
	Enabled bool `json:"enabled,omitempty"`
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
	// close stops the transport and any backend it owns.
	close() error
}

// spec is the shape a server's tools arrive in from tools/list.
type spec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// InputSchema is a JSON Schema object; it is forwarded to the model
	// unchanged so the server receives what it expects.
	InputSchema map[string]any `json:"inputSchema"`
}

// Client is a live connection to one MCP server. It owns the transport and the
// tools discovered from it; Close stops the transport.
type Client struct {
	name string
	t    transport

	tools []tools.Tool
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
	default:
		return nil, fmt.Errorf("mcp %q: unknown type %q", name, s.Type)
	}
	if err != nil {
		return nil, err
	}
	c := &Client{name: name, t: t}
	if err := c.initialize(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("mcp %q: %w", name, err)
	}
	if err := c.listTools(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("mcp %q: %w", name, err)
	}
	return c, nil
}

// Tools returns the tools this server advertised, ready to register.
func (c *Client) Tools() []tools.Tool { return c.tools }

// Name reports the server's configured name.
func (c *Client) Name() string { return c.name }

// Close stops the server. The agent keeps running; its tools simply stop
// answering.
func (c *Client) Close() error { return c.t.close() }

func (c *Client) initialize(ctx context.Context) error {
	var res struct {
		Error *rpcError `json:"error"`
	}
	if err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "raunen", "version": "dev"},
	}, &res); err != nil {
		return err
	}
	if res.Error != nil {
		return res.Error
	}
	// initialize is followed by an optional initialized notification; we do not
	// wait for a reply to it, so just send it and move on.
	_ = c.t.notify(ctx, "notifications/initialized", map[string]any{})
	return nil
}

func (c *Client) listTools(ctx context.Context) error {
	var res struct {
		Error *rpcError `json:"error"`
		Tools []spec    `json:"tools"`
	}
	if err := c.call(ctx, "tools/list", map[string]any{}, &res); err != nil {
		return err
	}
	if res.Error != nil {
		return res.Error
	}
	for _, t := range res.Tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		name := t.Name
		desc := t.Description
		// Capture per-tool state in values, so each closure sees its own.
		c.tools = append(c.tools, tools.Tool{
			Name:        name,
			Description: "[" + c.name + "] " + desc,
			Params:      schema,
			// MCP tools can do anything, so they are treated as mutating and are
			// gated by plan/accept exactly like bash. A server that only reads is
			// still asked permission before it acts, which is the safe default.
			Mutates: true,
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				return c.callTool(ctx, name, args)
			},
		})
	}
	return nil
}

func (c *Client) callTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if len(strings.TrimSpace(string(args))) == 0 {
		args = json.RawMessage("{}")
	}
	// call already peeled the {"result": ...} envelope, so this decodes the
	// result body directly: {content: [...], isError: bool}.
	var res struct {
		Error   *rpcError `json:"error"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := c.call(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	}, &res); err != nil {
		return "", err
	}
	if res.Error != nil {
		return "", res.Error
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
		return out, fmt.Errorf("%s", out)
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

// stdioTransport speaks JSON-RPC 2.0 to a subprocess over its stdin/stdout, one
// JSON object per line. It owns the subprocess and the goroutine reading from it.
type stdioTransport struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner

	mu      sync.Mutex
	pending map[int]chan json.RawMessage
	nextID  int

	// err carries the first read-side failure, so a dead server surfaces as a
	// closed connection rather than a hang on the next call.
	err  error
	done chan struct{}
}

func newStdio(name string, s Server) (*stdioTransport, error) {
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Env = envFor(s.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// A server that talks to a service of its own may open a listening socket,
	// and we should not wait on it at exit.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp %q: %w", name, err)
	}
	t := &stdioTransport{
		name:    name,
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewScanner(stdout),
		pending: map[int]chan json.RawMessage{},
		done:    make(chan struct{}),
		// Tool-call arguments and results can be large; the default 64KB token
		// limit is not enough.
		nextID: 1,
	}
	// A single token can be very long; raise the scanner's line budget.
	t.stdout.Buffer(make([]byte, 0, 64<<10), 8<<20)
	go t.read()
	return t, nil
}

func (s *stdioTransport) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	ch := make(chan json.RawMessage, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	req := request{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	select {
	case <-s.done:
		return nil, fmt.Errorf("mcp %q is closed: %w", s.name, s.deadErr())
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.Lock()
	if _, err := s.stdin.Write(append(body, '\n')); err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("mcp %q: write: %w", s.name, err)
	}
	s.mu.Unlock()

	select {
	case raw := <-ch:
		// Responses are wrapped as {"result": {...}}; peel that off before
		// decoding into dst, or the fields never land.
		var env struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, err
		}
		return env.Result, nil
	case <-s.done:
		return nil, fmt.Errorf("mcp %q is closed: %w", s.name, s.deadErr())
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *stdioTransport) notify(_ context.Context, method string, params any) error {
	req := request{JSONRPC: "2.0", Method: method, Params: params}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.stdin.Write(append(body, '\n'))
	return err
}

func (s *stdioTransport) close() error {
	s.kill()
	return nil
}

// read pumps responses (and notifications) from the server until it exits or is
// killed. Each line is a JSON-RPC message; ones with an id are matched to a
// waiting call, the rest (notifications) are dropped.
func (s *stdioTransport) read() {
	defer close(s.done)
	for s.stdout.Scan() {
		line := strings.TrimSpace(s.stdout.Text())
		if line == "" {
			continue
		}
		var msg struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			// Not valid JSON: a protocol break. Stop rather than loop forever on
			// garbage.
			s.fail(fmt.Errorf("mcp %q: bad line: %s", s.name, line))
			return
		}
		var id int
		if err := json.Unmarshal(msg.ID, &id); err != nil || msg.ID == nil {
			// A notification, or a response with no id. Nothing to deliver.
			continue
		}
		s.mu.Lock()
		ch, ok := s.pending[id]
		if ok {
			delete(s.pending, id)
		}
		s.mu.Unlock()
		if ok {
			ch <- json.RawMessage(line)
		}
	}
	if err := s.stdout.Err(); err != nil {
		s.fail(fmt.Errorf("mcp %q: %w", s.name, err))
		return
	}
	s.fail(fmt.Errorf("server exited"))
}

func (s *stdioTransport) deadErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	return fmt.Errorf("connection lost")
}

func (s *stdioTransport) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

// kill stops the subprocess and closes the pipes. It is safe to call more than
// once and from close.
func (s *stdioTransport) kill() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	// Give it a moment to exit cleanly, then force it. A hung server should not
	// keep the raunen process from leaving.
	_ = s.stdin.Close()
	done := make(chan struct{})
	go func() {
		_ = s.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
	}
}

// envFor builds the subprocess environment. It starts from the parent process
// so the server inherits PATH and the like, then overlays any declared
// variables — so a server can be handed an API key without it leaving config.
func envFor(extra map[string]string) []string {
	env := append([]string{}, os.Environ()...)
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// httpTransport speaks MCP over Streamable HTTP. Each request is a POST of a
// JSON-RPC body; the server answers with application/json or a text/event-stream
// of SSE frames. The Mcp-Session-Id returned by initialize is replayed on every
// later request so the server keeps the same session. Requests are serialized so
// only one is in flight at a time — simpler than multiplexing, and the agent
// rarely calls two tools at once anyway.
type httpTransport struct {
	name    string
	url     string
	headers map[string]string
	client  *http.Client

	mu        sync.Mutex
	nextID    int
	sessionID string
}

func newHTTP(name string, s Server) (*httpTransport, error) {
	return &httpTransport{
		name:    name,
		url:     s.URL,
		headers: s.Headers,
		client:  &http.Client{Timeout: 30 * time.Second},
		nextID:  1,
	}, nil
}

func (h *httpTransport) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.nextID
	h.nextID++
	return h.do(ctx, method, id, params)
}

func (h *httpTransport) notify(ctx context.Context, method string, params any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	// A notification has no id; id 0 marshals to nothing because the field is
	// omitempty, so the server reads it as a notification and answers 202.
	_, err := h.do(ctx, method, 0, params)
	return err
}

// do sends one POST and returns the result body. The caller holds mu, so it is
// safe to read and write sessionID here.
func (h *httpTransport) do(ctx context.Context, method string, id int, params any) (json.RawMessage, error) {
	body, err := json.Marshal(request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if h.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", h.sessionID)
	}
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp %q: %w", h.name, err)
	}
	defer resp.Body.Close()
	// Capture the session id for subsequent requests. initialize is the usual
	// source, but a server may refresh it on any reply.
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		h.sessionID = sid
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mcp %q: http %d: %s", h.name, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("mcp %q: %w", h.name, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		// A notification reply (202 Accepted) carries no body; nothing to decode.
		return nil, nil
	}

	ctype, _, _ := strings.Cut(resp.Header.Get("Content-Type"), ";")
	ctype = strings.TrimSpace(ctype)
	var msg json.RawMessage
	if ctype == "text/event-stream" {
		msg, err = parseSSE(raw, id)
		if err != nil {
			return nil, err
		}
	} else {
		msg = raw
	}

	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		return nil, fmt.Errorf("mcp %q: bad response: %w", h.name, err)
	}
	if env.Error != nil {
		return nil, env.Error
	}
	return env.Result, nil
}

func (h *httpTransport) close() error { return nil }

// parseSSE reads an SSE stream and returns the JSON-RPC message whose id matches
// wantID. A server may interleave notifications with the result, so we look for
// the matching id rather than assuming the first frame is ours. If nothing
// matches, the last frame wins — better a stale reply than none.
func parseSSE(raw []byte, wantID int) (json.RawMessage, error) {
	var frames []json.RawMessage
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		frames = append(frames, json.RawMessage(data))
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("mcp: sse stream had no data frames")
	}
	for _, f := range frames {
		var id struct {
			ID int `json:"id"`
		}
		if json.Unmarshal(f, &id) == nil && id.ID == wantID {
			return f, nil
		}
	}
	return frames[len(frames)-1], nil
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }
