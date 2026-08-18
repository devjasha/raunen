package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"raunen/internal/provider"
)

// summariser stands in for a model that is asked to summarise. It records the
// request it was given, since half of what compaction has to get right is what
// it sends rather than what it does with the answer.
type summariser struct {
	*httptest.Server
	reply    string
	status   int
	requests []provider.Message
	tools    int
}

func newSummariser(t *testing.T, reply string) *summariser {
	t.Helper()
	s := &summariser{reply: reply}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []provider.Message `json:"messages"`
			Tools    []json.RawMessage  `json:"tools"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("unparseable request: %v", err)
		}
		s.requests = req.Messages
		s.tools = len(req.Tools)

		if s.status != 0 {
			w.WriteHeader(s.status)
			fmt.Fprint(w, `{"error":{"message":"no"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		frame, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{
				"delta":         map[string]any{"content": s.reply},
				"finish_reason": "stop",
			}},
		})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
	}))
	t.Cleanup(s.Close)
	return s
}

// conversation builds n exchanges, each a question, a tool call and its result,
// with content long enough that trimming and compaction have something to bite
// on.
func conversation(n int) []provider.Message {
	msgs := []provider.Message{{Role: provider.System, Content: "sys"}}
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			provider.Message{Role: provider.User, Content: fmt.Sprintf("question %d %s", i, strings.Repeat("q", 400))},
			provider.Message{Role: provider.Assistant, ToolCalls: []provider.ToolCall{
				{ID: fmt.Sprintf("c%d", i), Function: provider.Function{Name: "read", Arguments: `{"path":"main.go"}`}},
			}},
			provider.Message{Role: provider.ToolRole, ToolCallID: fmt.Sprintf("c%d", i),
				Content: fmt.Sprintf("result %d %s", i, strings.Repeat("r", 400))},
		)
	}
	return msgs
}

func compactAgent(t *testing.T, s *summariser, window int) *Agent {
	t.Helper()
	a := New(provider.New(s.URL+"/v1", "", "m"), nil, "")
	a.contextTokens = window
	return a
}

// TestCompactReplacesTheOldAndKeepsTheNew is the shape of the whole feature:
// what is old becomes a summary, what is recent stays exactly as it was.
func TestCompactReplacesTheOldAndKeepsTheNew(t *testing.T) {
	s := newSummariser(t, "Goal — fix the parser.\nFound — main.go:42 has the bug.")
	a := compactAgent(t, s, 4096)
	a.messages = conversation(8)
	last := a.messages[len(a.messages)-1]

	got, err := a.Compact(context.Background(), "")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if a.messages[0].Role != provider.System {
		t.Error("the system prompt did not survive compaction")
	}
	if a.messages[1].Role != provider.User || !strings.Contains(a.messages[1].Content, "main.go:42") {
		t.Errorf("the summary is not the second message: %+v", a.messages[1])
	}
	if a.messages[len(a.messages)-1].Content != last.Content {
		t.Error("the newest message was not kept verbatim")
	}
	if got.Replaced < minCompact {
		t.Errorf("replaced %d messages, want at least %d", got.Replaced, minCompact)
	}
	if got.After >= got.Before {
		t.Errorf("compaction did not shrink anything: %d → %d", got.Before, got.After)
	}
	if got.Kept != len(a.messages)-2 {
		t.Errorf("Kept = %d, but %d messages follow the summary", got.Kept, len(a.messages)-2)
	}
}

// TestCompactLeavesAValidConversation guards the invariant that breaks a
// request outright: a tool result whose call is no longer there.
func TestCompactLeavesAValidConversation(t *testing.T) {
	s := newSummariser(t, "a summary")
	a := compactAgent(t, s, 4096)
	a.messages = conversation(10)

	if _, err := a.Compact(context.Background(), ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	calls := map[string]bool{}
	for i, m := range a.messages {
		for _, tc := range m.ToolCalls {
			calls[tc.ID] = true
		}
		if m.Role == provider.ToolRole && !calls[m.ToolCallID] {
			t.Fatalf("message %d is a result for call %q that is no longer in the conversation",
				i, m.ToolCallID)
		}
	}
}

// TestCompactSendsNoToolsAndNoRawCalls covers why the material is flattened
// into text: a request carrying tool calls without the schemas declaring them
// is rejected by some endpoints, and a summariser offered tools eventually
// answers with one instead of with prose.
func TestCompactSendsNoToolsAndNoRawCalls(t *testing.T) {
	s := newSummariser(t, "a summary")
	a := compactAgent(t, s, 4096)
	a.messages = conversation(8)

	if _, err := a.Compact(context.Background(), ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if s.tools != 0 {
		t.Errorf("the summarising request offered %d tools, want none", s.tools)
	}
	for i, m := range s.requests {
		if len(m.ToolCalls) > 0 || m.Role == provider.ToolRole {
			t.Errorf("request message %d carries raw tool traffic: %+v", i, m)
		}
	}
	if len(s.requests) != 2 || s.requests[0].Role != provider.System {
		t.Fatalf("want a system prompt and one user message, got %d", len(s.requests))
	}
	// The material has to actually be in there, or the summary is invented.
	if !strings.Contains(s.requests[1].Content, "question 0") {
		t.Error("the conversation being summarised was not in the request")
	}
	if !strings.Contains(s.requests[1].Content, "read") {
		t.Error("the tools that ran were not described to the summariser")
	}
}

// TestCompactPassesTheUsersFocus covers the argument to /compact: the user
// saying what matters is the one thing they can do to make a summary useful.
func TestCompactPassesTheUsersFocus(t *testing.T) {
	s := newSummariser(t, "a summary")
	a := compactAgent(t, s, 4096)
	a.messages = conversation(8)

	if _, err := a.Compact(context.Background(), "the migration plan"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !strings.Contains(s.requests[1].Content, "the migration plan") {
		t.Error("the user's focus never reached the summariser")
	}
}

// TestCompactRefusesAShortConversation checks the normal outcome that is not a
// failure: there is nothing yet worth spending a model call on.
func TestCompactRefusesAShortConversation(t *testing.T) {
	s := newSummariser(t, "a summary")
	a := compactAgent(t, s, 4096)
	a.messages = []provider.Message{
		{Role: provider.System, Content: "sys"},
		{Role: provider.User, Content: "hello"},
	}
	before := len(a.messages)

	if _, err := a.Compact(context.Background(), ""); !errors.Is(err, ErrNothingToCompact) {
		t.Errorf("err = %v, want ErrNothingToCompact", err)
	}
	if len(a.messages) != before {
		t.Error("a refused compaction still changed the conversation")
	}
}

// TestCompactLeavesTheConversationAloneOnFailure is what makes compaction safe
// to attempt automatically: a failed one costs a model call and nothing else.
func TestCompactLeavesTheConversationAloneOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reply  string
		status int
	}{
		{name: "the endpoint refuses", status: http.StatusInternalServerError},
		{name: "the model says nothing", reply: "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSummariser(t, tc.reply)
			s.status = tc.status
			a := compactAgent(t, s, 4096)
			a.messages = conversation(8)
			before := append([]provider.Message(nil), a.messages...)

			if _, err := a.Compact(context.Background(), ""); err == nil {
				t.Fatal("Compact reported success")
			}
			if len(a.messages) != len(before) {
				t.Fatalf("conversation went from %d messages to %d", len(before), len(a.messages))
			}
			for i := range before {
				if a.messages[i].Content != before[i].Content {
					t.Errorf("message %d changed after a failed compaction", i)
				}
			}
		})
	}
}

// TestReduceSummarisesBeforeItThrowsAway is the reason compaction exists at
// all: trimming loses what the model already worked out, so it must be the
// second thing tried rather than the first.
func TestReduceSummarisesBeforeItThrowsAway(t *testing.T) {
	s := newSummariser(t, "Found — main.go:42 has the bug.")
	a := compactAgent(t, s, 2048)
	a.messages = conversation(12)

	out := make(chan Event, 32)
	a.reduce(context.Background(), out)
	close(out)

	var compacted bool
	for ev := range out {
		if _, ok := ev.(Compacted); ok {
			compacted = true
		}
	}
	if !compacted {
		t.Fatal("reduce trimmed without trying to compact first")
	}
	if !strings.Contains(a.messages[1].Content, "main.go:42") {
		t.Error("what the summary established did not survive into the conversation")
	}
	// Whatever the summary could not save, trimming still has to, so the
	// request actually fits.
	if estimateTokens(a.messages) > a.trimBudget() {
		t.Errorf("after reduce the request is %d tokens, over the %d budget",
			estimateTokens(a.messages), a.trimBudget())
	}
}

// TestReduceStillTrimsWhenSummarisingFails covers the fallback. A dead
// summariser must not stop the turn — trimming is worse, but it is not nothing.
func TestReduceStillTrimsWhenSummarisingFails(t *testing.T) {
	s := newSummariser(t, "")
	s.status = http.StatusInternalServerError
	a := compactAgent(t, s, 2048)
	a.messages = conversation(12)

	out := make(chan Event, 32)
	a.reduce(context.Background(), out)
	close(out)

	var failed, trimmed bool
	for ev := range out {
		switch ev.(type) {
		case CompactFailed:
			failed = true
		case Trimmed:
			trimmed = true
		}
	}
	if !failed {
		t.Error("a failed compaction was not reported")
	}
	if !trimmed {
		t.Error("nothing was trimmed after compaction failed, so the request still does not fit")
	}
	if estimateTokens(a.messages) > a.trimBudget() {
		t.Error("the request still does not fit the window")
	}
}

// TestSubAgentsAreNotCompacted guards a model call nobody asked for: a
// sub-agent's whole context is discarded a few steps later anyway.
func TestSubAgentsAreNotCompacted(t *testing.T) {
	s := newSummariser(t, "a summary")
	a := compactAgent(t, s, 2048)
	a.depth = 1
	a.messages = conversation(12)

	if a.shouldCompact() {
		t.Error("a sub-agent would have paid for a summary of a context it is about to throw away")
	}
}

// TestTranscriptClipsHugeToolResults covers the case that would otherwise make
// compaction impossible: one file read larger than the window being summarised.
func TestTranscriptClipsHugeToolResults(t *testing.T) {
	a := &Agent{contextTokens: 4096}
	huge := strings.Repeat("x", 200_000)
	text := a.transcriptOf([]provider.Message{
		{Role: provider.User, Content: "read it"},
		{Role: provider.ToolRole, Content: huge},
	})

	if strings.Contains(text, huge) {
		t.Error("a 200k tool result went to the summariser whole")
	}
	if !strings.Contains(text, "read it") {
		t.Error("the question was dropped in favour of the file it asked about")
	}
	if got := estimateTokens([]provider.Message{{Content: text}}); got > a.summaryBudget() {
		t.Errorf("the material is %d tokens, over the %d budget", got, a.summaryBudget())
	}
}

// TestCompactRefusesASummaryBiggerThanItsSource covers what a live run against
// a small model actually did: four short messages summarised to more tokens
// than they contained. Applying that grows the next request rather than
// shrinking it, which is the opposite of the point.
func TestCompactRefusesASummaryBiggerThanItsSource(t *testing.T) {
	s := newSummariser(t, strings.Repeat("a very thorough summary. ", 200))
	a := compactAgent(t, s, 4096)
	// Short exchanges: there is little here to say, so anything said at length
	// about it costs more than it saves.
	a.messages = []provider.Message{{Role: provider.System, Content: "sys"}}
	for i := 0; i < 4; i++ {
		a.messages = append(a.messages,
			provider.Message{Role: provider.User, Content: fmt.Sprintf("q%d", i)},
			provider.Message{Role: provider.Assistant, Content: fmt.Sprintf("a%d", i)})
	}
	before := append([]provider.Message(nil), a.messages...)

	_, err := a.Compact(context.Background(), "")
	if !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("err = %v, want ErrNothingToCompact", err)
	}
	if len(a.messages) != len(before) {
		t.Fatalf("the conversation changed anyway: %d messages, was %d",
			len(a.messages), len(before))
	}
	for i := range before {
		if a.messages[i].Content != before[i].Content {
			t.Errorf("message %d was rewritten by a compaction that was refused", i)
		}
	}
}

// TestManualCompactKeepsATighterTailThanAutomatic covers the difference a live
// run made obvious: the loop only has to claw back enough room to send one
// request, but someone typing /compact is asking for room on purpose and a
// compaction that hands back a tenth of the window is not worth the wait.
func TestManualCompactKeepsATighterTailThanAutomatic(t *testing.T) {
	a := &Agent{contextTokens: 32768}
	a.messages = conversation(12)

	auto, manual := a.compactPoint(true), a.compactPoint(false)
	if manual <= auto {
		t.Fatalf("manual cuts at %d and automatic at %d — a hand-typed /compact "+
			"should reach further into the conversation", manual, auto)
	}
	// Both still keep the last exchange whole, whatever it costs.
	if last := lastUser(a.messages); manual > last {
		t.Errorf("manual cut at %d drops into the current exchange, which starts at %d",
			manual, last)
	}
}

// TestCompactPointAlwaysLandsOnAUserMessage is the invariant behind both cuts:
// anywhere else orphans a tool result.
func TestCompactPointAlwaysLandsOnAUserMessage(t *testing.T) {
	for _, n := range []int{2, 5, 12, 40} {
		for _, auto := range []bool{true, false} {
			a := &Agent{contextTokens: 8192}
			a.messages = conversation(n)
			cut := a.compactPoint(auto)
			if cut >= len(a.messages) {
				continue
			}
			if a.messages[cut].Role != provider.User {
				t.Errorf("%d exchanges, auto=%v: cut at %d is a %s, want a user message",
					n, auto, cut, a.messages[cut].Role)
			}
		}
	}
}
