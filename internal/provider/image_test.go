package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// wire marshals a message the way a request would, which is the only place the
// array form of content is allowed to appear.
func wire(t *testing.T, m Message) []byte {
	t.Helper()
	w, err := toWire(m)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A message without images must keep the plain string form. Not every endpoint
// accepts the array form — several local runtimes reject it outright — so the
// array has to be reserved for the one case that needs it.
func TestMessageWithoutImagesStaysAString(t *testing.T) {
	b := wire(t, Message{Role: User, Content: "hello"})
	var got struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Content) != `"hello"` {
		t.Errorf("content = %s, want a plain string", got.Content)
	}
}

// An assistant message carrying only tool calls still has to serialize its
// empty content, or Ollama rejects the next request.
func TestEmptyContentIsStillSerialized(t *testing.T) {
	b := wire(t, Message{Role: Assistant, ToolCalls: []ToolCall{{ID: "1"}}})
	if !strings.Contains(string(b), `"content":""`) {
		t.Errorf("marshalled = %s, want an explicit empty content", b)
	}
}

func TestMessageWithImageUsesContentParts(t *testing.T) {
	m := Message{
		Role:    User,
		Content: "what is this",
		Images:  []Image{{MIME: "image/png", Data: []byte("\x89PNG"), Name: "shot.png"}},
	}
	b := wire(t, m)
	var got struct {
		Content []contentPart `json:"content"`
		Images  json.RawMessage
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("content should be an array of parts: %v (%s)", err, b)
	}
	if len(got.Content) != 2 {
		t.Fatalf("got %d parts, want text then image", len(got.Content))
	}
	// Text first: the instruction is what tells the model why it is looking.
	if got.Content[0].Type != "text" || got.Content[0].Text != "what is this" {
		t.Errorf("first part = %+v, want the prose", got.Content[0])
	}
	if got.Content[1].Type != "image_url" || got.Content[1].ImageURL == nil {
		t.Fatalf("second part = %+v, want an image", got.Content[1])
	}
	if want := "data:image/png;base64,iVBORw=="; got.Content[1].ImageURL.URL != want {
		t.Errorf("url = %q, want %q", got.Content[1].ImageURL.URL, want)
	}
	// The images field is transport-internal; sending it would have some
	// endpoints reject the request for an unknown key.
	if strings.Contains(string(b), `"images"`) {
		t.Errorf("marshalled = %s, want no images key on the wire", b)
	}
}

// A prompt that is only a picture is a real request, and the text part is left
// out rather than sent empty — some endpoints reject an empty text block.
func TestImageOnlyMessageOmitsTheTextPart(t *testing.T) {
	b := wire(t, Message{Role: User, Images: []Image{{MIME: "image/png", Data: []byte("x")}}})
	var got struct {
		Content []contentPart `json:"content"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Content) != 1 || got.Content[0].Type != "image_url" {
		t.Errorf("content = %+v, want only the image", got.Content)
	}
}

// Sessions are saved as JSON and read back by a later run. Both the plain form
// written before images existed and the array form have to survive the trip.
func TestMessageRoundTrips(t *testing.T) {
	for _, m := range []Message{
		{Role: User, Content: "plain"},
		{Role: Assistant, Content: "", ToolCalls: []ToolCall{{ID: "a", Type: "function"}}},
		{Role: ToolRole, Content: "result", ToolCallID: "a", Name: "read"},
		{Role: User, Content: "look", Images: []Image{{MIME: "image/gif", Data: []byte("GIF89a")}}},
	} {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var got Message
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if got.Content != m.Content || got.Role != m.Role || len(got.Images) != len(m.Images) {
			t.Errorf("round trip of %s gave %+v", b, got)
		}
		for i := range m.Images {
			if string(got.Images[i].Data) != string(m.Images[i].Data) {
				t.Errorf("image %d survived as %q", i, got.Images[i].Data)
			}
			if got.Images[i].MIME != m.Images[i].MIME {
				t.Errorf("mime = %q, want %q", got.Images[i].MIME, m.Images[i].MIME)
			}
		}
	}
}

// A message read from an endpoint or an older session may hold parts with no
// image in them at all; the text must still come through.
func TestUnmarshalFlattensTextParts(t *testing.T) {
	var m Message
	raw := `{"role":"user","content":[{"type":"text","text":"one"},{"type":"text","text":"two"}]}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content != "one\ntwo" {
		t.Errorf("content = %q, want both parts", m.Content)
	}
}

// A session is written as Message and read back by a later run, so the name the
// user attached has to survive. It does not travel to the endpoint — an image
// block has nowhere to put it — which is exactly why the wire form is built in
// toWire rather than in a marshaller on the type: saving through the wire form
// dropped the name.
func TestSavedImageKeepsItsName(t *testing.T) {
	b, err := json.Marshal(Message{
		Role:    User,
		Content: "what is this",
		Images:  []Image{{MIME: "image/png", Data: []byte("\x89PNG"), Name: "shot.png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got Message
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Images) != 1 || got.Images[0].Name != "shot.png" {
		t.Errorf("reloaded %+v, want the name kept", got.Images)
	}
	// The prose stays prose in a saved session, so a title or a summary reads
	// it without having to understand content parts.
	if !strings.Contains(string(b), `"content":"what is this"`) {
		t.Errorf("saved as %s, want plain content", b)
	}
}
