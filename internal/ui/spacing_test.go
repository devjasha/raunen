package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"testing"

	"raunen/internal/agent"
	"raunen/internal/companion"
	"raunen/internal/config"
	"raunen/internal/provider"
	"raunen/internal/session"
	"raunen/internal/tools"
)

func testModel(t *testing.T) Model {
	t.Helper()
	ag := agent.New(provider.New("http://localhost:1/v1", "", "m"),
		tools.Default(t.TempDir(), 4096), "")
	m := New(&config.Config{}, ag, t.TempDir(), "x/m",
		session.New(t.TempDir(), "x/m"), companion.Load())
	m.width, m.height = 80, 40
	return m
}

// plain renders the transcript without styling, which is what the spacing rules
// are about: a blank line is a blank line whether or not the terminal drew it
// faint.
func rowsOf(m Model) []string {
	out := make([]string, 0, len(m.entries))
	for _, e := range m.entries {
		for _, r := range e.rows(m.innerWidth()) {
			out = append(out, strings.TrimRight(ansi.Strip(r), " "))
		}
	}
	return out
}

// TestKindSpacing checks that a change of register opens a blank line and that
// lines of the same kind stay together. The point of the feature is that a long
// run of tool calls reads as one block rather than as a wall of lines with gaps
// in arbitrary places.
func TestKindSpacing(t *testing.T) {
	m := testModel(t)
	m.pushKind(entry{kind: kindUser, text: "question"})
	m.pushKind(entry{kind: kindWork, text: "tool one"})
	m.pushKind(entry{kind: kindWork, text: "tool two"})
	m.pushKind(entry{kind: kindWork, text: "tool three"})
	m.pushKind(entry{kind: kindReply, text: "answer"})

	got := rowsOf(m)
	want := []string{
		"question",
		"",
		"tool one",
		"tool two",
		"tool three",
		"",
		"answer",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d:\n%q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got %q, want %q\nall:\n%q", i, got[i], want[i], got)
		}
	}
}

// TestNoDoubleBlank guards the bug this refactor was most likely to introduce:
// a call site that still adds its own blank line, plus the automatic one, which
// leaves two rows of nothing between every message.
func TestNoDoubleBlank(t *testing.T) {
	m := testModel(t)
	m.openTurn("question", "")
	m.pushKind(entry{kind: kindWork, text: "⏺ read main.go"})
	m.pushKind(entry{kind: kindReply, text: "it parses flags"})

	rows := rowsOf(m)
	for i := 1; i < len(rows); i++ {
		if rows[i] == "" && rows[i-1] == "" {
			t.Errorf("two blank lines in a row at %d:\n%q", i, rows)
		}
	}
}

// TestNoticeStaysTight checks that a one-line aside does not get a blank line on
// both sides — it is already set apart by its marker, and two rows is a lot to
// spend saying the level went up.
func TestNoticeStaysTight(t *testing.T) {
	m := testModel(t)
	m.pushKind(entry{kind: kindWork, text: "tool"})
	m.pushKind(entry{kind: kindNotice, text: "★ level 8"})
	m.pushKind(entry{kind: kindWork, text: "tool"})

	if got := rowsOf(m); len(got) != 3 {
		t.Errorf("notice should not open blank lines, got %d rows:\n%q", len(got), got)
	}
}
