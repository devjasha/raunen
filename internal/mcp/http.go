package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

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

	// notifyCB, when set, receives every JSON-RPC notification the server sends
	// (a message with no id). Guarded by mu, but invoked outside mu since the
	// callback may call back into the transport.
	notifyCB func(method string, params json.RawMessage)
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

// OnNotification stores a callback for server notifications (messages with no
// id). For Streamable HTTP, notifications may arrive as SSE frames in a response
// that has no matching pending request id; do() delivers such frames here.
func (h *httpTransport) OnNotification(cb func(method string, params json.RawMessage)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifyCB = cb
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
		// A non-2xx may still carry a JSON-RPC error body. Decode it so a 400
		// with an error object reports the server's code and message rather than
		// a bare status line; fall back to the raw status + body otherwise.
		var env struct {
			Error *rpcError `json:"error"`
		}
		if json.Unmarshal(b, &env) == nil && env.Error != nil {
			return nil, fmt.Errorf("mcp %q: http %d: [%d] %s", h.name, resp.StatusCode, env.Error.Code, env.Error.Message)
		}
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
		// A streamable HTTP response may interleave notifications (no id) with the
		// result for our request id. Deliver any notification frames to the
		// callback and use the frame matching our id (or the last one) as the
		// result.
		msg, err = h.handleSSE(raw, id)
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

// handleSSE parses an SSE body, forwarding any id-less notification frames to
// notifyCB, and returns the frame matching wantID (falling back to the last
// frame) for use as the request's result. The caller holds mu.
func (h *httpTransport) handleSSE(raw []byte, wantID int) (json.RawMessage, error) {
	frames := splitSSE(raw)
	if len(frames) == 0 {
		return nil, fmt.Errorf("mcp %q: sse stream had no data frames", h.name)
	}
	var result json.RawMessage
	for _, f := range frames {
		var hdr struct {
			ID int `json:"id"`
		}
		if json.Unmarshal(f, &hdr) != nil || hdr.ID == 0 {
			// A notification (no id) — deliver it if a callback is set.
			if h.notifyCB != nil {
				var n struct {
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if err := json.Unmarshal(f, &n); err == nil && n.Method != "" {
					h.notifyCB(n.Method, n.Params)
				}
			}
			continue
		}
		if hdr.ID == wantID {
			result = f
		}
	}
	if result == nil {
		result = frames[len(frames)-1]
	}
	return result, nil
}

func (h *httpTransport) close() error { return nil }

// splitSSE reads an SSE byte stream and returns the JSON payloads carried in its
// "data:" frames, in order. Non-data lines (event:, id:, blank separators, and
// server pings) are ignored, as is the case on a long-lived stream where the
// server may prefix lines with ":" as a comment.
func splitSSE(raw []byte) []json.RawMessage {
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
	return frames
}

// parseSSE reads an SSE stream and returns the JSON-RPC message whose id matches
// wantID. A server may interleave notifications with the result, so we look for
// the matching id rather than assuming the first frame is ours. If nothing
// matches, the last frame wins — better a stale reply than none.
func parseSSE(raw []byte, wantID int) (json.RawMessage, error) {
	frames := splitSSE(raw)
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
