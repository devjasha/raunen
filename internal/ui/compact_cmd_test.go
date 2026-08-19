package ui

import (
	"strings"
	"testing"

	"raunen/internal/agent"
)

// TestCompactCommandReportsWhatItWon covers the line the user reads: a
// compaction that says nothing looks like a command that did nothing.
func TestCompactCommandReportsWhatItWon(t *testing.T) {
	m := testModel(t)
	tn := begin(&m)
	m.onEvent(tn, agent.Compacted{
		Replaced: 46, Kept: 8,
		Before: 82_000, After: 19_000,
		Summary: "Goal — fix the parser.",
	})

	out := strings.Join(rowsOf(m), "\n")
	for _, want := range []string{"46", "76%", "82k", "19k", "8"} {
		if !strings.Contains(out, want) {
			t.Errorf("the compaction report is missing %q:\n%s", want, out)
		}
	}
	// The bar has to follow the conversation down, or it reads as full against
	// messages that are no longer there.
	if m.ctxTokens != 19_000 {
		t.Errorf("context still reads %d tokens after compaction, want 19000", m.ctxTokens)
	}
	if tn.warnedFull {
		t.Error("the full-context warning was not rearmed, so it will not fire again")
	}
}

// TestAutoCompactSaysWhyItHappened separates the two cases: one the user asked
// for, and one that happened to them mid-turn.
func TestAutoCompactSaysWhyItHappened(t *testing.T) {
	m := testModel(t)
	m.onEvent(begin(&m), agent.Compacted{Replaced: 20, Kept: 4, Before: 1000, After: 400, Auto: true})

	if out := strings.Join(rowsOf(m), "\n"); !strings.Contains(out, "context was full") {
		t.Errorf("an automatic compaction did not say what caused it:\n%s", out)
	}
}

// TestCompactRefusesMidTurn guards the obvious footgun: rewriting the message
// list while the agent is streaming against it.
func TestCompactRefusesMidTurn(t *testing.T) {
	m := testModel(t)
	running := begin(&m)
	m.command("/compact")

	if len(m.turns) != 1 || m.turns[0] != running {
		t.Error("/compact took over a turn that was still running")
	}
	if out := strings.Join(rowsOf(m), "\n"); !strings.Contains(out, "wait") {
		t.Errorf("/compact refused silently:\n%s", out)
	}
}

// TestCompactFailureDoesNotReadAsAFailedTurn checks the distinction the event
// exists for: the turn carries on and trims instead.
func TestCompactFailureDoesNotReadAsAFailedTurn(t *testing.T) {
	m := testModel(t)
	m.onEvent(begin(&m), agent.CompactFailed{Err: errStub("could not summarise the conversation: endpoint refused")})

	out := strings.Join(rowsOf(m), "\n")
	if !strings.Contains(out, "endpoint refused") {
		t.Errorf("the reason was not shown:\n%s", out)
	}
	if strings.Contains(out, "✗") {
		t.Errorf("a survivable compaction failure was rendered as a failed turn:\n%s", out)
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }
