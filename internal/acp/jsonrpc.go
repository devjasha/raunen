// Package acp serves the Agent Client Protocol over stdio, so an editor can
// drive raunen as its coding agent.
//
// ACP is JSON-RPC 2.0 with newline-delimited messages. The client (the editor)
// calls the agent to start sessions and send prompts; the agent calls back to
// ask permission and to stream what it is doing. Both directions travel on the
// same pipe, which is why this file has a request router and a pending-call
// table rather than just a handler loop.
//
// Nothing about the agent changes here. This package is a transport: it turns
// method calls into calls on agent.Agent and turns agent.Event into the
// session/update notifications ACP defines. The tools, the permission rules,
// the skills and the sessions are the ones the terminal uses.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// JSON-RPC error codes. The first five are the standard set; the rest are the
// ones ACP reserves out of the implementation-defined range.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
	codeAuthRequired   = -32000
)

// message is one JSON-RPC frame. Requests, responses and notifications share a
// shape on the wire, and which one it is depends on which fields are present:
// an id with a method is a request, an id without is a response, a method
// without an id is a notification.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the error object of a failed call.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (%d)", e.Message, e.Code) }

// errorf builds an error response body.
func errorf(code int, format string, args ...any) *rpcError {
	return &rpcError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// handler answers one method. Returning a nil result with a nil error is a
// valid empty response, which several ACP methods use.
type handler func(ctx context.Context, params json.RawMessage) (any, error)

// conn is a JSON-RPC connection over a reader and a writer.
//
// It is bidirectional: an editor calls us to run a prompt, and while that
// prompt is running we call the editor back to ask whether a tool may write to
// a file. Both use the same pipe, so incoming frames are demultiplexed by
// whether they carry a method (a call for us to answer) or only an id (an
// answer to a call we made).
type conn struct {
	in  *bufio.Scanner
	out io.Writer

	// writing serialises writes. Notifications are emitted from the goroutine
	// running a turn while responses come from the read loop, and two frames
	// interleaved on one line would be unparseable.
	writing sync.Mutex

	// handlers are the methods this side answers.
	handlers map[string]handler

	// pending maps the id of a call we made to the channel waiting for its
	// answer. Guarded because a turn goroutine registers while the read loop
	// resolves.
	pendingMu sync.Mutex
	pending   map[string]chan message

	// seq numbers outgoing calls.
	seq atomic.Int64
}

func newConn(r io.Reader, w io.Writer) *conn {
	sc := bufio.NewScanner(r)
	// An editor can embed a whole file in a prompt, so the default 64 KB line
	// limit is far too small. A megabyte is generous without being unbounded.
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	return &conn{
		in:       sc,
		out:      w,
		handlers: map[string]handler{},
		pending:  map[string]chan message{},
	}
}

// handle registers a method.
func (c *conn) handle(method string, h handler) { c.handlers[method] = h }

// write emits one frame, followed by a newline.
func (c *conn) write(m message) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.writing.Lock()
	defer c.writing.Unlock()
	if _, err := c.out.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// notify sends a notification, which by definition expects no answer.
func (c *conn) notify(method string, params any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(message{Method: method, Params: b})
}

// call invokes a method on the other side and waits for the answer.
//
// This is what makes permission prompts work: the agent blocks inside a tool
// dispatch while the editor decides, and the read loop delivers the answer to
// the channel this registered.
func (c *conn) call(ctx context.Context, method string, params any, result any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("%d", c.seq.Add(1))
	raw, _ := json.Marshal(id)

	reply := make(chan message, 1)
	c.pendingMu.Lock()
	c.pending[id] = reply
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	if err := c.write(message{ID: raw, Method: method, Params: b}); err != nil {
		return err
	}

	select {
	case m := <-reply:
		if m.Error != nil {
			return m.Error
		}
		if result != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// serve reads frames until the input ends, dispatching each one.
//
// Requests are answered on their own goroutine. A prompt can run for minutes
// and calls back to the editor while it does; answering it inline would block
// the read loop, and the permission answer it is waiting for would never be
// read. That is a deadlock, and it is the reason this is not a simple loop.
func (c *conn) serve(ctx context.Context) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	for c.in.Scan() {
		line := c.in.Bytes()
		if len(line) == 0 {
			continue
		}
		// Copied because the scanner reuses its buffer and the frame outlives
		// this iteration once it goes to a goroutine.
		buf := make([]byte, len(line))
		copy(buf, line)

		var m message
		if err := json.Unmarshal(buf, &m); err != nil {
			_ = c.write(message{Error: errorf(codeParseError, "invalid JSON: %v", err)})
			continue
		}

		switch {
		case m.Method == "" && len(m.ID) > 0:
			// An answer to something we asked.
			c.resolve(m)
		case len(m.ID) == 0:
			// A notification: run it and send nothing back, whatever happens.
			if h, ok := c.handlers[m.Method]; ok {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, _ = h(ctx, m.Params)
				}()
			}
		default:
			wg.Add(1)
			go func() {
				defer wg.Done()
				c.dispatch(ctx, m)
			}()
		}
	}
	return c.in.Err()
}

// resolve hands a response to whoever is waiting for it.
func (c *conn) resolve(m message) {
	var id string
	// An id is a string or a number on the wire; both are keyed as their
	// decoded string form so a client that numbers its replies still matches.
	if err := json.Unmarshal(m.ID, &id); err != nil {
		var n json.Number
		if err := json.Unmarshal(m.ID, &n); err != nil {
			return
		}
		id = n.String()
	}
	c.pendingMu.Lock()
	ch, ok := c.pending[id]
	c.pendingMu.Unlock()
	if ok {
		ch <- m
	}
}

// dispatch answers one request.
func (c *conn) dispatch(ctx context.Context, m message) {
	h, ok := c.handlers[m.Method]
	if !ok {
		_ = c.write(message{ID: m.ID, Error: errorf(codeMethodNotFound, "no such method: %s", m.Method)})
		return
	}

	result, err := h(ctx, m.Params)
	if err != nil {
		var re *rpcError
		if e, ok := err.(*rpcError); ok {
			re = e
		} else {
			re = errorf(codeInternalError, "%s", err.Error())
		}
		_ = c.write(message{ID: m.ID, Error: re})
		return
	}

	// A method with nothing to report still owes an object: ACP's result type
	// is a struct everywhere, and a bare null trips strict clients.
	if result == nil {
		result = struct{}{}
	}
	b, err := json.Marshal(result)
	if err != nil {
		_ = c.write(message{ID: m.ID, Error: errorf(codeInternalError, "encoding result: %v", err)})
		return
	}
	_ = c.write(message{ID: m.ID, Result: b})
}
