package agent

import (
	"strings"
	"testing"

	"raunen/internal/provider"
)

func ladder() []Candidate {
	return []Candidate{
		{Ref: "ollama/small", Context: 4096},
		{Ref: "ollama/medium", Context: 16384},
		{Ref: "openrouter/big"}, // no declared window
	}
}

func drain(ch chan Event) []Event {
	close(ch)
	var out []Event
	for e := range ch {
		out = append(out, e)
	}
	return out
}

func TestEscalateIsOffUnlessEnabled(t *testing.T) {
	a := &Agent{ref: "ollama/small", contextTokens: 4096, fallbacks: ladder()}
	out := make(chan Event, 4)
	if a.escalate("because", out) {
		t.Error("escalated with auto-switch off")
	}
	if got := drain(out); len(got) != 0 {
		t.Errorf("emitted %d events with auto-switch off", len(got))
	}
}

func TestEscalateClimbsAndReports(t *testing.T) {
	a := &Agent{ref: "ollama/small", contextTokens: 4096, autoSwitch: true, fallbacks: ladder()}
	out := make(chan Event, 8)

	if !a.escalate("too tight", out) {
		t.Fatal("did not escalate")
	}
	if a.ref != "ollama/medium" || a.contextTokens != 16384 {
		t.Fatalf("moved to %s (%d), want ollama/medium (16384)", a.ref, a.contextTokens)
	}

	// The undeclared window is allowed: it was put in the ladder deliberately.
	if !a.escalate("still tight", out) {
		t.Fatal("did not escalate to the undeclared candidate")
	}
	if a.ref != "openrouter/big" {
		t.Fatalf("moved to %s, want openrouter/big", a.ref)
	}

	// Ladder exhausted.
	if a.escalate("nowhere left", out) {
		t.Error("escalated past the end of the ladder")
	}

	events := drain(out)
	if len(events) != 2 {
		t.Fatalf("emitted %d switches, want 2", len(events))
	}
	first, ok := events[0].(Switched)
	if !ok {
		t.Fatalf("event 0 is %T, want Switched", events[0])
	}
	if first.From != "ollama/small" || first.To != "ollama/medium" || first.Reason != "too tight" {
		t.Errorf("switch = %+v, want small→medium with the reason carried", first)
	}
}

// Moving to a model that is no roomier would hit the same ceiling again, so a
// smaller or equal declared window is skipped.
func TestEscalateSkipsNonUpgrades(t *testing.T) {
	a := &Agent{
		ref: "ollama/medium", contextTokens: 16384, autoSwitch: true,
		fallbacks: []Candidate{
			{Ref: "ollama/small", Context: 4096},
			{Ref: "ollama/same", Context: 16384},
			{Ref: "ollama/large", Context: 65536},
		},
	}
	out := make(chan Event, 4)
	if !a.escalate("tight", out) {
		t.Fatal("did not escalate")
	}
	if a.ref != "ollama/large" {
		t.Errorf("moved to %s, want ollama/large — smaller and equal windows must be skipped", a.ref)
	}
	drain(out)
}

// Escalation is for when trimming cannot help: what it may never drop already
// fills the window.
func TestNeedsMoreRoom(t *testing.T) {
	a := &Agent{contextTokens: 1024}
	a.messages = []provider.Message{
		{Role: provider.System, Content: "sys"},
		{Role: provider.User, Content: "short question"},
	}
	if _, tight := a.needsMoreRoom(); tight {
		t.Error("reported tight with a nearly empty conversation")
	}

	// A big tool result inside the current turn cannot be trimmed away.
	a.messages = append(a.messages,
		provider.Message{Role: provider.Assistant, ToolCalls: []provider.ToolCall{
			{ID: "c", Function: provider.Function{Name: "read", Arguments: "{}"}},
		}},
		provider.Message{Role: provider.ToolRole, ToolCallID: "c", Content: strings.Repeat("x", 3200)},
	)
	reason, tight := a.needsMoreRoom()
	if !tight {
		t.Fatal("did not report tight when the current turn alone fills the window")
	}
	if !strings.Contains(reason, "window") {
		t.Errorf("reason = %q, want it to explain the window", reason)
	}
}

// An unknown window means there is nothing to measure against, so escalation
// must not fire on a guess.
func TestNeedsMoreRoomSilentWithoutAContext(t *testing.T) {
	a := &Agent{contextTokens: 0}
	a.messages = []provider.Message{
		{Role: provider.System, Content: strings.Repeat("x", 100000)},
	}
	if _, tight := a.needsMoreRoom(); tight {
		t.Error("reported tight with no declared context")
	}
}
