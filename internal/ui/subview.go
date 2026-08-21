package ui

import (
	"fmt"
	"strings"
	"time"

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
	// codes holds the edit windows this sub-agent has opened, one per write or
	// edit, so the panel can show the file it is working on the way the main
	// transcript does. Only the most recent is ever drawn — it is the change in
	// progress — but the whole list is kept so a long-running sub does not lose
	// its last window when the steps ahead of it scroll off the tail.
	codes []*codeBlock
	// steps counts tool calls, so the hint can say how far along it is without
	// the panel being open.
	steps int
	// start is when this sub-agent was spawned. A delegated task is the work
	// that most often runs long with nothing else to show for it, so the hint
	// reports duration alongside the step count.
	start time.Time
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
	// stop freezes the clock when the sub-agent reports back, so a panel left
	// open on a finished task shows how long it took rather than how long ago
	// it happened.
	stop time.Time
}

// subViewRows is the most the preview panel will show of the live steps. A
// sub-agent can run for dozens; the last few are the ones worth watching.
const subViewRows = 6

// expandedRows is the most the expanded panel will take. It is capped, not the
// whole screen, so a tall terminal does not hand a single sub a window the size
// of the session.
const expandedRows = 30

// subViewCodeRows is the most an edit window shows in the preview panel. The
// expanded panel asks the renderer for maxBodyLines instead; the window is the
// same data, drawn a few rows deep here and in full there, which is all the
// preview is for — to watch the change take shape, not to read the file.
const subViewCodeRows = 4

// codeFrameRows is what an edit window costs beyond its code: two border rows,
// the header naming the file, and the "… n more lines" marker when it is cut
// short. The panel has to pay for those out of its body budget or the window
// draws taller than the rows reserved for it.
const codeFrameRows = 4

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

// took is how long this sub-agent has been running, or ran for once it is
// done. Empty when it has no start time, which is what the tests build.
func (s *subView) took() string {
	if s.start.IsZero() {
		return ""
	}
	end := time.Now()
	if !s.stop.IsZero() {
		end = s.stop
	}
	return humanDuration(end.Sub(s.start))
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
//
// It is measured from the same rows render draws, at the same width, rather
// than guessed from the step count: the answer and the edit window are body
// rows too, and a panel that reserved a different number of rows than it drew
// moved everything below it — the input included — as it was opened.
func (s *subView) height(expanded bool, avail, width int) int {
	// A panel is a header, a body and two border rows; below four rows there is
	// nothing left to put in it, and one row has to stay with the transcript.
	if avail < panelChrome+1 {
		return 0
	}
	inner := panelInner(width)
	h := fitPanel(len(s.bodyRows(inner, bodyBudget(expanded, avail), expanded))+panelChrome, avail)
	// What the body wants and what avail allows can differ, and render sizes the
	// body from the number returned here — so settle on the height that produces
	// itself. bodyRows never returns more rows than it was given, so this only
	// ever shrinks and cannot loop.
	for {
		next := len(s.bodyRows(inner, h-panelChrome, expanded)) + panelChrome
		if next >= h {
			return h
		}
		h = next
	}
}

// panelChrome is what the panel costs before any body: the header row and the
// two border rows.
const panelChrome = 3

// bodyBudget is the most body rows the panel may use before avail is applied:
// the last few steps and a shallow edit window when previewing, as much as the
// cap allows when expanded.
func bodyBudget(expanded bool, avail int) int {
	if expanded {
		return min(avail, expandedRows) - panelChrome
	}
	return subViewRows + subViewCodeRows + codeFrameRows
}

// panelInner is the width available inside the panel's border and padding. It
// is derived from the width render hands lipgloss, not guessed alongside it: a
// row measured against a wider inner than the box really has wraps, and a
// wrapped row makes the panel taller than the rows reserved for it. Style.Width
// counts the border as well as the padding, so four cells come off, not two.
func panelInner(width int) int {
	return max(4, max(10, width-2)-4)
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

	// The oldest one's clock: with several running, that is the one that says
	// how long this has been going on, and the others are bounded by it.
	detail := fmt.Sprintf(" · %d steps", steps)
	if el := oldestSub(subs).took(); el != "" {
		detail += " · " + el
	}
	tail := detail + "  ctrl+o to watch"
	if ansi.StringWidth(marks+body+tail) > width {
		tail = detail + "  ctrl+o"
	}
	if ansi.StringWidth(marks+body+tail) > width {
		tail = detail
	}
	if ansi.StringWidth(marks+body+tail) > width {
		tail = ""
	}
	return marks + dimStyle.Render(body) + dimStyle.Render(tail)
}

// oldestSub is the sub-agent that has been running longest, which is the one
// whose clock speaks for the group. Never nil for a non-empty slice.
func oldestSub(subs []*subView) *subView {
	best := subs[0]
	for _, s := range subs[1:] {
		if s.start.IsZero() {
			continue
		}
		if best.start.IsZero() || s.start.Before(best.start) {
			best = s
		}
	}
	return best
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
	if el := s.took(); el != "" {
		step += " · " + el
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
	inner := panelInner(width)

	// ctrl+o walks one panel closed → preview → expanded → closed; with several
	// running, ←/→ move between them, so the position says which of them this is.
	label := "  ctrl+o to expand"
	if expanded {
		label = "  ctrl+o to close"
	}
	if n > 1 {
		// Which of them this is, and both keys: the arrows are new, and with a
		// panel open there is nowhere else that says how to put it away.
		label = fmt.Sprintf("  %d/%d  ←/→ switch · ctrl+o size", i+1, n)
	}

	// The header is one row, and it has to stay one row: wrapping to two makes
	// the panel taller than the rows reserved for it, which moves everything
	// below — the input included. So the description is given what the fixed
	// parts leave, and the finished row is truncated again as a guard, since at
	// twenty-odd cells even the fixed parts do not fit.
	lead, state := s.glyph()+" "+spinner+" working on  ", "" // running
	if s.done {
		lead, state = s.glyph()+" ", "  ✓ done"
	}
	room := inner - ansi.StringWidth(lead) - ansi.StringWidth(state) - ansi.StringWidth(label)
	head := s.paint(lead+ansi.Truncate(s.desc, max(0, room), "…")) +
		dimStyle.Render(state) + dimStyle.Render(label)

	var b strings.Builder
	b.WriteString(ansi.Truncate(head, inner, "…"))

	// Header + border account for three rows; the rest is body.
	for _, r := range s.bodyRows(inner, max(0, rows-panelChrome), expanded) {
		b.WriteString("\n" + r)
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(s.color)).
		Padding(0, 1).
		Width(max(10, width-2)).
		Render(b.String())
}

// bodyRows is everything inside the panel's border below the header: the steps,
// the answer once the sub-agent is done, and the edit window at the tail. It is
// what render draws and what height measures, so the two cannot disagree — a
// panel that reserved more rows than it drew left a gap the input floated up
// into, and one that drew more than it reserved pushed the input off the
// bottom. Never longer than body.
func (s *subView) bodyRows(inner, body int, expanded bool) []string {
	if body <= 0 {
		return nil
	}

	var rows []string
	if expanded {
		rows = s.answerRows(inner, body)
	} else {
		shown := s.lines
		if len(shown) > subViewRows {
			shown = shown[len(shown)-subViewRows:]
		}
		for _, l := range shown {
			rows = append(rows, ansi.Truncate(l, inner, "…"))
		}
	}
	if len(rows) > body {
		// The newest steps are the ones being watched, so the head goes first.
		rows = rows[len(rows)-body:]
	}

	// The edit window rides at the tail, after the steps (and the answer, once
	// the sub-agent is done), so the eye lands on the file most recently
	// touched. It takes only the rows the steps left over: they are the point of
	// the panel, and the window is a bonus that must not cost them a row.
	return append(rows, s.codeRows(inner, body-len(rows), expanded)...)
}

// codeRows renders the panel's edit window: the most recent write or edit this
// sub-agent made, drawn at the panel's inner width so it sits inside the border
// rather than spilling past it. Only the last is shown — it is the change in
// progress, and the earlier ones have scrolled off the tail of the steps with
// the calls that made them. The truncation is explicit: the preview asks for
// subViewCodeRows (a few, to watch the shape of the change form), the expanded
// panel asks for maxBodyLines (the whole window), and whichever is smaller —
// that cap or the rows still free in the body — wins, so the box is never taller
// than the space left and the same window reads shallow when collapsed and full
// when opened. Nothing is returned when there is no window or no room, so the
// caller can add the rows it has without special-casing the absence.
func (s *subView) codeRows(inner, room int, expanded bool) []string {
	if room <= 0 || len(s.codes) == 0 {
		return nil
	}
	c := s.codes[len(s.codes)-1]
	n := subViewCodeRows
	if expanded {
		n = maxBodyLines
	}
	// The box costs more rows than the code in it — two borders, a header, and
	// the "more lines" marker when it is cut short — and how many depends on
	// whether it was cut at all. Asking for room minus a guess would sometimes
	// overflow the panel, so the count is measured and walked down until it
	// fits: at most codeFrameRows+1 tries, on a window of a dozen rows.
	for n = min(n, room); n >= 1; n-- {
		if rows := c.rowsCapped(inner, n); len(rows) <= room {
			return rows
		}
	}
	return nil
}

// answerRows is the sub-agent's full step history and, once it has finished, its
// answer, fitting within body rows. The answer lives here even though the
// transcript does not: the caller asked for an answer, and this is where it is
// shown. The answer is kept if it has to be traded against the steps, so a long
// reply is never squeezed out by its own working-out — and it is the tail of a
// long answer that is cut, since the steps above it say what it is answering.
func (s *subView) answerRows(inner, body int) []string {
	if body <= 0 {
		return nil
	}
	var answer []string
	if s.done && s.answer != "" {
		answer = strings.Split(ansi.Wrap(s.answer, inner, ""), "\n")
	}
	sep := 0
	if len(answer) > 0 {
		sep = 1
	}
	if len(answer)+sep > body {
		answer = answer[:max(0, body-sep)]
	}
	steps := s.lines
	if room := max(0, body-sep-len(answer)); len(steps) > room {
		steps = steps[len(steps)-room:]
	}

	rows := make([]string, 0, len(steps)+sep+len(answer))
	for _, l := range steps {
		rows = append(rows, ansi.Truncate(l, inner, "…"))
	}
	if len(answer) > 0 {
		rows = append(rows, dimStyle.Render(strings.Repeat("─", max(4, inner))))
		for _, l := range answer {
			rows = append(rows, s.paint(l))
		}
	}
	return rows
}
