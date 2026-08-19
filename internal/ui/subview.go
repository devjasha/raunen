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
// It appears when a sub-agent starts, follows along live, and collapses when
// the sub-agent is done, leaving a single line in the transcript.
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
}

// subViewRows is the most the panel will take from the transcript. A sub-agent
// can run for dozens of steps; the last few are the ones worth watching.
const subViewRows = 6

var (
	subBorder = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	subHead   = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
)

func (s *subView) add(line string) {
	s.lines = append(s.lines, line)
	// Only the tail is ever shown, so the rest is not worth keeping.
	if len(s.lines) > subViewRows*4 {
		s.lines = s.lines[len(s.lines)-subViewRows*4:]
	}
}

// height is the rows the panel occupies, so the transcript above can give up
// exactly that much and the input stays where it is. Only ever called for the
// panel being watched; a sub-agent nobody is looking at costs nothing, because
// its hint lives on the status row, which is always there.
func (s *subView) height() int {
	return min(len(s.lines), subViewRows) + 3
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
	head := "◆ " + spinner
	body := fmt.Sprintf(" %d sub-agents", len(subs))
	tail := fmt.Sprintf(" · %d steps  ctrl+o to watch", steps)
	if ansi.StringWidth(head+body+tail) > width {
		tail = "  ctrl+o"
	}
	if ansi.StringWidth(head+body+tail) > width {
		tail = ""
	}
	return subHead.Render(head) + subBorder.Render(body) + dimStyle.Render(tail)
}

// hint is the one-line notice shown under the input while a sub-agent runs and
// the panel is closed. It says that something is happening, roughly how far
// along it is, and how to look — which is all that is wanted from a glance.
func (s *subView) hint(spinner string, width int) string {
	head := "◆ " + spinner
	lead := " sub-agent  "
	step := ""
	if s.steps > 0 {
		step = fmt.Sprintf(" · %d steps", s.steps)
	}
	tail := step + "  ctrl+o to watch"

	// Shed detail as the terminal narrows, in order of what can be worked out
	// from elsewhere: the step count is on the panel, the key is in /help, and
	// the word "sub-agent" is implied by the marker. What must survive is the
	// marker and the spinner, which are what say something is running at all.
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

	return subHead.Render(head) + dimStyle.Render(lead) +
		subBorder.Render(label) + dimStyle.Render(tail)
}

// render draws the panel at the given width. n and i are how many sub-agents
// are running and which of them this is, so the header can say where the key
// goes next — with siblings ctrl+o moves along rather than closing, and a label
// that claimed otherwise would be a lie.
func (s *subView) render(width int, spinner string, i, n int) string {
	inner := max(20, width-4)

	var b strings.Builder
	label := "  ctrl+o to close"
	if n > 1 {
		label = fmt.Sprintf("  %d/%d  ctrl+o for next", i+1, n)
	}
	tail := dimStyle.Render(label)
	b.WriteString(subHead.Render("◆ "+spinner+" working on") + dimStyle.Render("  "+
		ansi.Truncate(s.desc, max(10, inner-34), "…")) + tail)

	shown := s.lines
	if len(shown) > subViewRows {
		shown = shown[len(shown)-subViewRows:]
	}
	for _, l := range shown {
		b.WriteString("\n" + ansi.Truncate(l, inner, "…"))
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(subBorder.GetForeground()).
		Padding(0, 1).
		Width(max(10, width-2)).
		Render(b.String())
}
