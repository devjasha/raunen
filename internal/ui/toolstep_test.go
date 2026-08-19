package ui

import (
	"strings"
	"testing"

	"raunen/internal/agent"
)

// step drives one complete tool call at a model the way the agent would, so a
// test can ask what a run of them leaves behind.
func step(m *Model, t *turn, name, args, result string) {
	m.onEvent(t, agent.ToolStart{Name: name, Args: args})
	m.onEvent(t, agent.ToolEnd{Name: name, Result: result})
}

// TestLiveStepStaysPut is the bug this file is about: each step replaced the
// last, as it should, but the line it drew sat one row lower every time. The
// transcript has to be the same height after the tenth step as after the first,
// or the reader watches the agent's working-out walk off the bottom.
func TestLiveStepStaysPut(t *testing.T) {
	// Each case is a different thing sitting above the step, because what
	// leaked was the separation the step asked for from whatever preceded it:
	// the blank pushKind opens on a change of kind, and the one pushTurn opens
	// when a second turn is writing into the same transcript.
	cases := []struct {
		name  string
		setup func(m *Model) *turn
	}{
		{"straight after the question", func(m *Model) *turn {
			tn := begin(m)
			m.openTurn(tn, "what is in here?", "")
			return tn
		}},
		{"after the model's prose", func(m *Model) *turn {
			tn := begin(m)
			m.openTurn(tn, "what is in here?", "")
			m.onEvent(tn, agent.TextDelta{Text: "let me look\n"})
			return tn
		}},
		{"while another turn is answering", func(m *Model) *turn {
			a, b := begin(m), begin(m)
			a.tag, b.tag = "A", "B"
			m.openTurn(a, "first", "")
			m.openTurn(b, "second", "")
			m.onEvent(a, agent.TextDelta{Text: "answering the first\n"})
			return b
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := testModel(t)
			tn := c.setup(&m)

			step(&m, tn, "read", `{"path":"main.go"}`, "ok")
			first := len(rowsOf(m))
			for i := 0; i < 8; i++ {
				step(&m, tn, "bash", `{"cmd":"ls"}`, "ok")
				if got := len(rowsOf(m)); got != first {
					t.Fatalf("after step %d the transcript is %d rows, want %d — the live step is drifting down:\n%q",
						i+2, got, first, rowsOf(m))
				}
			}
		})
	}
}

// TestLastStepOfATurnSurvivesTheNext guards the other half of the rule: only
// the step being replaced goes. What the agent did last time is part of the
// record of that exchange and must still be there once the next question opens
// its own rule.
func TestLastStepOfATurnSurvivesTheNext(t *testing.T) {
	m := testModel(t)
	first := begin(&m)
	m.openTurn(first, "what is in here?", "")
	step(&m, first, "read", `{"path":"main.go"}`, "it parses flags")

	second := begin(&m)
	m.openTurn(second, "and now?", "")
	step(&m, second, "bash", `{"cmd":"ls"}`, "ok")

	if !strings.Contains(transcript(m), "it parses flags") {
		t.Errorf("the previous turn's last step was erased across the turn boundary:\n%s", transcript(m))
	}
}

// TestLiveStepSpacing pins the shape of the transcript around the live step:
// one blank line between the model's prose and its working-out, and none
// between the call and the result it produced.
func TestLiveStepSpacing(t *testing.T) {
	m := testModel(t)
	tn := begin(&m)
	m.openTurn(tn, "what is in here?", "")
	m.onEvent(tn, agent.TextDelta{Text: "let me look\n"})
	step(&m, tn, "read", `{"path":"main.go"}`, "ok")
	step(&m, tn, "bash", `{"cmd":"ls"}`, "ok")

	rows := rowsOf(m)
	// The rule and the question open the turn; what matters here is the tail.
	tail := rows[len(rows)-4:]
	want := []string{"let me look", "", "  ⏺ bash  {\"cmd\":\"ls\"}", "    ↳ ok"}
	for i := range want {
		if tail[i] != want[i] {
			t.Errorf("row %d of the tail: got %q, want %q\nall:\n%q", i, tail[i], want[i], rows)
		}
	}
}
