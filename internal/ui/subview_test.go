package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestSubViewCollapsedTakesNoRows is the layout invariant the whole feature
// rests on: while the panel is closed it must contribute nothing to the height,
// or the cursor lands on the wrong row and the input appears to drift.
func TestSubViewCollapsedTakesNoRows(t *testing.T) {
	s := &subView{desc: "find the read tool"}
	s.add("⏺ grep read")
	s.add("↳ 12 lines")

	if got := s.height(); got != 0 {
		t.Errorf("collapsed panel takes %d rows, want 0", got)
	}
	s.open = true
	if got := s.height(); got == 0 {
		t.Error("open panel takes no rows")
	}
}

// TestSubViewHintFits checks the hint stays inside the width it is given. It
// sits on the status row, which is a single line — an overrun would push the
// input down and break the one thing the layout guarantees.
func TestSubViewHintFits(t *testing.T) {
	for _, width := range []int{20, 24, 30, 40, 60, 80, 120} {
		s := &subView{desc: strings.Repeat("a very long description ", 10), steps: 7}
		got := ansi.StringWidth(s.hint("⠋", width))
		if got > width {
			t.Errorf("width %d: hint renders %d cells:\n%q", width, got, s.hint("⠋", width))
		}
	}
}

// TestSubViewToggle covers the key's contract: it opens and closes, and does
// nothing at all when no sub-agent is running.
func TestSubViewToggle(t *testing.T) {
	m := testModel(t)
	before := m.viewHeight()
	if _, _ = m.onKey(keyPress("ctrl+o")); m.sub != nil {
		t.Fatal("ctrl+o invented a sub-agent")
	}
	if m.viewHeight() != before {
		t.Error("ctrl+o changed the layout with no sub-agent running")
	}

	m.sub = &subView{desc: "task"}
	m.sub.add("⏺ read x")
	ret, _ := m.onKey(keyPress("ctrl+o"))
	m = ret.(Model)
	if !m.sub.open {
		t.Error("ctrl+o did not open the panel")
	}
	if m.viewHeight() >= before {
		t.Error("opening the panel did not take rows from the transcript")
	}

	ret, _ = m.onKey(keyPress("ctrl+o"))
	m = ret.(Model)
	if m.sub.open {
		t.Error("ctrl+o did not close the panel")
	}
	if m.viewHeight() != before {
		t.Error("closing the panel did not give the rows back")
	}
}

// keyPress builds the message a real key press would produce, so tests exercise
// the same path the terminal does.
func keyPress(s string) tea.KeyPressMsg {
	if k, ok := strings.CutPrefix(s, "ctrl+"); ok {
		return tea.KeyPressMsg{Mod: tea.ModCtrl, Code: rune(k[0])}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}
