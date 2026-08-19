package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// subView is the panel a sub-agent works in. Its steps go here rather than into
// the transcript: the caller asked for an answer, not for a record of how it
// was found, and the transcript should end up reading like the conversation
// actually went.
//
// It appears when a sub-agent starts, follows along live, and — once the sub has
// reported back — stays open so the answer can be read, rather than collapsing
// the instant it finishes. Closing the panel (or the delegating turn ending) is
// what finally removes it.
type subView struct {
	// id is the agent's task ID, which is how events find their way here when
	// several sub-agents are running at once.
	id   string
	desc string
	// owner is the turn that delegated this task. Several turns can be running
	// at once, and a sub-agent must not outlive the one that spawned it — nor
	// be closed when a different one ends.
	owner *turn
	lines []string
	// steps counts tool calls, so the hint can say how far along it is without
	// the panel being open.
	steps int
	// color names this sub-agent. It is assigned from a palette at start and
	// reused on the panel border, its live lines, and its two transcript markers,
	// so several running at once are told apart by colour rather than by where
	// they happen to sit on the screen. Bright indices keep them clear of the
	// semantic colours (error red, user blue, tool cyan, spinner yellow) and of
	// the turn-gutter marks, which already use the darker ones.
	color string
	// done is set when the sub-agent has reported back. The panel stays so the
	// answer can be reviewed; its expanded window then shows that answer.
	done   bool
	answer string
	err    error
}

// subViewRows is the most the preview panel will show of the live steps. A
// sub-agent can run for dozens; the last few are the ones worth watching.
const subViewRows = 6

// expandedRows is the most the expanded panel will take. It is capped, not the
// whole screen, so a tall terminal does not hand a single sub a window the size
// of the session.
const expandedRows = 30

// subMarks name sub-agents. Colour alone is not enough — a narrow palette or a
// colour-blind reader would be left with two identical marks — so the glyph
// varies too, the way the turn marks do. They share the diamond family so a
// sub-agent reads as one kind of thing; the turn marks are bars.
var subMarks = []struct {
	glyph, color string
}{
	{"◆", "9"},
	{"◈", "11"},
	{"❖", "12"},
	{"⬢", "13"},
	{"✸", "14"},
	{"➤", "10"},
}

var subColorSeq int

// nextSubColor hands out the next sub-agent colour, cycling the palette. The UI
// is single-goroutine, so the counter needs no locking.
func nextSubColor() string {
	subColorSeq++
	return subMarks[subColorSeq%len(subMarks)].color
}

// glyph is this sub-agent's mark.
func (s *subView) glyph() string {
	if s.color == "" {
		return "◆"
	}
	for _, m := range subMarks {
		if m.color == s.color {
			return m.glyph
		}
	}
	return "◆"
}

// paint colours text in this sub-agent's colour.
func (s *subView) paint(text string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(s.color)).Render(text)
}

// mark is the coloured glyph that stands in for this sub-agent.
func (s *subView) mark() string {
	return s.paint(s.glyph() + " ")
}

func (s *subView) add(line string) {
	s.lines = append(s.lines, line)
	// Only the tail is ever shown in the preview, and even the expanded view is
	// capped, so the rest is not worth keeping.
	if len(s.lines) > subViewRows*8 {
		s.lines = s.lines[len(s.lines)-subViewRows*8:]
	}
}

// height is the rows the panel occupies. expanded opens it to as much of the
// available space as it can use; the preview keeps to the last few steps. avail
// is the rows left for the panel and the transcript together, so the panel
// never eats the whole screen — at least one transcript row is always kept.
func (s *subView) height(expanded bool, avail int) int {
	base := min(len(s.lines), subViewRows) + 3
	if !expanded {
		return fitPanel(base, avail)
	}
	big := min(avail, expandedRows)
	return fitPanel(max(base, big), avail)
}

// fitPanel keeps a desired panel size inside what is available, leaving at least
// one row for the transcript.
func fitPanel(n, avail int) int {
	if n > avail-1 {
		n = max(0, avail-1)
	}
	if n < 0 {
		n = 0
	}
	return n
}

// subsHint is the notice shown while sub-agents run with the panel closed. One
// gets named; several are counted, because three descriptions do not fit on a
// row and the count is what is actually wanted at a glance — that work is in
// flight, how much, and which key looks at it.
func subsHint(subs []*subView, spinner string, width int) string {
	if len(subs) == 0 {
		return ""
	}
	if len(subs) == 1 {
		return subs[0].hint(spinner, width)
	}

	steps := 0
	for _, s := range subs {
		steps += s.steps
	}
	// One coloured mark per running sub-agent, so "◆◆◆ 3 sub-agents" reads as
	// three distinct agents rather than one repeated.
	marks := ""
	for _, s := range subs {
		marks += s.mark()
	}
	body := fmt.Sprintf(" %d sub-agents", len(subs))
	tail := fmt.Sprintf(" · %d steps  ctrl+o to watch", steps)
	if ansi.StringWidth(marks+body+tail) > width {
		tail = "  ctrl+o"
	}
	if ansi.StringWidth(marks+body+tail) > width {
		tail = ""
	}
	return marks + dimStyle.Render(body) + dimStyle.Render(tail)
}

// hint is the one-line notice shown under the input while a sub-agent runs and
// the panel is closed. It says that something is happening, roughly how far
// along it is, and how to look — which is all that is wanted from a glance.
func (s *subView) hint(spinner string, width int) string {
	head := s.mark() + dimStyle.Render(spinner+" ")
	lead := "sub-agent  "
	step := ""
	if s.steps > 0 {
		step = fmt.Sprintf(" · %d steps", s.steps)
	}
	tail := step + "  ctrl+o to watch"

	// Shed detail as the terminal narrows, in order of what can be worked out
	// from elsewhere: the step count is on the panel, the key is in /help, and
	// the word "sub-agent" is implied by the mark. What must survive is the
	// mark, the spinner, and the description.
	fixed := func() int {
		return ansi.StringWidth(head) + ansi.StringWidth(lead) + ansi.StringWidth(tail)
	}
	if fixed() >= width {
		tail = "  ctrl+o"
	}
	if fixed() >= width {
		tail = ""
	}
	if fixed() >= width {
		lead = " "
	}
	label := ansi.Truncate(s.desc, max(0, width-fixed()), "…")

	return head + dimStyle.Render(lead) + dimStyle.Render(label) + dimStyle.Render(tail)
}

// render draws the panel at the given width, occupying rows rows. n is how many
// sub-agents are listed (running and finished), i is which of them this is, and
// expanded opens the window to show the full step history and — once the sub is
// done — its answer.
func (s *subView) render(width int, spinner string, i, n, rows int, expanded bool) string {
	inner := max(20, width-4)

	label := "  ctrl+o to close"
	if expanded {
		label = "  ctrl+o to shrink"
	} else if n > 1 {
		label = fmt.Sprintf("  %d/%d  ctrl+o for next", i+1, n)
	}

	var b strings.Builder
	if s.done {
		b.WriteString(s.paint(s.glyph()+" "+s.desc) + dimStyle.Render("  ✓ done") + dimStyle.Render(label))
	} else {
		b.WriteString(s.paint(s.glyph()+" "+spinner+" working on") +
			dimStyle.Render("  "+ansi.Truncate(s.desc, max(10, inner-34), "…")) + dimStyle.Render(label))
	}

	// Header + border account for three rows; the rest is body.
	body := max(0, rows-3)
	if expanded {
		b.WriteString(s.expandedBody(inner, body))
	} else {
		shown := s.lines
		if len(shown) > subViewRows {
			shown = shown[len(shown)-subViewRows:]
		}
		for _, l := range shown {
			b.WriteString("\n" + ansi.Truncate(l, inner, "…"))
		}
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(s.color)).
		Padding(0, 1).
		Width(max(10, width-2)).
		Render(b.String())
}

// expandedBody appends the sub-agent's full step history and, once it has
// finished, its answer, fitting within body rows. The answer lives here even
// though the transcript does not: the caller asked for an answer, and this is
// where it is shown. The answer is kept if it has to be traded against the
// steps, so a long reply is never squeezed out by its own working-out.
func (s *subView) expandedBody(inner, body int) string {
	if body <= 0 {
		return ""
	}
	answer := []string{}
	if s.done && s.answer != "" {
		answer = strings.Split(ansi.Wrap(s.answer, inner, ""), "\n")
	}
	sep := 0
	if s.done && len(answer) > 0 {
		sep = 1
	}
	steps := s.lines
	if len(steps)+sep+len(answer) > body {
		room := max(0, body-sep-len(answer))
		if len(steps) > room {
			steps = steps[len(steps)-room:]
		}
	}

	var b strings.Builder
	for _, l := range steps {
		b.WriteString("\n" + ansi.Truncate(l, inner, "…"))
	}
	if s.done && len(answer) > 0 {
		b.WriteString("\n" + dimStyle.Render(strings.Repeat("─", max(4, inner))))
		for _, l := range answer {
			b.WriteString("\n" + s.paint(l))
		}
	}
	return b.String()
}
