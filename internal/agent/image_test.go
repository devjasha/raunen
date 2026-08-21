package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"raunen/internal/provider"
	"raunen/internal/tools"
)

// capturing answers every request with "ok" and records the raw bodies, so a
// test can assert on what the endpoint was actually sent rather than on what
// the transcript says it was sent.
func capturing(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		frame, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{
				"delta":         map[string]any{"content": "ok"},
				"finish_reason": "stop",
			}},
		})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func runWith(t *testing.T, a *Agent, prompt string, imgs []provider.Image) {
	t.Helper()
	events := make(chan Event, 256)
	go a.RunWith(context.Background(), prompt, imgs, events)
	for ev := range events {
		if e, ok := ev.(Failed); ok {
			t.Fatalf("turn failed: %v", e.Err)
		}
	}
}

func png() provider.Image {
	return provider.Image{MIME: "image/png", Data: []byte("\x89PNG-data"), Name: "shot.png"}
}

func TestAttachedImageReachesTheEndpoint(t *testing.T) {
	srv, bodies := capturing(t)
	a := New(provider.New(srv.URL+"/v1", "", "m"), tools.Default(t.TempDir(), 4096), "")

	runWith(t, a, "what is this", []provider.Image{png()})

	msgs := (*bodies)[0]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	parts, ok := last["content"].([]any)
	if !ok {
		t.Fatalf("content = %#v, want an array of parts", last["content"])
	}
	var sawImage bool
	for _, p := range parts {
		if p.(map[string]any)["type"] == "image_url" {
			sawImage = true
		}
	}
	if !sawImage {
		t.Errorf("parts = %#v, want one carrying the image", parts)
	}
}

// The picture must stay on the message for the rest of the conversation. A
// later "what colour was the button" has to be answerable by looking again;
// dropping it leaves the model answering confidently from nothing.
func TestImageStaysInTheTranscript(t *testing.T) {
	srv, bodies := capturing(t)
	a := New(provider.New(srv.URL+"/v1", "", "m"), tools.Default(t.TempDir(), 4096), "")

	runWith(t, a, "what is this", []provider.Image{png()})
	runWith(t, a, "and its colour", nil)

	second := (*bodies)[1]["messages"].([]any)
	var withImage int
	for _, m := range second {
		if _, ok := m.(map[string]any)["content"].([]any); ok {
			withImage++
		}
	}
	if withImage != 1 {
		t.Errorf("%d messages carried parts on the second turn, want the first question to still have its image", withImage)
	}
}

// The model is shown the picture but not told what it was called; without the
// names in the prose, "compare the two mockups" cannot be answered by name.
func TestAttachmentsAreNamedInTheProse(t *testing.T) {
	srv, bodies := capturing(t)
	a := New(provider.New(srv.URL+"/v1", "", "m"), tools.Default(t.TempDir(), 4096), "")

	runWith(t, a, "compare these", []provider.Image{
		{MIME: "image/png", Data: []byte("a"), Name: "before.png"},
		{MIME: "image/png", Data: []byte("b"), Name: "after.png"},
	})

	msgs := (*bodies)[0]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	var text string
	for _, p := range last["content"].([]any) {
		if part := p.(map[string]any); part["type"] == "text" {
			text, _ = part["text"].(string)
		}
	}
	for _, want := range []string{"compare these", "before.png", "after.png"} {
		if !strings.Contains(text, want) {
			t.Errorf("text = %q, want it to mention %q", text, want)
		}
	}
}

// A turn with no attachments must go out exactly as it did before: several
// local runtimes reject the array form of content outright.
func TestTurnWithoutImagesSendsPlainText(t *testing.T) {
	srv, bodies := capturing(t)
	a := New(provider.New(srv.URL+"/v1", "", "m"), tools.Default(t.TempDir(), 4096), "")

	runWith(t, a, "hello", nil)

	msgs := (*bodies)[0]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if got, ok := last["content"].(string); !ok || got != "hello" {
		t.Errorf("content = %#v, want the plain string", last["content"])
	}
}

// An image costs real context. Counting it as free lets a few screenshots fill
// the window with nothing in the estimate to show for it, and the request is
// then rejected for a size the agent believed it had room for.
func TestImagesAreCountedAgainstTheContext(t *testing.T) {
	plain := []provider.Message{{Role: provider.User, Content: "hi"}}
	withImg := []provider.Message{{Role: provider.User, Content: "hi", Images: []provider.Image{png()}}}
	if estimateTokens(withImg) <= estimateTokens(plain)+100 {
		t.Errorf("an attachment added %d tokens to the estimate, want a real charge",
			estimateTokens(withImg)-estimateTokens(plain))
	}
}
