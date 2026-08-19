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
)

// sseTransport speaks the LEGACY MCP SSE transport. It is distinct from the
// Streamable HTTP transport in http.go: rather than multiplexing requests and
// responses over a single POST, the legacy transport opens one long-lived GET
// SSE stream for all server→client traffic (responses and notifications) and
// sends each client→server call as its own POST. The POST is answered directly
// (body carries the result, or 202 with an empty body when the result is routed
// back over the GET stream).
//
// The GET stream is opened to s.URL with Accept: text/event-stream; the POST
// goes to the same s.URL. The Mcp-Session-Id returned on either side is replayed
// on both for the rest of the session. This implementation is deliberately
// simple: it does not follow the "endpoint" event handshake of the earliest SSE
// spec, assuming the configured URL already accepts both GET and POST.
type sseTransport struct {
	name    string
	url     string
	headers map[string]string
	client  *http.Client

	mu        sync.Mutex
	nextID    int
	sessionID string
	// notifyCB, when set, receives every JSON-RPC notification the server sends
	// over the GET stream (a message with no id). Guarded by mu.
	notifyCB func(method string, params json.RawMessage)
	// pending maps an in-flight request id to the channel the GET stream delivers
	// its response on (used when the POST returns 202 with no body).
	pending map[int]chan json.RawMessage

	// done is closed when the GET stream ends or the transport is closed, so a
	// request waiting on a dead stream surfaces a clean error instead of hanging.
	done    chan struct{}
	cancel  context.CancelFunc
	getBody io.Closer
}

// newSSE builds a legacy SSE transport. The long-lived GET stream is opened in
// the background, so construction does not require a live server: connection
// failures are reported through the stream loop, not returned here. Close stops
// the stream and the client.
func newSSE(name string, s Server) (*sseTransport, error) {
	ctx, cancel := context.WithCancel(context.Background())
	t := &sseTransport{
		name:    name,
		url:     s.URL,
		headers: s.Headers,
		// No Timeout: the GET stream is meant to stay open for the session's life.
		client:  &http.Client{},
		pending: map[int]chan json.RawMessage{},
		done:    make(chan struct{}),
		cancel:  cancel,
		nextID:  1,
	}
	go t.stream(ctx)
	return t, nil
}

func (s *sseTransport) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
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

	select {
	case <-s.done:
		return nil, fmt.Errorf("mcp %q: sse stream closed", s.name)
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// The POST carries the call; the result normally comes back in the POST body,
	// but a 202 with an empty body means the server routes it over the GET stream.
	raw, err := s.post(ctx, method, id, params)
	if err != nil {
		return nil, err
	}
	if raw != nil {
		return raw, nil
	}
	select {
	case m := <-ch:
		var env struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(m, &env); err != nil {
			return nil, err
		}
		return env.Result, nil
	case <-s.done:
		return nil, fmt.Errorf("mcp %q: sse stream closed", s.name)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *sseTransport) notify(ctx context.Context, method string, params any) error {
	// A notification has no id; id 0 marshals to nothing (omitempty), so the
	// server reads it as a notification and answers 202.
	_, err := s.post(ctx, method, 0, params)
	return err
}

// post sends one JSON-RPC POST and returns the peeled result body. It mirrors
// httpTransport.do: same headers, session-id replay, and error decoding. The
// caller does not need to hold mu; post guards the session id itself.
func (s *sseTransport) post(ctx context.Context, method string, id int, params any) (json.RawMessage, error) {
	body, err := json.Marshal(request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	s.mu.Lock()
	if s.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", s.sessionID)
	}
	s.mu.Unlock()
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp %q: %w", s.name, err)
	}
	defer resp.Body.Close()
	s.mu.Lock()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		s.sessionID = sid
	}
	s.mu.Unlock()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		var env struct {
			Error *rpcError `json:"error"`
		}
		if json.Unmarshal(b, &env) == nil && env.Error != nil {
			return nil, fmt.Errorf("mcp %q: http %d: [%d] %s", s.name, resp.StatusCode, env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("mcp %q: http %d: %s", s.name, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("mcp %q: %w", s.name, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		// A 202 with no body: the result, if any, arrives on the GET stream.
		return nil, nil
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("mcp %q: bad response: %w", s.name, err)
	}
	if env.Error != nil {
		return nil, env.Error
	}
	return env.Result, nil
}

// stream opens the long-lived GET SSE connection and pumps frames off it until
// the context is cancelled, the connection drops, or close() is called. Each
// data frame is a JSON-RPC message; ones with an id are matched to a waiting
// request, the rest (notifications) are delivered to notifyCB.
func (s *sseTransport) stream(getCtx context.Context) {
	defer close(s.done)
	req, err := http.NewRequestWithContext(getCtx, http.MethodGet, s.url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	s.mu.Lock()
	if s.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", s.sessionID)
	}
	s.mu.Unlock()
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.getBody = resp.Body
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		s.sessionID = sid
	}
	s.mu.Unlock()
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	// SSE events are one or more "data:" lines up to a blank separator. Accumulate
	// them into one JSON payload, then dispatch the completed frame.
	var buf bytes.Buffer
	for sc.Scan() {
		trimmed := strings.TrimSpace(sc.Text())
		if trimmed == "" {
			if buf.Len() > 0 {
				s.dispatch(json.RawMessage(strings.TrimSpace(buf.String())))
				buf.Reset()
			}
			continue
		}
		if strings.HasPrefix(trimmed, ":") {
			continue // comment / server ping
		}
		if strings.HasPrefix(trimmed, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if data == "" {
				continue
			}
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(data)
		}
	}
	if buf.Len() > 0 {
		s.dispatch(json.RawMessage(strings.TrimSpace(buf.String())))
	}
}

// dispatch routes one JSON-RPC frame from the GET stream: id-less messages are
// notifications for notifyCB, and id-bearing ones go to the waiting request.
func (s *sseTransport) dispatch(raw json.RawMessage) {
	var hdr struct {
		ID     int            `json:"id"`
		Method string         `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &hdr); err != nil {
		return
	}
	if hdr.ID == 0 {
		s.mu.Lock()
		cb := s.notifyCB
		s.mu.Unlock()
		if cb != nil && hdr.Method != "" {
			cb(hdr.Method, hdr.Params)
		}
		return
	}
	s.mu.Lock()
	ch, ok := s.pending[hdr.ID]
	if ok {
		delete(s.pending, hdr.ID)
	}
	s.mu.Unlock()
	if ok {
		ch <- raw
	}
}

// OnNotification stores a callback for server notifications delivered over the
// GET stream. It is called from the stream goroutine; install it before starting
// traffic that may produce notifications.
func (s *sseTransport) OnNotification(cb func(method string, params json.RawMessage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifyCB = cb
}

func (s *sseTransport) close() error {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	if s.getBody != nil {
		_ = s.getBody.Close()
	}
	s.mu.Unlock()
	return nil
}
