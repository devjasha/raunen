package agent

import (
	"strings"
	"testing"

	"raunen/internal/provider"
)

// A trimmed conversation must still be a valid request: every tool result has
// to be preceded by the assistant message that called for it, or the API
// rejects the whole thing.
func TestTrimKeepsToolGroupsIntact(t *testing.T) {
	a := &Agent{contextTokens: 512}
	a.messages = []provider.Message{{Role: provider.System, Content: "sys"}}

	// Several exchanges, each an assistant tool call answered by two results.
	for i := 0; i < 8; i++ {
		a.messages = append(a.messages,
			provider.Message{Role: provider.User, Content: strings.Repeat("q", 400)},
			provider.Message{Role: provider.Assistant, ToolCalls: []provider.ToolCall{
				{ID: "a", Function: provider.Function{Name: "read", Arguments: "{}"}},
				{ID: "b", Function: provider.Function{Name: "list", Arguments: "{}"}},
			}},
			provider.Message{Role: provider.ToolRole, ToolCallID: "a", Content: strings.Repeat("r", 400)},
			provider.Message{Role: provider.ToolRole, ToolCallID: "b", Content: strings.Repeat("r", 400)},
		)
	}

	out := make(chan Event, 16)
	a.trim(out)
	close(out)

	if got := estimateTokens(a.messages); got > 512 {
		t.Errorf("after trim, estimate = %d tokens, want <= 512", got)
	}
	if a.messages[0].Role != provider.System {
		t.Fatal("system prompt was dropped; it must always survive")
	}
	// No tool result may lead the conversation after the system prompt.
	if len(a.messages) > 1 && a.messages[1].Role == provider.ToolRole {
		t.Error("conversation starts with an orphaned tool result")
	}
	var trimmed bool
	for ev := range out {
		if _, ok := ev.(Trimmed); ok {
			trimmed = true
		}
	}
	if !trimmed {
		t.Error("no Trimmed event emitted")
	}
}

func TestTrimIsANoOpWhenItFits(t *testing.T) {
	a := &Agent{contextTokens: 8192}
	a.messages = []provider.Message{
		{Role: provider.System, Content: "sys"},
		{Role: provider.User, Content: "hi"},
	}
	before := len(a.messages)

	out := make(chan Event, 4)
	a.trim(out)
	close(out)

	if len(a.messages) != before {
		t.Errorf("messages = %d, want %d unchanged", len(a.messages), before)
	}
	for ev := range out {
		if _, ok := ev.(Trimmed); ok {
			t.Error("emitted Trimmed when nothing needed trimming")
		}
	}
}

// An unknown window must not trigger trimming, or a hosted model with a large
// context would have its history silently thrown away.
func TestTrimDisabledWithoutAContext(t *testing.T) {
	a := &Agent{contextTokens: 0}
	for i := 0; i < 50; i++ {
		a.messages = append(a.messages,
			provider.Message{Role: provider.User, Content: strings.Repeat("x", 1000)})
	}
	before := len(a.messages)

	out := make(chan Event, 4)
	a.trim(out)
	close(out)

	if len(a.messages) != before {
		t.Errorf("messages = %d, want %d unchanged", len(a.messages), before)
	}
}

// Within a single exchange the question must survive while the oldest tool
// results are dropped: that is the case that produced an empty reply, because
// the context filled up before the model ever got to answer.
func TestTrimDropsOldToolGroupsWithinATurn(t *testing.T) {
	a := &Agent{contextTokens: 1024}
	a.messages = []provider.Message{
		{Role: provider.System, Content: "sys"},
		{Role: provider.User, Content: "introduce this project"},
	}
	for i := 0; i < 6; i++ {
		a.messages = append(a.messages,
			provider.Message{Role: provider.Assistant, ToolCalls: []provider.ToolCall{
				{ID: "c", Function: provider.Function{Name: "read", Arguments: "{}"}},
			}},
			provider.Message{Role: provider.ToolRole, ToolCallID: "c", Content: strings.Repeat("x", 500)},
		)
	}

	out := make(chan Event, 32)
	a.trim(out)
	close(out)

	if a.messages[0].Role != provider.System {
		t.Fatal("system prompt dropped")
	}
	if lastUser(a.messages) == 0 {
		t.Fatal("the question being answered was dropped")
	}
	if a.messages[lastUser(a.messages)].Content != "introduce this project" {
		t.Error("the surviving user message is not the question being answered")
	}
	// The newest tool result is what the model is about to reason about.
	if last := a.messages[len(a.messages)-1]; last.Role != provider.ToolRole {
		t.Errorf("last message role = %q, want the newest tool result", last.Role)
	}
	if got := estimateTokens(a.messages); got > 1024 {
		t.Errorf("estimate = %d tokens, want <= 1024", got)
	}
}
