package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

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

	// notifyCB, when set, receives every JSON-RPC notification the server sends
	// (a message with no id). Guarded by mu.
	notifyCB func(method string, params json.RawMessage)
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

// OnNotification stores a callback for server notifications (messages with no
// id). It is called from the read goroutine; install it before starting traffic
// that may produce notifications.
func (s *stdioTransport) OnNotification(cb func(method string, params json.RawMessage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifyCB = cb
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
			// A notification, or a response with no id. Deliver it to the callback
			// if one is registered; otherwise there is nothing else to do.
			s.mu.Lock()
			cb := s.notifyCB
			s.mu.Unlock()
			if cb != nil {
				var n struct {
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if err := json.Unmarshal([]byte(line), &n); err == nil {
					cb(n.Method, n.Params)
				}
			}
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
