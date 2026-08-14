package provider

import (
	"context"
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
	if !strings.Contains(err.Error(), "closed mid-response") {
		t.Errorf("error = %q, want it to name the dropped connection", err)
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
