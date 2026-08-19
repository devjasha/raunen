package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestSubViewCollapsedTakesNoRows is the layout invariant the whole feature
// rests on: a sub-agent nobody is watching must contribute nothing to the
// height, or the cursor lands on the wrong row and the input appears to drift.
func TestSubViewCollapsedTakesNoRows(t *testing.T) {
	m := testModel(t)
	before := m.viewHeight()

	m.subs = append(m.subs, &subView{id: "t1", desc: "find the read tool"})
	m.subs[0].add("⏺ grep read")
	if m.viewHeight() != before {
		t.Error("an unwatched sub-agent took rows from the transcript")
	}

	m.watching = "t1"
	if m.viewHeight() >= before {
		t.Error("watching a sub-agent did not take rows for its panel")
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

// TestSubViewToggle covers the key's contract: one press opens a preview, a
// second expands it, and it does nothing at all when no sub-agent is running.
func TestSubViewToggle(t *testing.T) {
	m := testModel(t)
	before := m.viewHeight()
	if _, _ = m.onKey(keyPress("ctrl+o")); len(m.subs) != 0 || m.watching != "" {
		t.Fatal("ctrl+o invented a sub-agent")
	}
	if m.viewHeight() != before {
		t.Error("ctrl+o changed the layout with no sub-agent running")
	}

	m.subs = append(m.subs, &subView{id: "t1", desc: "task"})
	m.subs[0].add("⏺ read x")
	ret, _ := m.onKey(keyPress("ctrl+o"))
	m = ret.(Model)
	if m.watching != "t1" || m.expanded {
		t.Errorf("ctrl+o did not open a preview, watching=%q expanded=%v", m.watching, m.expanded)
	}
	if m.viewHeight() >= before {
		t.Error("opening the panel did not take rows from the transcript")
	}

	ret, _ = m.onKey(keyPress("ctrl+o"))
	m = ret.(Model)
	if m.watching != "t1" || !m.expanded {
		t.Errorf("ctrl+o did not expand the panel, watching=%q expanded=%v", m.watching, m.expanded)
	}
	if m.viewHeight() >= before {
		t.Error("expanding the panel did not take more rows from the transcript")
	}

	// A third press closes the panel and gives the rows back.
	ret, _ = m.onKey(keyPress("ctrl+o"))
	m = ret.(Model)
	if m.watching != "" {
		t.Errorf("ctrl+o did not close the panel, watching=%q", m.watching)
	}
	if m.viewHeight() != before {
		t.Error("closing the panel did not give the rows back")
	}
}

// TestSubViewCyclesThroughSiblings covers the key with several running: it
// previews each in turn, expands, then moves to the next, and the press after
// the last closes the panel — so one key means both "look" and "look at the
// other one".
func TestSubViewCyclesThroughSiblings(t *testing.T) {
	m := testModel(t)
	for _, id := range []string{"t1", "t2", "t3"} {
		m.subs = append(m.subs, &subView{id: id, desc: id})
	}

	type step struct {
		w string
		e bool
	}
	for _, want := range []step{
		{"t1", false},
		{"t1", true},
		{"t2", false},
		{"t2", true},
		{"t3", false},
		{"t3", true},
		{"", false},
	} {
		ret, _ := m.onKey(keyPress("ctrl+o"))
		m = ret.(Model)
		if m.watching != want.w || m.expanded != want.e {
			t.Fatalf("after press: watching=%q(%v), want %q(%v)",
				m.watching, m.expanded, want.w, want.e)
		}
	}
}

// TestWatchedSurvivesASiblingFinishing is why the watch is an id rather than an
// index: a sub-agent finishing must not slide the panel onto a different one.
func TestWatchedSurvivesASiblingFinishing(t *testing.T) {
	m := testModel(t)
	for _, id := range []string{"t1", "t2", "t3"} {
		m.subs = append(m.subs, &subView{id: id, desc: id})
	}
	m.watching = "t3"

	m.dropSub("t1")
	if m.watching != "t3" {
		t.Errorf("watching moved to %q when a sibling finished", m.watching)
	}

	// When the watched one finishes, the panel moves to another still running
	// rather than closing under the reader.
	m.dropSub("t3")
	if m.watching != "t2" {
		t.Errorf("watching=%q after the watched sub-agent finished, want t2", m.watching)
	}

	m.dropSub("t2")
	if m.watching != "" {
		t.Errorf("watching=%q with nothing running, want empty", m.watching)
	}
}

// TestSubsHintFits keeps the multi-agent notice inside one row, at the widths a
// terminal actually gets used at.
func TestSubsHintFits(t *testing.T) {
	subs := []*subView{
		{id: "t1", desc: "search the ui package", steps: 4},
		{id: "t2", desc: "search the agent package", steps: 2},
		{id: "t3", desc: "search the tools package", steps: 9},
	}
	for _, width := range []int{20, 30, 40, 60, 80, 120} {
		got := ansi.StringWidth(subsHint(subs, "⠋", width))
		if got > width {
			t.Errorf("width %d: hint renders %d cells:\n%q",
				width, got, subsHint(subs, "⠋", width))
		}
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
