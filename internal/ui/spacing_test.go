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
	// Point the companion at a temporary directory: without this every test
	// that builds a model reads — and one that saves would overwrite — the
	// dragon of whoever is running the suite.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
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

// TestMultilineUserStaysStyled guards a bug that is invisible without reading
// escape codes: a question that wraps used to lose its colour after the first
// row. The style was opened once at the front of the string, but every
// continuation row starts with cont, which ends in a reset — so the bold blue
// stopped at the first wrap point and the rest of the question rendered plain.
func TestMultilineUserStaysStyled(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		width int
		want  int // rows the question should occupy
	}{
		// Typed with shift+enter: the newlines are in the text itself.
		{"hard newlines", "one\ntwo\nthree", 80, 3},
		// One long line the terminal has to break: the rows come from wrapping.
		{"soft wrap", strings.Repeat("word ", 40), 40, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(t)
			m.width = tc.width
			m.openTurn(tc.text, "")

			var rows []string
			for _, e := range m.entries {
				if e.kind != kindUser {
					continue
				}
				rows = append(rows, e.rows(m.innerWidth())...)
			}
			if len(rows) < tc.want {
				t.Fatalf("got %d rows, want at least %d:\n%q", len(rows), tc.want, rows)
			}
			for i, r := range rows {
				if strings.TrimSpace(ansi.Strip(r)) == "" {
					continue
				}
				// The bar prefix is blue on its own; what matters is that the
				// text after it is bold too, which is what userStyle adds.
				if !strings.Contains(r, "\x1b[1;34m") {
					t.Errorf("row %d is not styled as a user message: %q", i, r)
				}
			}
		})
	}
}
