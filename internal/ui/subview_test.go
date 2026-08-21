package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"raunen/internal/agent"
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

// TestSubViewHeightMatchesRender is the bug that made expanding a panel move
// the input: height() guessed the rows from the step count while render() drew
// the answer and the edit window too, so what the layout reserved and what
// appeared on screen disagreed — and everything below the panel, the prompt
// included, slid by the difference.
func TestSubViewHeightMatchesRender(t *testing.T) {
	code := &codeBlock{
		path: "internal/thing.go", label: "+40 -2", numbered: true,
	}
	for i := 0; i < 40; i++ {
		code.lines = append(code.lines, diffLine{
			kind: lineAdd, text: fmt.Sprintf("\tsomething(%d)", i), newNo: i + 1,
		})
	}

	cases := []struct {
		name string
		s    *subView
	}{
		{"empty", &subView{id: "t", desc: "task", color: "9"}},
		{"few steps", &subView{id: "t", desc: "task", color: "9",
			lines: []string{"⏺ read a.go", "⏺ grep foo"}}},
		{"long description", &subView{id: "t", color: "9",
			desc:  strings.Repeat("a very long description ", 8),
			lines: []string{"⏺ read a.go"}}},
		{"done with a long answer", &subView{id: "t", desc: "task", color: "9",
			done: true, answer: strings.Repeat("an answer sentence. ", 80),
			lines: []string{"⏺ read a.go", "⏺ grep foo"}}},
		{"an edit window", &subView{id: "t", desc: "task", color: "9",
			lines: []string{"⏺ write internal/thing.go"}, codes: []*codeBlock{code}}},
		{"steps, answer and a window", &subView{id: "t", desc: "task", color: "9",
			done: true, answer: strings.Repeat("an answer sentence. ", 40),
			lines: []string{"⏺ a", "⏺ b", "⏺ c", "⏺ d", "⏺ e", "⏺ f", "⏺ g", "⏺ h"},
			codes: []*codeBlock{code}}},
	}

	for _, c := range cases {
		// Many steps is the ordinary case for a long-running sub-agent, so the
		// tall variants are covered as well as the ones above.
		for _, width := range []int{40, 80, 120} {
			for _, avail := range []int{6, 12, 25, 37} {
				for _, expanded := range []bool{false, true} {
					want := c.s.height(expanded, avail, width)
					got := strings.Count(
						c.s.render(width, "⠋", 0, 1, want, expanded), "\n") + 1
					if got != want {
						t.Errorf("%s (width %d, avail %d, expanded %v): reserved %d rows, drew %d",
							c.name, width, avail, expanded, want, got)
					}
					if want > avail-1 {
						t.Errorf("%s (width %d, avail %d, expanded %v): took %d of %d rows, leaving none for the transcript",
							c.name, width, avail, expanded, want, avail)
					}
				}
			}
		}
	}
}

// TestSubViewHintFits checks the hint stays inside the width it is given. It
// sits on the status row, which is a single line — an overrun would push the
// input down and break the one thing the layout guarantees.
func TestSubViewHintFits(t *testing.T) {
	for _, width := range []int{20, 24, 30, 40, 60, 80, 120} {
		// A start time well in the past, so the elapsed field is at its widest
		// — an hours-long run is exactly when the row is most likely to overrun.
		s := &subView{
			desc:  strings.Repeat("a very long description ", 10),
			steps: 7,
			start: time.Now().Add(-3*time.Hour - 25*time.Minute),
		}
		got := ansi.StringWidth(s.hint("⠋", width))
		if got > width {
			t.Errorf("width %d: hint renders %d cells:\n%q", width, got, s.hint("⠋", width))
		}
	}
}

// The group clock speaks for the oldest sub-agent: with several running, that is
// the one that says how long the work has been going.
func TestSubsHintClockFollowsOldest(t *testing.T) {
	subs := []*subView{
		{desc: "recent", start: time.Now().Add(-5 * time.Second)},
		{desc: "old", start: time.Now().Add(-2 * time.Minute)},
	}
	if got := oldestSub(subs).desc; got != "old" {
		t.Errorf("group clock followed %q, want the oldest sub-agent", got)
	}
}

// A finished sub-agent's panel stays open, and its clock must stop with it —
// otherwise a completed task keeps counting and reads as still running.
func TestSubViewClockStopsWhenDone(t *testing.T) {
	s := &subView{start: time.Now().Add(-90 * time.Second), stop: time.Now().Add(-30 * time.Second)}
	if got := s.took(); got != "1m 00s" {
		t.Errorf("took() = %q, want the frozen 1m 00s", got)
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

// TestSubViewCyclesThroughSiblings covers the two keys with several running:
// ctrl+o sizes one panel and ←/→ choose which sub-agent it is on, so getting to
// the third of three is one press rather than five, and there is a way back.
func TestSubViewCyclesThroughSiblings(t *testing.T) {
	m := testModel(t)
	for _, id := range []string{"t1", "t2", "t3"} {
		m.subs = append(m.subs, &subView{id: id, desc: id})
	}

	type step struct {
		key string
		w   string
		e   bool
	}
	for _, want := range []step{
		// ctrl+o alone sizes the panel and never changes which one is watched.
		{"ctrl+o", "t1", false},
		{"ctrl+o", "t1", true},
		{"ctrl+o", "", false},
		// The arrows walk the ring, in both directions and wrapping either way.
		{"ctrl+o", "t1", false},
		{"right", "t2", false},
		{"right", "t3", false},
		{"right", "t1", false},
		{"left", "t3", false},
		// Size is kept across a switch: expanded on one is expanded on the next.
		{"ctrl+o", "t3", true},
		{"left", "t2", true},
	} {
		ret, _ := m.onKey(keyPress(want.key))
		m = ret.(Model)
		if m.watching != want.w || m.expanded != want.e {
			t.Fatalf("after %s: watching=%q(%v), want %q(%v)",
				want.key, m.watching, m.expanded, want.w, want.e)
		}
	}
}

// The arrows belong to the input whenever there is something to move a cursor
// through: switching panels must not make editing a typed line unreliable.
func TestSubViewArrowsYieldToTheInput(t *testing.T) {
	m := testModel(t)
	for _, id := range []string{"t1", "t2"} {
		m.subs = append(m.subs, &subView{id: id, desc: id})
	}
	m.watching = "t1"
	m.input.SetValue("hello")

	ret, _ := m.onKey(keyPress("right"))
	if got := ret.(Model).watching; got != "t1" {
		t.Errorf("→ switched to %q while text was typed, want the input to keep the key", got)
	}
}

// With no panel open the arrows are the input's, whatever is running.
func TestSubViewArrowsNeedAnOpenPanel(t *testing.T) {
	m := testModel(t)
	for _, id := range []string{"t1", "t2"} {
		m.subs = append(m.subs, &subView{id: id, desc: id})
	}

	ret, _ := m.onKey(keyPress("right"))
	if got := ret.(Model).watching; got != "" {
		t.Errorf("→ opened a panel on %q with none open, want the key left alone", got)
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
	// Start times well in the past, so the elapsed field is at its widest — an
	// hours-long run is when the row is most likely to overrun.
	subs := []*subView{
		{id: "t1", desc: "search the ui package", steps: 4, start: time.Now().Add(-4 * time.Hour)},
		{id: "t2", desc: "search the agent package", steps: 2, start: time.Now().Add(-time.Minute)},
		{id: "t3", desc: "search the tools package", steps: 9, start: time.Now().Add(-time.Second)},
	}
	for _, width := range []int{20, 30, 40, 60, 80, 120} {
		got := ansi.StringWidth(subsHint(subs, "⠋", width))
		if got > width {
			t.Errorf("width %d: hint renders %d cells:\n%q",
				width, got, subsHint(subs, "⠋", width))
		}
	}
}

// TestSubViewEditWindowRoutedByTaskID is the concurrency guarantee the panels
// rest on: several sub-agents run at once, distinguished only by task id, and an
// edit from one must reach that one's panel and no other's — a write by t1 must
// not appear in t2's window, or the reader would be watching the wrong agent.
func TestSubViewEditWindowRoutedByTaskID(t *testing.T) {
	m := testModel(t)
	turn := begin(&m)

	// Two sub-agents start together, the way a fan-out delegation does.
	m.onEvent(turn, agent.TaskStart{ID: "t1", Description: "rewrite the ui"})
	m.onEvent(turn, agent.TaskStart{ID: "t2", Description: "tidy the agent"})

	args := mustJSON(t, map[string]string{
		"path":    "panel.go",
		"content": "package ui\n\nfunc Panel() {}\n",
	})
	m.onEvent(turn, agent.ToolStart{Name: "write", Task: "t1", Args: args})

	if got := len(m.sub("t1").codes); got != 1 {
		t.Fatalf("t1 got %d code windows, want 1", got)
	}
	if got := len(m.sub("t2").codes); got != 0 {
		t.Fatalf("t2 got %d code windows, want 0 — an edit must not leak across panels", got)
	}

	// Another edit from t1 appends to its own list; an edit from t2 opens its
	// first. The windows stay partitioned by the task id throughout.
	m.onEvent(turn, agent.ToolStart{Name: "write", Task: "t1", Args: args})
	m.onEvent(turn, agent.ToolStart{Name: "write", Task: "t2", Args: args})
	if got := len(m.sub("t1").codes); got != 2 {
		t.Fatalf("t1 should hold 2 windows, got %d", got)
	}
	if got := len(m.sub("t2").codes); got != 1 {
		t.Fatalf("t2 should hold 1 window, got %d", got)
	}
}

// TestSubViewCodePreviewTruncatesBody checks the one behaviour the dual render
// depends on: the preview shows only a few rows of the window while the expanded
// panel shows the full cap, so the collapsed panel stays small and opening it
// reveals more. The truncation is the point — without it the preview would be
// the entire panel.
func TestSubViewCodePreviewTruncatesBody(t *testing.T) {
	// Far more lines than any panel draws, so the caps actually bite.
	var lines []diffLine
	for i := 0; i < maxBodyLines+10; i++ {
		lines = append(lines, diffLine{kind: lineAdd, text: fmt.Sprintf("line %d", i), newNo: i + 1})
	}
	s := &subView{codes: []*codeBlock{{path: "big.go", label: "new", indent: "", lines: lines, numbered: true}}}

	const inner = 76
	preview := s.codeRows(inner, subViewRows*8, false)
	expanded := s.codeRows(inner, expandedRows, true)

	if len(expanded) <= len(preview) {
		t.Fatalf("expanded %d rows not taller than preview %d", len(expanded), len(preview))
	}
	// A window of n body lines is two border rows, one header, n rows, and — when
	// anything was cut off — one "more lines" marker, so the total is n+4. The
	// preview must therefore top out at subViewCodeRows of body and the expanded
	// at maxBodyLines.
	if len(preview) > subViewCodeRows+4 {
		t.Fatalf("preview drew %d rows, want at most the cap plus border, header and marker", len(preview))
	}
	if len(expanded) > maxBodyLines+4 {
		t.Fatalf("expanded drew %d rows, want at most the cap plus border, header and marker", len(expanded))
	}
}

// TestSubViewCodeFitsNestedWidth is the width invariant for the new window: it
// sits inside the panel's own border and padding, so its rows must not exceed
// the content width those leave — six cells narrower than the panel's outer
// width, two for the border and two for the padding. At the widths a terminal
// actually gets used at, the box must never spill past the panel.
func TestSubViewCodeFitsNestedWidth(t *testing.T) {
	var lines []diffLine
	for i := 0; i < 20; i++ {
		lines = append(lines, diffLine{
			kind:  lineAdd,
			text:  fmt.Sprintf("a line of content padded out to overflow at narrow widths number %d", i),
			newNo: i + 1,
		})
	}
	s := &subView{codes: []*codeBlock{{
		path: "a/very/long/path/to/some/file.go", label: "+20", indent: "", lines: lines, numbered: true,
	}}}

	for _, width := range []int{24, 40, 80} {
		// render hands the panel its outer width; the content it may fill is six
		// cells narrower once the border and padding are accounted for. The code
		// box is built at width-4, the same inner width render uses.
		content := width - 6
		for _, r := range s.codeRows(width-4, expandedRows, true) {
			if got := ansi.StringWidth(r); got > content {
				t.Errorf("width %d: code row is %d cells, over the nested %d:\n%q",
					width, got, content, r)
			}
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
