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
	desc  string
	lines []string
	// open is whether the full panel is shown. A sub-agent starts collapsed to
	// a single hint under the input: it is working-out, and the caller asked
	// for an answer rather than for a running commentary. Watching it is a
	// deliberate act, so the panel opens on a keystroke and stays open until
	// dismissed or until the sub-agent finishes.
	open bool
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
// exactly that much and the input stays where it is. Collapsed it takes
// nothing: the hint lives on the status row, which is always there.
func (s *subView) height() int {
	if !s.open {
		return 0
	}
	return min(len(s.lines), subViewRows) + 3
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

// render draws the panel at the given width.
func (s *subView) render(width int, spinner string) string {
	inner := max(20, width-4)

	var b strings.Builder
	tail := dimStyle.Render("  ctrl+o to close")
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
