package ui

import (
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
// exactly that much and the input stays where it is.
func (s *subView) height() int {
	return min(len(s.lines), subViewRows) + 3
}

// render draws the panel at the given width.
func (s *subView) render(width int, spinner string) string {
	inner := max(20, width-4)

	var b strings.Builder
	b.WriteString(subHead.Render("◆ "+spinner+" working on") + dimStyle.Render("  "+
		ansi.Truncate(s.desc, max(10, inner-16), "…")))

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
