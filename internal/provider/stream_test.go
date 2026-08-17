package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, frames string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(frames))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "", "test-model")
}

func frame(delta string) string {
	return `data: {"choices":[{"index":0,"delta":` + delta + `,"finish_reason":null}]}` + "\n\n"
}

// A connection that drops mid-response must be reported. Without this it is
// indistinguishable from the model choosing to say nothing, which sends you
// looking at the model instead of at the server.
func TestStreamTruncatedIsAnError(t *testing.T) {
	c := serve(t, frame(`{"content":"partial answer"}`))

	msg, _, err := c.Stream(context.Background(), nil, nil, Handler{})
	if err == nil {
		t.Fatal("truncated stream returned no error")
	}
	if !strings.Contains(err.Error(), "closed the connection mid-response") {
		t.Errorf("error = %q, want it to name the dropped connection", err)
	}
	// Classified as the endpoint failing, not the model: everything behind it
	// is suspect, so a ladder should not just try the next model on the same
	// unreachable server.
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("error = %v, want it classified as ErrUnavailable", err)
	}
	_ = msg
}

func TestStreamCompletesOnDone(t *testing.T) {
	c := serve(t, frame(`{"content":"hello"}`)+"data: [DONE]\n\n")

	msg, _, err := c.Stream(context.Background(), nil, nil, Handler{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Content != "hello" {
		t.Errorf("content = %q, want %q", msg.Content, "hello")
	}
}

// finish_reason has to survive: "length" is how a cut-off generation is told
// apart from a deliberate stop, and with a reasoning model it is the difference
// between "said nothing" and "never got to the answer".
func TestStreamCapturesFinishReason(t *testing.T) {
	body := `data: {"choices":[{"index":0,"delta":{"content":""},"finish_reason":"length"}]}` + "\n\n"
	c := serve(t, body)

	msg, _, err := c.Stream(context.Background(), nil, nil, Handler{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Finish != "length" {
		t.Errorf("Finish = %q, want %q", msg.Finish, "length")
	}
}

// A tool that takes no arguments is often called with none at all. The call
// goes back with every later request, and a gateway translating to Anthropic's
// schema rejects an empty string where a tool_use input should be — poisoning
// the conversation, since the message it objects to is replayed on every retry.
func TestStreamGivesEmptyToolArgsAnObject(t *testing.T) {
	c := serve(t, frame(`{"tool_calls":[{"index":0,"id":"call_1","type":"function",`+
		`"function":{"name":"list"}}]}`)+"data: [DONE]\n\n")

	msg, _, err := c.Stream(context.Background(), nil, nil, Handler{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(msg.ToolCalls))
	}
	if got := msg.ToolCalls[0].Function.Arguments; got != "{}" {
		t.Errorf("arguments = %q, want %q", got, "{}")
	}
}

// Arguments the model did write are its own: dispatch hands malformed JSON back
// as a tool result so it can correct itself, and rewriting them here would run
// the tool with no arguments instead of reporting that anything went wrong.
func TestStreamKeepsToolArgsAsWritten(t *testing.T) {
	c := serve(t, frame(`{"tool_calls":[{"index":0,"id":"call_1","type":"function",`+
		`"function":{"name":"read","arguments":"{\"path\":"}}]}`)+"data: [DONE]\n\n")

	msg, _, err := c.Stream(context.Background(), nil, nil, Handler{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := msg.ToolCalls[0].Function.Arguments; got != `{"path":` {
		t.Errorf("arguments = %q, want the malformed JSON left intact", got)
	}
}

// A session saved before the above was fixed still holds the empty field, and
// would fail on every turn after being resumed rather than only once.
func TestNormalizeToolArgsRepairsSavedSessions(t *testing.T) {
	msgs := []Message{
		{Role: User, Content: "give me an overview"},
		{Role: Assistant, ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: Function{Name: "list"}},
			{ID: "call_2", Type: "function", Function: Function{Name: "read", Arguments: `{"path":"main.go"}`}},
		}},
	}
	NormalizeToolArgs(msgs)

	if got := msgs[1].ToolCalls[0].Function.Arguments; got != "{}" {
		t.Errorf("empty arguments = %q, want %q", got, "{}")
	}
	if got := msgs[1].ToolCalls[1].Function.Arguments; got != `{"path":"main.go"}` {
		t.Errorf("arguments = %q, want them untouched", got)
	}
}

// Reasoning must reach the handler but never the message: it is shown live and
// never sent back to the model.
func TestStreamSeparatesReasoningFromContent(t *testing.T) {
	c := serve(t, frame(`{"content":"","reasoning":"pondering"}`)+
		frame(`{"content":"answer"}`)+"data: [DONE]\n\n")

	var thought strings.Builder
	msg, _, err := c.Stream(context.Background(), nil, nil, Handler{
		Reasoning: func(s string) { thought.WriteString(s) },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if thought.String() != "pondering" {
		t.Errorf("reasoning = %q, want %q", thought.String(), "pondering")
	}
	if msg.Content != "answer" {
		t.Errorf("content = %q, want %q — reasoning must not leak in", msg.Content, "answer")
	}
}
