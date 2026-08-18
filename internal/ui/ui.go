// Package ui renders the agent in the terminal.
//
// Three constraints shape everything here:
//
//   - No background. Nothing in this file ever calls Background(), and
//     View.BackgroundColor is left nil, so the terminal's own background shows
//     through untouched. Colors are ANSI indices 0-15 so they follow the
//     terminal's palette instead of fighting it.
//
//   - The input is welded to the bottom row and never moves, from the very
//     first frame. That requires owning the whole screen, so the program takes
//     the alternate screen and renders the transcript itself.
//
//     Two earlier attempts rendered inline instead, to keep the terminal's own
//     scrollback. Neither could hold the input still: padding the frame to
//     reach the bottom meant predicting how many rows the renderer had
//     consumed, which it does not report, so the count drifted and the input
//     crept upward; and letting the renderer place the frame naturally left the
//     input walking down the screen as output arrived.
//
//   - Because the alternate screen means the terminal's scrollback no longer
//     holds the conversation, the transcript is scrollable in-app and the
//     conversation is saved to disk. Quitting leaves the terminal exactly as it
//     was found; sessions are resumed rather than replayed into it.
package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"raunen/internal/agent"
	"raunen/internal/companion"
	"raunen/internal/config"
	"raunen/internal/provider"
	"raunen/internal/session"
	"raunen/internal/vcs"
)

// Foreground-only styles. Adding a Background() call anywhere below would
// break the transparency this program is built around.
var (
	promptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	userStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	toolStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	branchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	modelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	spinStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	thinkStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	askStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	barStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	taskStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))

	// Working-out — tool calls, their results, delegated tasks — is rendered
	// faint so it sits behind the conversation rather than beside it. A
	// terminal has one font size, so weight is the only axis available: the
	// question and the answer are what you read, and the steps between them are
	// there to be glanced at.
	//
	// Faint is SGR 2. Terminals that do not implement it fall back to the plain
	// colour, which is what these lines looked like before — so the worst case
	// is the old appearance rather than an unreadable one.
	workStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Faint(true)
	workDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)
	workErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Faint(true)

	// The border tracks state: quiet when idle, warm while the model works.
	borderIdle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	borderBusy = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

// modeStyle colours the mode indicator by how much rope the agent has.
func modeStyle(m agent.Mode) lipgloss.Style {
	switch m {
	case agent.ModePlan:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true)
	case agent.ModeAccept:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	}
}

// Context fullness, coloured by how much room is left.
func usageStyle(pct int) lipgloss.Style {
	switch {
	case pct >= 85:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	case pct >= 60:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	}
}

const prompt = "› "

// chromeLines is everything below the transcript apart from the input's own
// rows: a status row, the input's top and bottom border, the bar beneath it,
// and the bottom gutter.
const chromeLines = 4 + padBottom

// padBottom is blank rows kept under the status bar, so the frame does not sit
// flush against the last row of the terminal.
const padBottom = 1

// maxInputRows caps how tall the input may grow, so that a long message cannot
// squeeze the transcript off the screen.
const maxInputRows = 8

// boxPadX is the horizontal padding inside the input border.
const boxPadX = 1

// padX is the gutter kept on both sides of the screen, so nothing sits flush
// against the terminal edge.
const padX = 2

// maxLines caps the retained transcript. Old lines are dropped rather than
// growing the model without bound over a long session.
const maxLines = 10000

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type eventMsg struct{ ev agent.Event }
type streamDoneMsg struct{}
type tickMsg time.Time

type Model struct {
	cfg   *config.Config
	ag    *agent.Agent
	root  string
	input textarea.Model

	events chan agent.Event
	cancel context.CancelFunc
	busy   bool

	// entries is the transcript as logical lines, before wrapping. It is the
	// source of truth; display holds the same content wrapped to width.
	entries []entry
	// display is lines wrapped to wrapWidth, which is what actually gets shown.
	// It is rebuilt only when the width changes.
	display   []string
	wrapWidth int
	// scroll is how many display rows the view is lifted off the bottom. Zero
	// means pinned to the newest output.
	scroll int
	// owner maps each display row back to the entry that produced it, which is
	// what makes a click resolvable to a message.
	owner []int
	// blockSeq numbers messages; block starts bump it.
	blockSeq int
	// replyTo is the message being replied to, quoted into the next send.
	replyTo string

	// pending is assistant text received but not yet turned into whole lines.
	pending string
	// think is the tail of a reasoning model's thinking, shown as a single
	// status row. It never enters the transcript.
	think string
	// md carries fenced-code-block state across streamed lines.
	md markdown
	// inText records whether the last thing written was assistant prose, so a
	// blank line can separate it from the tool calls around it.
	inText bool
	frame  int

	// ref is the active "provider/model" reference, kept so the context limit
	// can be looked up again after /model.
	ref string
	// rejected records that the chosen model refused outright this turn, so a
	// switch away from it can be made permanent rather than repeated tomorrow.
	rejected bool
	// adopt is a model to remember as the default, but only after it answers.
	adopt string
	// chosen is the model the user picked, which is not always the active one:
	// escalation moves ref to a roomier model for a turn, and that is a
	// temporary measure rather than a decision worth remembering.
	chosen string
	// branch is the git branch of root, empty outside a repository.
	branch string
	// ctxTokens is the conversation size reported by the last request.
	ctxTokens int
	// warnedFull records that the context-pressure warning has been shown for
	// the current turn, so it is not repeated on every request within it.
	warnedFull bool
	// sess is the conversation on disk, written after each turn.
	sess *session.Session
	// comp is the mascot's progress, which spans every session and provider
	// rather than belonging to any one of them.
	comp *companion.Companion
	// subs are the sub-agents currently running, in the order they started.
	// Several can be in flight at once, so this is a list rather than the one
	// panel it used to be; watching is one at a time, since only one of them
	// can have the rows.
	subs []*subView
	// watching is the id of the sub-agent whose panel is open, empty when the
	// panel is closed. Kept as an id rather than an index so a sibling
	// finishing does not shift what is being watched.
	watching string
	// queued is a message typed while the agent was busy. The model cannot take
	// it mid-turn — it is blocked on a tool result it asked for — so it is held
	// and sent the moment the turn ends.
	queued string
	// keyAsk is the API key prompt, open only while asking.
	keyAsk *keyPrompt
	// pick is the model chooser overlay, open only while choosing.
	pick *picker
	// ask is a tool call waiting on the user in accept mode. While it is set
	// the loop is blocked, so keys answer the prompt rather than type.
	ask *agent.Approval
	// sug is the completion list for a / command being typed, nil otherwise.
	sug *suggest
	// sugOff dismisses the list until the input changes again, so esc and an
	// accepted completion both close it without it springing straight back.
	sugOff bool
	// mcp is the number of tools each MCP server contributed this run, keyed by
	// server name. Empty when none were configured; absent keys mean the server
	// was defined but never started.
	mcp map[string]int
	// lastInput is what the input held when the list was last rebuilt, which is
	// how a dismissal can tell "still the same word" from "typing again".
	lastInput string
	// files is the snapshot @ mentions complete against, nil until the first
	// one is typed: a session that never mentions a file never pays for a scan.
	files *fileIndex
	// scanning records that a scan is in flight, so a burst of keystrokes
	// starts one walk of the tree rather than one per key.
	scanning bool

	width  int
	height int
	quit   bool
}

// SetMCPSummary records how many tools each MCP server contributed this run, so
// /mcp and /status can list them. It is set once at startup, before the program
// starts, and never changed — the agent keeps its tools for the whole session.
func (m *Model) SetMCPSummary(s map[string]int) { m.mcp = s }

func New(cfg *config.Config, ag *agent.Agent, root, ref string, sess *session.Session, comp *companion.Companion) Model {
	ti := textarea.New()
	ti.Placeholder = "ask something, or /help"
	ti.ShowLineNumbers = false
	// Wrap across the full width and grow downward with the message, rather
	// than scrolling sideways on a single line.
	ti.DynamicHeight = true
	ti.MinHeight = 1
	ti.MaxHeight = maxInputRows
	// Use the terminal's real cursor rather than a drawn block: a virtual
	// cursor would paint a cell background.
	ti.SetVirtualCursor(false)

	// Enter sends the message, so inserting a newline moves to a modifier.
	ti.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "alt+enter", "ctrl+j"),
		key.WithHelp("shift+enter", "newline"),
	)

	// The marker belongs on the first row only; wrapped rows align under it.
	ti.SetPromptFunc(lipgloss.Width(prompt), func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return promptStyle.Render(prompt)
		}
		return strings.Repeat(" ", lipgloss.Width(prompt))
	})

	// Built from scratch rather than from DefaultStyles, which set a
	// cursor-line background that would break the transparency here.
	var st textarea.Styles
	st.Focused.Placeholder = dimStyle
	st.Blurred.Placeholder = dimStyle
	ti.SetStyles(st)

	ti.Focus()

	m := Model{
		cfg:    cfg,
		ag:     ag,
		root:   root,
		ref:    ref,
		chosen: ref,
		sess:   sess,
		comp:   comp,
		branch: vcs.Branch(root),
		input:  ti,
		width:  80,
	}
	m.replay()
	return m
}

// replay rebuilds the visible transcript from a resumed session, so picking a
// conversation back up shows what was said rather than an empty screen.
func (m *Model) replay() {
	var md markdown
	for _, msg := range m.sess.Messages {
		switch msg.Role {
		case provider.User:
			m.blank()
			m.push(entry{rule: true})
			// Split like openTurn does, so a resumed multi-line question looks
			// the same as it did when it was asked.
			for _, l := range strings.Split(msg.Content, "\n") {
				m.push(entry{
					kind:  kindUser,
					first: barStyle.Render("▌ "),
					cont:  barStyle.Render("▌ "),
					text:  l,
					style: &userStyle,
				})
			}
		case provider.Assistant:
			for _, tc := range msg.ToolCalls {
				m.pushKind(entry{
					kind:  kindWork,
					first: "  " + workStyle.Render("⏺ "),
					cont:  "      ",
					text:  workStyle.Render(tc.Function.Name) + workDim.Render("  "+summarize(tc.Function.Arguments, 40)),
				})
			}
			if t := strings.TrimSpace(msg.Content); t != "" {
				for _, l := range strings.Split(t, "\n") {
					e := md.entry(l)
					e.kind = kindReply
					m.pushKind(e)
				}
			}
		case provider.ToolRole:
			m.pushKind(entry{
				kind:  kindWork,
				first: "    " + workDim.Render("↳ "),
				cont:  "      ",
				text:  workDim.Render(resultSummary(msg.Content)),
			})
		}
	}
	if len(m.sess.Messages) > 0 {
		m.blank()
		m.push(entry{rule: true, stamp: "resumed " + m.sess.ID})
	}
}

func (m Model) Init() tea.Cmd {
	return textarea.Blink
}

// entry is one logical transcript line together with the prefixes used when it
// wraps. Keeping them apart from the text is what allows a hanging indent: a
// tool call that runs past the width continues under its own marker instead of
// starting again at column zero.
type entry struct {
	// first prefixes the opening row, cont every row after it.
	first, cont string
	text        string
	// block groups the lines that belong to one message, so clicking any line of
	// a reply selects the reply rather than the line.
	block int
	// rule makes this a turn separator drawn to the full width, optionally
	// labelled with stamp. It is re-rendered on resize rather than stored, so
	// the rule always spans the terminal.
	rule  bool
	stamp string
	// kind is what this line is, which decides the spacing around it. Lines of
	// the same kind sit together; a change of kind opens a blank line. See
	// pushKind.
	kind entryKind
	// style, when set, is applied to each row after wrapping, and text is held
	// unstyled. A style opened once at the front of the string does not survive
	// wrapping: every row after the first starts with cont, which ends in a
	// reset, so the colour would stop at the first wrap point. Anything with a
	// single style throughout — a question, a quoted reply — sets this;
	// mixed-style lines like a tool call style their own spans instead.
	style *lipgloss.Style
}

// entryKind classifies a transcript line by its role in the conversation. It
// exists so spacing is a property of the content rather than something every
// call site has to remember to add — the old code called blank() by hand in a
// dozen places, and the gaps disagreed with each other.
type entryKind uint8

const (
	kindNone   entryKind = iota // blanks, rules, and anything unclassified
	kindUser                    // what you typed
	kindReply                   // the model's prose
	kindWork                    // tool calls, results, delegated tasks
	kindNotice                  // switches, warnings, level-ups, approvals
)

// spacedApart reports whether a blank line belongs between two kinds. Speech
// and working-out are different registers and get air between them; consecutive
// lines of the same kind do not, so ten reads in a row stay one block.
func spacedApart(prev, next entryKind) bool {
	if prev == kindNone || next == kindNone || prev == next {
		return false
	}
	// A notice is a one-line aside — it is already set apart by its marker, and
	// giving it a blank line on both sides costs two rows to say very little.
	if next == kindNotice || prev == kindNotice {
		return false
	}
	return true
}

// rows renders the entry at a given width.
func (e entry) rows(width int) []string {
	if width <= 0 {
		width = 80
	}
	if e.rule {
		if e.stamp == "" {
			return []string{dimStyle.Render(strings.Repeat("─", max(4, width)))}
		}
		n := max(4, width-lipgloss.Width(e.stamp)-1)
		return []string{dimStyle.Render(strings.Repeat("─", n) + " " + e.stamp)}
	}
	w := max(10, width-lipgloss.Width(e.first))
	parts := strings.Split(ansi.Wrap(e.text, w, ""), "\n")
	out := make([]string, 0, len(parts))
	for i, r := range parts {
		// Style each row in its own right, so a message that wraps is coloured
		// all the way down rather than only as far as the first break.
		if e.style != nil {
			r = e.style.Render(r)
		}
		if i == 0 {
			out = append(out, e.first+r)
		} else {
			out = append(out, e.cont+r)
		}
	}
	return out
}

// push appends an entry. When the view is scrolled back it is nudged to keep
// showing the same content rather than jumping to the bottom under the reader.
// onClick picks the message under the pointer to reply to, the way a messaging
// app does. Clicking the same message again, or empty space, clears it.
func (m *Model) onClick(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	if mouse.Button != tea.MouseLeft {
		return *m, nil
	}
	i := m.entryAtRow(mouse.Y)
	if i < 0 {
		m.replyTo = ""
		return *m, nil
	}

	quote := m.blockText(m.entries[i].block)
	if quote == "" || quote == m.replyTo {
		// A second click on the same message means "never mind".
		m.replyTo = ""
		return *m, nil
	}
	m.replyTo = quote
	return *m, nil
}

// blockText gathers a whole message as plain text, styling removed: it is going
// to the model as a quote, and escape codes would only cost tokens.
func (m Model) blockText(block int) string {
	var lines []string
	for _, e := range m.entries {
		if e.block != block || e.rule {
			continue
		}
		lines = append(lines, strings.TrimRight(ansi.Strip(e.first+e.text), " "))
	}
	// Blank lines at either end carry nothing into a quote.
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// sub finds a running sub-agent by id, nil when it has finished or never was.
func (m Model) sub(id string) *subView {
	for _, s := range m.subs {
		if s.id == id {
			return s
		}
	}
	return nil
}

// watched is the sub-agent whose panel is open, nil when none is. Kept as an id
// rather than an index so a sibling finishing does not shift what is watched.
func (m Model) watched() *subView {
	if m.watching == "" {
		return nil
	}
	return m.sub(m.watching)
}

// dropSub removes a finished sub-agent, moving the watch to another one that is
// still running rather than closing the panel out from under the reader.
func (m *Model) dropSub(id string) {
	out := m.subs[:0]
	for _, s := range m.subs {
		if s.id != id {
			out = append(out, s)
		}
	}
	m.subs = out
	if m.watching == id {
		m.watching = ""
		if len(m.subs) > 0 {
			m.watching = m.subs[0].id
		}
	}
}

// newBlock starts a new message, so a click can select one whole.
func (m *Model) newBlock() { m.blockSeq++ }

func (m *Model) push(e entry) {
	e.block = m.blockSeq
	m.entries = append(m.entries, e)
	rows := e.rows(m.innerWidth())
	for range rows {
		m.owner = append(m.owner, len(m.entries)-1)
	}
	m.display = append(m.display, rows...)
	if m.scroll > 0 {
		m.scroll += len(rows)
	}
	if n := len(m.entries); n > maxLines {
		m.entries = append([]entry(nil), m.entries[n-maxLines:]...)
		m.rewrap()
	}
	m.clampScroll()
}

// add appends plain lines with no prefix.
func (m *Model) add(lines ...string) {
	for _, l := range lines {
		m.push(entry{text: l})
	}
}

// pushKind appends an entry, opening a blank line first when the kind changes.
// Everything that writes conversation goes through here, so the rhythm of the
// transcript is decided in one place rather than at each call site.
func (m *Model) pushKind(e entry) {
	if spacedApart(m.lastKind(), e.kind) {
		m.push(entry{})
	}
	m.push(e)
}

// lastKind is the kind of the last non-blank entry, so a blank line already
// present does not read as "unclassified" and suppress the spacing rule.
func (m Model) lastKind() entryKind {
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := m.entries[i]
		if e.rule {
			// A rule opens a turn and provides its own separation.
			return kindNone
		}
		if e.text == "" && e.first == "" {
			continue
		}
		return e.kind
	}
	return kindNone
}

// rewrap rebuilds the wrapped view of the transcript, after a resize or a trim.
func (m *Model) rewrap() {
	m.wrapWidth = m.innerWidth()
	m.display = m.display[:0]
	m.owner = m.owner[:0]
	for i, e := range m.entries {
		rows := e.rows(m.innerWidth())
		for range rows {
			m.owner = append(m.owner, i)
		}
		m.display = append(m.display, rows...)
	}
}

// content is what the transcript area shows: the conversation, followed by a
// single spinner line while a reasoning model is mid-thought. The thinking
// itself is never shown — only the tools it uses and the answer it reaches are
// worth the room — so this line is just a sign the model is still working.
func (m Model) content() []string {
	if m.think == "" {
		return m.display
	}
	rows := make([]string, 0, len(m.display)+1)
	rows = append(rows, m.display...)
	rows = append(rows, thinkStyle.Render("thinking…"))
	return rows
}

func (m *Model) clampScroll() {
	if maxScroll := max(0, len(m.content())-m.viewHeight()); m.scroll > maxScroll {
		m.scroll = maxScroll
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

// inputRows is how many rows the input actually renders as. It is measured
// from the rendered view rather than taken from Height(), so the layout can
// never disagree with what is drawn.
func (m Model) inputRows() int {
	return strings.Count(m.input.View(), "\n") + 1
}

// viewHeight is the transcript area, which yields rows as the input grows so
// that the input stays against the bottom of the screen.
// innerWidth is the width available to content, once the side gutters are
// taken out. Everything measures and wraps against this rather than the
// terminal width, so the gutters are never written into.
func (m Model) innerWidth() int {
	return max(20, m.width-padX*2)
}

func (m Model) viewHeight() int {
	h := m.height - chromeLines - m.inputRows()
	if m.pick != nil {
		h -= m.pick.height()
	}
	if m.keyAsk != nil {
		h -= m.keyAsk.height()
	}
	if w := m.watched(); w != nil {
		h -= w.height()
	}
	if m.sug != nil {
		h -= m.sug.height()
	}
	if m.replyTo != "" {
		h--
	}
	return max(1, h)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// SetWidth is the text width, excluding the prompt column, so the
		// prompt has to come out of it too — otherwise rendered rows overrun
		// the box and lipgloss wraps them a second time.
		m.input.SetWidth(max(20, m.innerWidth()-lipgloss.Width(prompt)-boxPadX*2-2))
		if m.innerWidth() != m.wrapWidth {
			m.rewrap()
		}
		m.clampScroll()
		return m, nil

	case tea.KeyPressMsg:
		next, cmd := m.onKey(msg)
		// The completion list follows whatever the keystroke did to the input,
		// rather than each key handler having to remember to update it.
		m = next.(Model)
		return m, tea.Batch(cmd, m.refreshSuggest())

	case filesMsg:
		m.files = msg.index
		m.scanning = false
		// The mention that asked for this has been growing while the tree was
		// read, so the list is built from what is in the input now.
		return m, m.refreshSuggest()

	case tea.MouseClickMsg:
		return m.onClick(msg.Mouse())

	case tea.MouseWheelMsg:
		// The alternate screen took the terminal's own scrolling, so the wheel
		// has to be handled here or it does nothing at all.
		switch msg.Mouse().Button {
		case tea.MouseWheelUp:
			m.scroll += 3
		case tea.MouseWheelDown:
			m.scroll -= 3
		}
		m.clampScroll()
		return m, nil

	case tickMsg:
		if !m.busy {
			return m, nil
		}
		m.frame++
		return m, tick()

	case eventMsg:
		return m.onEvent(msg.ev)

	case statusMsg:
		m.showProviders(msg.providers)
		return m, nil

	case modelsMsg:
		if m.pick != nil && m.pick.kind == pickModel {
			m.pick.err = msg.err
			m.pick.needsKey = msg.needsKey
			m.pick.setModels(msg.models)
		}
		return m, nil

	case branchesMsg:
		if m.pick != nil && m.pick.kind == pickBranch {
			m.pick.err = msg.err
			m.pick.setItems(msg.branches)
		}
		return m, nil

	case streamDoneMsg:
		m.busy = false
		m.cancel = nil
		m.events = nil
		// A sub-agent cannot outlive the turn that spawned it, so the panels
		// close here even if the turn ended badly.
		m.subs = nil
		m.watching = ""
		m.input.Focus()
		if q := m.queued; q != "" {
			m.queued = ""
			return m.send(q)
		}
		return m, textarea.Blink
	}

	// Anything not handled above goes to whatever currently owns the keyboard,
	// which is not always the main input. Bracketed paste is the case that
	// exposed this: it arrives as its own message rather than as key presses, so
	// routing everything to the input by default put a pasted API key into the
	// conversation instead of into the prompt asking for it.
	var cmd tea.Cmd
	switch {
	case m.keyAsk != nil:
		m.keyAsk.input, cmd = m.keyAsk.input.Update(msg)
		return m, cmd

	case m.pick != nil:
		// The chooser has no widget of its own, so a paste narrows the filter.
		if p, ok := msg.(tea.PasteMsg); ok {
			m.pick.filter += p.Content
			m.pick.apply()
		}
		return m, nil

	case m.ask != nil:
		// An approval takes y or n. Anything else, pasted included, is not an
		// answer and must not leak into the input behind it.
		return m, nil
	}

	m.input, cmd = m.input.Update(msg)
	// A paste can start a command or a mention just as typing can.
	return m, tea.Batch(cmd, m.refreshSuggest())
}

// refreshSuggest rebuilds the completion list from what is in the input. It is
// called after anything that could have changed it, so the list is derived
// state rather than something the key handlers have to maintain. The command
// it returns is a scan of the tree, when a mention needs one.
func (m *Model) refreshSuggest() tea.Cmd {
	v := m.input.Value()
	if v != m.lastInput {
		// Typing something new is a fresh question, so an earlier dismissal
		// stops applying.
		m.lastInput = v
		m.sugOff = false
	}
	if m.sugOff || m.pick != nil || m.keyAsk != nil || m.ask != nil {
		m.sug = nil
		return nil
	}
	prev := m.sug
	m.sug = suggestFor(v, m.files)
	// Keep the highlight on the same entry where it still exists, so narrowing
	// the list does not move the selection out from under the user.
	if prev != nil && m.sug != nil {
		if sel, ok := prev.selected(); ok {
			for i, it := range m.sug.items {
				if it.insert == sel.insert {
					m.sug.cursor = i
					break
				}
			}
		}
	}
	return m.scan()
}

// scan asks for a fresh snapshot of the tree when a mention needs one: the
// first time one is typed, and when a new mention starts against a snapshot
// old enough to have missed something. Not mid-word — a list that reorders
// under the cursor because a build wrote a file is worse than a stale one.
func (m *Model) scan() tea.Cmd {
	if m.sug == nil || m.sug.kind != sugFile || m.scanning {
		return nil
	}
	if m.files != nil && !(m.sug.token == mention && m.files.stale()) {
		return nil
	}
	m.scanning = true
	return scanFiles(m.root)
}

// acceptSuggest replaces the token being completed with the highlighted entry.
// Only the last word of the input is ever rewritten, which is where the token
// came from.
func (m *Model) acceptSuggest() (tea.Model, tea.Cmd) {
	it, ok := m.sug.selected()
	if !ok {
		return *m, nil
	}
	text := strings.TrimSuffix(m.input.Value(), m.sug.token) + it.insert
	if it.space {
		text += " "
	}
	m.input.SetValue(text)
	m.input.CursorEnd()
	// A finished completion closes the list. One that carries on — a directory
	// to step into — leaves it open, and the rebuild below fills it with what
	// is inside.
	m.lastInput, m.sugOff, m.sug = text, it.space, nil
	return *m, m.refreshSuggest()
}

func (m *Model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// The key prompt takes the keyboard while it is open.
	if m.keyAsk != nil {
		switch msg.String() {
		case "esc", "ctrl+c":
			m.keyAsk = nil
			return *m, nil
		case "enter":
			return m.saveKey()
		}
		var cmd tea.Cmd
		m.keyAsk.input, cmd = m.keyAsk.input.Update(msg)
		return *m, cmd
	}

	// The model chooser takes the keyboard while it is open.
	if m.pick != nil {
		switch msg.String() {
		case "esc", "ctrl+c":
			m.pick = nil
		case "up", "ctrl+p":
			m.pick.move(-1)
		case "down", "ctrl+n":
			m.pick.move(1)
		case "ctrl+f":
			// Pin or unpin what is highlighted without leaving the list, so a
			// handful of models can be marked in one pass.
			if m.pick.kind != pickModel {
				return *m, nil
			}
			if ref := m.pick.selected(); ref != "" {
				if err := m.cfg.ToggleFavourite(ref); err != nil {
					m.add(errStyle.Render("✗ could not update favourites: " + err.Error()))
				} else {
					// Re-float so a freshly pinned model jumps to the top and an
					// unpinned one drops back into the alphabetical list.
					m.pick.all = sortFavourites(m.pick.all, m.cfg.Favourites)
					m.pick.apply()
				}
			}
			return *m, nil
		case "enter":
			if m.pick.kind == pickBranch {
				sel := m.pick.selected()
				m.pick = nil
				if sel == "" {
					return *m, nil
				}
				if name, ok := strings.CutPrefix(sel, newBranchPrefix); ok {
					return m.switchBranch(name, true)
				}
				return m.switchBranch(sel, false)
			}
			if ref := m.pick.selected(); ref != "" {
				m.pick = nil
				// The entry that offers to add a key, for a provider whose
				// catalogue could not be listed without one.
				if name, ok := strings.CutPrefix(ref, addKeyPrefix); ok {
					p := m.cfg.Providers[name]
					k := newKeyPrompt(name, p.APIKeyEnv, "")
					k.reopen = true
					m.keyAsk = k
					return *m, nil
				}
				// A model whose provider has no key would fail with a 401 a few
				// seconds from now. Ask while the intent is still fresh.
				if name, env, needs := m.needsKey(ref); needs {
					m.keyAsk = newKeyPrompt(name, env, ref)
					return *m, nil
				}
				return m.switchModel(ref)
			}
			m.pick = nil
		case "backspace":
			if n := len(m.pick.filter); n > 0 {
				m.pick.filter = m.pick.filter[:n-1]
				m.pick.apply()
			}
		case "space":
			// Reported by name rather than as a rune, so it needs saying.
			m.pick.filter += " "
			m.pick.apply()
		default:
			// Anything printable narrows the list.
			if r := []rune(msg.String()); len(r) == 1 {
				m.pick.filter += string(r)
				m.pick.apply()
			}
		}
		return *m, nil
	}

	// An approval blocks the agent, so it takes the keyboard until answered.
	if m.ask != nil {
		switch msg.String() {
		case "y", "Y", "enter":
			m.answer(true)
		case "n", "N", "esc", "ctrl+c":
			m.answer(false)
		}
		return *m, nil
	}

	// The completion list borrows a few keys while it is open — the ones that
	// do nothing useful on a single-word line — and leaves the rest to the
	// input, so typing never stops working.
	if m.sug != nil {
		switch msg.String() {
		case "up", "ctrl+p":
			m.sug.move(-1)
			return *m, nil
		case "down", "ctrl+n":
			m.sug.move(1)
			return *m, nil
		case "tab":
			return m.acceptSuggest()
		case "esc":
			// Cancelling the turn is the more urgent meaning of esc; the list
			// is only in the way when nothing is running.
			if !m.busy {
				m.sugOff = true
				return *m, nil
			}
		case "enter":
			// A half-typed name completes rather than runs: "/mod" is not a
			// command, and sending it would only earn an "unknown command".
			// A name typed out in full is meant, so it falls through and runs.
			//
			// A mention is the opposite case. It sits inside a message that is
			// otherwise finished, and taking enter to mean "complete this
			// path" would leave no way to send a message that ends in one.
			// Tab completes those, as it does everywhere else.
			if m.sug.kind == sugCommand && !isCommand(m.input.Value()) {
				return m.acceptSuggest()
			}
		}
	}

	switch msg.String() {
	case "tab":
		m.ag.SetMode(m.ag.Mode().Next())
		return *m, nil

	case "ctrl+c":
		if m.busy {
			// First interrupt cancels the turn; it does not exit. Quitting
			// mid-tool-call would leave the transcript inconsistent.
			m.cancel()
			return *m, nil
		}
		m.quit = true
		return *m, tea.Quit

	case "esc":
		// Stopping the agent comes first. A pending reply also clears on esc,
		// but if a turn is running that is not what the key is for — and having
		// clicked a message mid-turn, the first esc appeared to do nothing.
		if m.busy {
			m.cancel()
			return *m, nil
		}
		m.replyTo = ""
		return *m, nil

	case "ctrl+d":
		if !m.busy && m.input.Value() == "" {
			m.quit = true
			return *m, tea.Quit
		}

	case "ctrl+o":
		// Open or close the sub-agent panel. Doing nothing when none is running
		// is deliberate: the key means "show me the sub-agent", and inventing
		// something for it to do when there isn't one would only surprise.
		// With several running, the key steps through them and then closes:
		// watch the first, the second, … and the press after the last puts the
		// panel away. One key covers both "look" and "look at the other one".
		switch {
		case len(m.subs) == 0:
			// Nothing running. Inventing something for the key to do here would
			// only surprise.
		case m.watching == "":
			m.watching = m.subs[0].id
		default:
			i := 0
			for j, s := range m.subs {
				if s.id == m.watching {
					i = j
					break
				}
			}
			if i+1 < len(m.subs) {
				m.watching = m.subs[i+1].id
			} else {
				m.watching = ""
			}
		}
		// The panel takes its rows from the transcript, so what is visible
		// changes under the reader; keep the newest end in view.
		m.clampScroll()
		return *m, nil

	// The terminal's own scrollback cannot see the transcript on the alternate
	// screen, so scrolling is handled here.
	case "pgup":
		m.scroll += m.viewHeight() / 2
		m.clampScroll()
		return *m, nil

	case "pgdown":
		m.scroll -= m.viewHeight() / 2
		m.clampScroll()
		return *m, nil

	case "shift+up":
		m.scroll++
		m.clampScroll()
		return *m, nil

	case "shift+down":
		m.scroll--
		m.clampScroll()
		return *m, nil

	case "enter":
		if m.busy {
			// The model is mid-turn, blocked on a tool result it asked for, so
			// it cannot take this now. Hold it and send it the moment the turn
			// ends, rather than dropping the keystroke on the floor.
			if q := strings.TrimSpace(m.input.Value()); q != "" {
				m.queued = q
				m.input.Reset()
			}
			return *m, nil
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return *m, nil
		}
		m.input.Reset()
		if strings.HasPrefix(text, "/") {
			return m.command(text)
		}
		return m.send(text)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return *m, cmd
}

// needsKey reports whether a reference's provider wants a key it does not have.
func (m Model) needsKey(ref string) (name, env string, needs bool) {
	p, _, err := m.cfg.Resolve(ref)
	if err != nil {
		return "", "", false
	}
	name, _, _ = strings.Cut(ref, "/")
	if p.APIKeyEnv != "" && p.Key() == "" {
		return name, p.APIKeyEnv, true
	}
	return name, p.APIKeyEnv, false
}

// saveKey stores what was typed and carries on to whatever wanted it.
func (m *Model) saveKey() (tea.Model, tea.Cmd) {
	key := strings.TrimSpace(m.keyAsk.input.Value())
	if key == "" {
		m.keyAsk.err = "nothing entered"
		return *m, nil
	}
	if err := m.cfg.SetKey(m.keyAsk.provider, key); err != nil {
		m.keyAsk.err = err.Error()
		return *m, nil
	}

	name, then, reopen := m.keyAsk.provider, m.keyAsk.then, m.keyAsk.reopen
	m.keyAsk = nil
	m.add(okStyle.Render("✓ saved a key for "+name) +
		dimStyle.Render("  in "+shortRoot(config.Path())))
	if then != "" {
		return m.switchModel(then)
	}
	if reopen {
		// The key was added to get at those models, so go back to the list —
		// refetched, since the catalogue should now answer.
		m.pick = &picker{loading: true, cfg: m.cfg}
		return *m, fetchModels(m.cfg)
	}
	return *m, nil
}

// switchModel points the agent at a different model, keeping the conversation.
func (m *Model) switchModel(ref string) (tea.Model, tea.Cmd) {
	p, model, err := m.cfg.Resolve(ref)
	if err != nil {
		m.add(errStyle.Render("✗ " + err.Error()))
		return *m, nil
	}
	m.ag.SetClient(provider.New(p.BaseURL, p.Key(), model))
	m.ag.SetContext(m.cfg.ContextFor(ref))
	m.ag.SetRef(ref)
	m.ref = ref
	m.chosen = ref
	m.add(okStyle.Render("✓ switched to " + ref))

	// Remembered for next time. Choosing a model is a decision about how you
	// work, so the config — the one place that says which model you use — is
	// where it belongs, rather than in a hidden state file. The -m flag stays a
	// one-off override precisely because it does not come through here.
	if m.cfg.Default != ref {
		m.cfg.Default = ref
		if err := m.cfg.Save(); err != nil {
			m.add(errStyle.Render("✗ could not remember the model: " + err.Error()))
		}
	}
	return *m, nil
}

// switchBranch checks out another branch of the working directory, creating it
// first when create is set.
//
// The conversation is kept: the files under discussion are the same files, and
// throwing away the context of a switch would be a strange way to reward one.
// What the agent must not do is carry on believing the old branch is checked
// out, so the switch is announced into the transcript, where it becomes part
// of the history the model reads.
func (m *Model) switchBranch(name string, create bool) (tea.Model, tea.Cmd) {
	if m.branch == "" {
		m.add(errStyle.Render("✗ not a git repository"))
		return *m, nil
	}
	if name == m.branch && !create {
		m.add(dimStyle.Render("already on " + name))
		return *m, nil
	}
	if err := vcs.Checkout(m.root, name, create); err != nil {
		// git's own words: "Your local changes would be overwritten…" says
		// exactly what to do next, and nothing here could say it better.
		m.add(errStyle.Render("✗ " + err.Error()))
		return *m, nil
	}

	from := m.branch
	m.branch = vcs.Branch(m.root)
	if create {
		m.add(okStyle.Render("✓ created "+m.branch) + dimStyle.Render("  from "+from))
	} else {
		m.add(okStyle.Render("✓ switched to "+m.branch) + dimStyle.Render("  from "+from))
	}
	// Told to the model as well as the user. A branch switch changes the files
	// underneath it, so anything it read a moment ago may no longer be true.
	m.ag.Note(fmt.Sprintf("The user switched the working directory from branch %q to branch %q. "+
		"Files may have changed on disk; re-read anything you are relying on.", from, m.branch))
	return *m, nil
}

// persist writes the conversation out. A failure to save is reported inline
// rather than returned: losing a session is worth telling the user about, but
// not worth interrupting the conversation for.
func (m *Model) persist() {
	m.sess.Messages = m.ag.Messages()
	// Recorded so resuming picks up where it left off. The chosen model, not
	// the active one: a session that happened to end on an escalated model
	// should not reopen on it.
	m.sess.Model = m.chosen
	if err := m.sess.Save(); err != nil {
		m.add(errStyle.Render("✗ could not save session: " + err.Error()))
	}
	// Keep the advertised title current, so the picker shows what this
	// instance is about rather than "(new session)".
	m.sess.UpdateTitle()
	// Saved alongside the session: the companion outlives it, so losing a level
	// to a crash would be a shame.
	if err := m.comp.Save(); err != nil {
		m.add(errStyle.Render("✗ could not save the companion: " + err.Error()))
	}
}

// blank pushes a separating line, but only when the transcript does not
// already end in one. Callers can ask for space without having to know what
// came before, which is what keeps double gaps out of the transcript.
func (m *Model) blank() {
	if len(m.entries) == 0 {
		return
	}
	if last := m.entries[len(m.entries)-1]; last.text == "" && !last.rule && last.first == "" {
		return
	}
	m.push(entry{})
}

// openTurn marks the start of an exchange: a dim rule carrying the time, then
// the question against a coloured bar. The rule is the main thing that makes a
// long conversation scannable — it is where the eye lands when scrolling back.
func (m *Model) openTurn(text, quote string) {
	m.blank()
	m.newBlock()
	m.push(entry{rule: true, stamp: time.Now().Format("15:04")})
	// What is being replied to, shown the way a messaging app shows it: above
	// the message, quieter than it, and clearly not something you typed.
	for _, l := range strings.Split(quote, "\n") {
		if quote == "" {
			break
		}
		m.push(entry{
			first: dimStyle.Render("│ "),
			cont:  dimStyle.Render("│ "),
			text:  l,
			style: &quoteStyle,
		})
	}
	// A question can be several lines, either because it was typed with
	// shift+enter or because it is long enough to wrap. Either way it is one
	// message: every line gets the bar and the colour.
	for _, l := range strings.Split(text, "\n") {
		m.push(entry{
			kind:  kindUser,
			first: barStyle.Render("▌ "),
			cont:  barStyle.Render("▌ "),
			text:  l,
			style: &userStyle,
		})
	}
	// The gap under the question is opened by whatever comes next, via
	// pushKind — a blank line here would double it.
}

// answer releases the blocked agent with the user's decision.
func (m *Model) answer(ok bool) {
	if m.ask == nil {
		return
	}
	if ok {
		m.add(okStyle.Render("✓ approved " + m.ask.Name))
	} else {
		m.add(errStyle.Render("✗ declined " + m.ask.Name))
	}
	m.ask.Reply <- ok
	m.ask = nil
}

// send starts a turn and begins pumping events from the agent.
func (m *Model) send(text string) (tea.Model, tea.Cmd) {
	// A reply carries what it is replying to, so the model does not have to
	// guess which part of a long answer is meant. Markdown quoting is what it
	// already understands. The quote is kept out of the echoed message so the
	// transcript shows it as a quote rather than as something you typed.
	quote := m.replyTo
	m.replyTo = ""
	sent := text
	if quote != "" {
		var q strings.Builder
		for _, l := range strings.Split(quote, "\n") {
			q.WriteString("> " + l + "\n")
		}
		q.WriteString("\n" + text)
		sent = q.String()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.busy = true
	m.frame = 0
	m.pending = ""
	m.think = ""
	m.md = markdown{}
	m.inText = false
	m.warnedFull = false
	m.rejected = false
	m.adopt = ""
	m.events = make(chan agent.Event, 64)
	// Sending jumps back to the newest output: the reply is what you want to
	// see, wherever you had scrolled to.
	m.scroll = 0
	// The input stays focused while the model works so the next message can be
	// composed in the meantime. Enter is ignored until the turn finishes, and
	// the typed text simply stays in the box.
	go m.ag.Run(ctx, sent, m.events)

	m.openTurn(text, quote)
	return *m, tea.Batch(waitFor(m.events), tick())
}

func (m *Model) onEvent(ev agent.Event) (tea.Model, tea.Cmd) {
	next := waitFor(m.events)

	switch e := ev.(type) {

	case agent.TextDelta:
		// Once the real answer starts, the thinking has served its purpose.
		m.think = ""
		m.pending += e.Text
		var lines []string
		lines, m.pending = splitLines(m.pending)
		m.addText(lines)
		return *m, next

	case agent.ReasoningDelta:
		m.think = m.think + e.Text
		return *m, next

	case agent.ToolStart:
		if e.Depth == 0 {
			m.comp.Tools++
		}
		m.newBlock()
		// Delegation announces itself through TaskStart, which opens the panel.
		// Reporting the call as an ordinary tool as well says it twice.
		if e.Name == "task" && e.Depth == 0 {
			return *m, next
		}
		if s := m.sub(e.Task); s != nil {
			s.steps++
			s.add(toolStyle.Render("⏺ "+e.Name) +
				dimStyle.Render("  "+summarize(e.Args, max(10, m.innerWidth()-len(e.Name)-14))))
			return *m, next
		}
		m.think = ""
		m.flush()
		m.inText = false
		pad := strings.Repeat("  ", 1+e.Depth)
		m.pushKind(entry{
			kind:  kindWork,
			first: pad + workStyle.Render("⏺ "),
			cont:  pad + "    ",
			text:  workStyle.Render(e.Name) + workDim.Render("  "+summarize(e.Args, max(20, m.innerWidth()-len(e.Name)-10))),
		})
		return *m, next

	case agent.ToolEnd:
		// Likewise: TaskEnd reports what came back.
		if e.Name == "task" && e.Depth == 0 {
			return *m, next
		}
		text, style := resultSummary(e.Result), workDim
		if e.Err != nil {
			text, style = e.Err.Error(), workErr
		}
		if s := m.sub(e.Task); s != nil {
			s.add("  " + workDim.Render("↳ ") + style.Render(text))
			return *m, next
		}
		pad := strings.Repeat("  ", 2+e.Depth)
		m.pushKind(entry{
			kind:  kindWork,
			first: pad + workDim.Render("↳ "),
			cont:  pad + "  ",
			text:  style.Render(text),
		})
		return *m, next

	case agent.TaskStart:
		m.comp.Tasks++
		m.think = ""
		m.flush()
		m.inText = false
		// The panel opens here and takes the sub-agent's steps from now on.
		m.subs = append(m.subs, &subView{id: e.ID, desc: e.Description})
		m.pushKind(entry{
			kind:  kindWork,
			first: "  " + taskStyle.Render("◆ "),
			cont:  "    ",
			text:  taskStyle.Render("task") + workDim.Render("  "+e.Description),
		})
		return *m, next

	case agent.TaskEnd:
		// The panel collapses, leaving one line in the transcript. What the
		// sub-agent did was working-out, not conversation.
		m.dropSub(e.ID)
		text, style := fmt.Sprintf("returned %d chars after %d steps", len(e.Summary), e.Steps), workDim
		if e.Err != nil {
			text, style = e.Err.Error(), workErr
		}
		m.pushKind(entry{
			kind:  kindWork,
			first: "    " + workDim.Render("↳ "),
			cont:  "      ",
			text:  style.Render(text),
		})
		return *m, next

	case agent.Usage:
		m.ctxTokens = e.Total
		// Context is the one thing every provider charges in, so it is what the
		// companion grows on — whichever model happened to serve this request.
		if before, after := m.comp.Feed(m.ref, int64(e.Total)); after > before {
			m.pushKind(entry{
				kind:  kindNotice,
				first: "  " + levelStyle.Render("★ "),
				cont:  "    ",
				text: levelStyle.Render(fmt.Sprintf("level %d — %s", after, m.comp.Title())) +
					dimStyle.Render("  /companion"),
			})
		}
		// Warn before the window overflows rather than after: once the server
		// starts truncating, the model loses the question it was asked and
		// begins answering something else entirely.
		if m.contextNearlyFull() && !m.warnedFull {
			m.warnedFull = true
			m.add(errStyle.Render(fmt.Sprintf(
				"⚠ context %d%% full — replies will degrade. /compact to summarise it, /clear to start over.",
				m.ctxTokens*100/m.contextLimit())))
		}
		return *m, next

	case agent.Approval:
		m.think = ""
		m.flush()
		m.inText = false
		m.ask = &e
		return *m, next

	case agent.ModeChanged:
		return *m, next

	case agent.Switched:
		m.ref = e.To
		m.warnedFull = false
		// A model that refused outright will refuse again next session, so
		// leaving it as the default means starting every session with the same
		// failure. The replacement is adopted only once it has actually
		// answered, though — adopting it here churned the default through a
		// whole ladder of models that were failing too.
		if m.rejected && e.From == m.chosen {
			m.adopt = e.To
		}
		m.push(entry{
			first: "  " + okStyle.Render("⇅ "),
			cont:  "    ",
			text: okStyle.Render("switched to "+e.To) +
				dimStyle.Render("  — "+e.Reason),
		})
		return *m, next

	case agent.Rejected:
		if e.Ref == m.chosen {
			m.rejected = true
		}
		m.push(entry{
			first: "  " + errStyle.Render("✗ "),
			cont:  "    ",
			text: errStyle.Render(e.Ref+" refused") +
				dimStyle.Render("  — "+e.Reason),
		})
		return *m, next

	case agent.Retrying:
		m.push(entry{
			first: "  " + dimStyle.Render("↻ "),
			cont:  "    ",
			text: dimStyle.Render(fmt.Sprintf("retrying in %s (attempt %d)",
				e.After.Round(time.Millisecond*100), e.Attempt)) +
				dimStyle.Render("  — "+ansi.Truncate(e.Reason, max(20, m.innerWidth()-40), "…")),
		})
		return *m, next

	case agent.Tripped:
		m.push(entry{
			first: "  " + askStyle.Render("⚠ "),
			cont:  "    ",
			text: askStyle.Render(e.Provider+" taken out of rotation") +
				dimStyle.Render(fmt.Sprintf("  — %s, retrying in %s", e.Reason, e.For)),
		})
		return *m, next

	case agent.Trimmed:
		m.add(askStyle.Render(fmt.Sprintf("⋯ dropped %d earlier messages", e.Messages)) +
			dimStyle.Render("  — no roomier model to switch to, so the agent may"+
				" repeat work it has already done"))
		return *m, next

	case agent.Compacted:
		// The count the user cares about is the room won back, so it leads.
		saved := 0
		if e.Before > 0 {
			saved = (e.Before - e.After) * 100 / e.Before
		}
		how := "compacted"
		if e.Auto {
			how = "context was full, so the conversation was compacted"
		}
		m.blank()
		m.add(okStyle.Render(fmt.Sprintf("⋯ %s — %d messages into a summary, %d%% smaller",
			how, e.Replaced, saved)))
		m.add(dimStyle.Render(fmt.Sprintf("  %s → %s tokens, keeping the last %d messages in full",
			humanTokens(e.Before), humanTokens(e.After), e.Kept)))
		// The estimate is what the agent works from; the real count arrives
		// with the next reply. Showing the estimate now stops the bar reading
		// full against a conversation that is no longer there.
		m.ctxTokens = e.After
		m.warnedFull = false
		m.persist()
		return *m, next

	case agent.CompactFailed:
		// Not a failed turn. Mid-turn the loop carries on and trims instead,
		// which Trimmed reports on its own; on demand the conversation is
		// simply unchanged. Either way the reason already reads as a sentence,
		// so it is shown as one rather than wrapped in another.
		why := e.Err.Error()
		if errors.Is(e.Err, context.Canceled) {
			why = "compacting was cancelled"
		}
		m.add(askStyle.Render("⋯ " + why))
		return *m, next

	case agent.TurnEnd:
		m.think = ""
		m.comp.Turns++
		// The turn finished, so whatever produced it works. Now it is worth
		// remembering in place of the model that refused.
		if m.adopt != "" {
			from, to := m.chosen, m.adopt
			m.adopt, m.rejected, m.chosen = "", false, to
			if m.cfg.Default != to {
				m.cfg.Default = to
				if err := m.cfg.Save(); err != nil {
					m.add(errStyle.Render("✗ could not remember the model: " + err.Error()))
				} else {
					m.add(dimStyle.Render("remembering " + to + " — " + from + " refused"))
				}
			}
		}
		// Cheap, and catches a branch the agent itself switched.
		m.branch = vcs.Branch(m.root)
		m.flush()
		m.persist()
		// Reasoning models sometimes finish a turn having written only
		// thinking, leaving nothing to show. Say so rather than returning to
		// the prompt as if nothing happened.
		if !m.inText {
			m.blank()
			// An empty turn is usually the window overflowing: the server drops
			// the oldest messages, the model loses the thread, and it returns
			// nothing. Say so, because "(no reply)" on its own looks like a bug
			// in the tool rather than a limit being hit.
			if m.contextNearlyFull() {
				m.add(errStyle.Render(
					"(no reply — the context is full. /compact to summarise it, /clear to start over.)"))
			} else {
				m.add(dimStyle.Render("(no reply)"))
			}
		}
		return *m, next

	case agent.Failed:
		m.think = ""
		m.flush()
		msg := e.Err.Error()
		if errors.Is(e.Err, context.Canceled) {
			msg = "cancelled"
		}
		m.blank()
		m.add(errStyle.Render("✗ " + msg))
		m.persist()
		return *m, next
	}

	return *m, next
}

// addText styles assistant lines as markdown and appends them, opening the
// block with a blank line so prose is set off from the tool calls above it.
func (m *Model) addText(lines []string) {
	if len(lines) == 0 {
		return
	}
	if !m.inText {
		m.inText = true
		// The reply is its own message, so a click anywhere in it quotes all
		// of it rather than the one line under the pointer.
		m.newBlock()
	}
	for _, l := range lines {
		e := m.md.entry(l)
		e.kind = kindReply
		// Only the first line can need a gap opening before it; once inside a
		// reply, consecutive lines are the same kind and stay together.
		m.pushKind(e)
	}
}

// flush turns any buffered text into transcript lines, including a final line
// with no trailing newline.
func (m *Model) flush() {
	lines, rest := splitLines(m.pending)
	if strings.TrimSpace(rest) != "" {
		lines = append(lines, rest)
	}
	m.pending = ""
	m.addText(lines)
}

func (m *Model) command(line string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(line)
	switch fields[0] {

	case "/quit", "/exit", "/q":
		m.quit = true
		return *m, tea.Quit

	case "/clear", "/new":
		// The old conversation stays on disk; this starts a fresh one rather
		// than overwriting it.
		m.ag.Reset()
		m.sess = session.New(m.root, m.ref)
		m.entries = nil
		m.display = nil
		m.scroll = 0
		m.ctxTokens = 0
		m.warnedFull = false
		return *m, nil

	case "/sessions":
		list, err := session.List("", 20)
		if err != nil {
			m.add(errStyle.Render("✗ " + err.Error()))
			return *m, nil
		}
		if len(list) == 0 {
			m.add(dimStyle.Render("no saved sessions"))
			return *m, nil
		}
		for _, x := range list {
			mark := " "
			if x.ID == m.sess.ID {
				mark = "*"
			} else if x.Root == m.root {
				mark = "·"
			}
			m.add(dimStyle.Render(fmt.Sprintf("%s %-22s %-9s %2d turns  ", mark, x.ID, x.Age(), x.Turns())) +
				x.Title)
		}
		m.add(dimStyle.Render("  /resume <id> to pick one up"))
		return *m, nil

	case "/resume":
		if len(fields) < 2 {
			m.add(errStyle.Render("✗ /resume needs a session id — see /sessions"))
			return *m, nil
		}
		x, err := session.Load(fields[1])
		if err != nil {
			m.add(errStyle.Render("✗ " + err.Error()))
			return *m, nil
		}
		m.sess = x
		m.ag.Restore(x.Messages)
		m.entries = nil
		m.display = nil
		m.scroll = 0
		m.ctxTokens = 0
		m.warnedFull = false
		m.replay()
		return *m, nil

	case "/model":
		if len(fields) < 2 {
			// No argument: offer what the endpoints actually serve.
			m.pick = &picker{loading: true, cfg: m.cfg}
			return *m, fetchModels(m.cfg)
		}
		return m.switchModel(fields[1])

	case "/branch", "/br":
		if m.busy {
			// The agent is mid-turn with tools running against these files.
			// Moving the working tree underneath it would have it read one
			// branch and write to another, and the note appended on the way
			// races with the turn appending to the same transcript.
			m.add(errStyle.Render("✗ /branch has to wait for the turn to finish"))
			return *m, nil
		}
		if m.branch == "" {
			m.add(errStyle.Render("✗ not a git repository"))
			return *m, nil
		}
		if len(fields) < 2 {
			// No argument: offer what the repository has.
			m.pick = &picker{kind: pickBranch, loading: true, cfg: m.cfg}
			return *m, fetchBranches(m.root)
		}
		// "-b name" creates, mirroring git, so the habit carries over.
		if fields[1] == "-b" || fields[1] == "-c" {
			if len(fields) < 3 {
				m.add(errStyle.Render("✗ /branch -b needs a name"))
				return *m, nil
			}
			return m.switchBranch(fields[2], true)
		}
		return m.switchBranch(fields[1], false)

	case "/favourite", "/fav":
		ref := m.ref
		if len(fields) >= 2 {
			// A named reference must resolve before it is worth pinning; an
			// unresolvable one would silently do nothing useful in /model.
			if _, _, err := m.cfg.Resolve(fields[1]); err != nil {
				m.add(errStyle.Render("✗ " + err.Error()))
				return *m, nil
			}
			ref = fields[1]
		}
		if m.cfg.IsFavourite(ref) {
			if err := m.cfg.ToggleFavourite(ref); err != nil {
				m.add(errStyle.Render("✗ could not update favourites: " + err.Error()))
				return *m, nil
			}
			m.add(dimStyle.Render("✗ removed " + ref + " from favourites"))
			return *m, nil
		}
		if err := m.cfg.ToggleFavourite(ref); err != nil {
			m.add(errStyle.Render("✗ could not update favourites: " + err.Error()))
			return *m, nil
		}
		m.add(okStyle.Render("★ pinned " + ref + " to favourites"))
		return *m, nil

	case "/key":
		if len(fields) < 2 {
			m.add(errStyle.Render("✗ /key needs a provider — see /providers"))
			return *m, nil
		}
		name := fields[1]
		p, ok := m.cfg.Providers[name]
		if !ok {
			m.add(errStyle.Render("✗ unknown provider: " + name))
			return *m, nil
		}
		m.keyAsk = newKeyPrompt(name, p.APIKeyEnv, "")
		return *m, nil

	case "/compact":
		if m.busy {
			m.add(errStyle.Render("✗ /compact has to wait for the turn to finish"))
			return *m, nil
		}
		// Driven over the same event channel as a turn, so it gets the spinner,
		// esc to cancel and the busy state without any machinery of its own.
		focus := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		ctx, cancel := context.WithCancel(context.Background())
		m.cancel = cancel
		m.busy = true
		m.frame = 0
		m.events = make(chan agent.Event, 8)
		m.scroll = 0
		go m.ag.RunCompact(ctx, focus, m.events)

		m.blank()
		note := "⋯ compacting the conversation…"
		if focus != "" {
			// Quoted rather than folded into the sentence: it is whatever the
			// user typed, and no phrasing reads well around all of it.
			note = fmt.Sprintf("⋯ compacting the conversation, keeping %q in view…", focus)
		}
		m.add(askStyle.Render(note))
		return *m, tea.Batch(waitFor(m.events), tick())

	case "/companion", "/comp":
		for _, e := range m.companionRows() {
			m.push(e)
		}
		return *m, nil

	case "/status":
		m.status()
		return *m, probeProviders(m.cfg)

	case "/providers":
		for name, p := range m.cfg.Providers {
			m.add(dimStyle.Render(fmt.Sprintf("  %-12s %s", name, p.BaseURL)))
		}
		return *m, nil

	case "/mcp":
		return *m, m.showMCP()

	case "/help":
		// Printed from the same table the completion list offers, so what is
		// documented and what can be typed cannot disagree.
		const col = 25
		for _, c := range commands {
			line := "  " + padTo(c.label(), col) + c.help
			if len(c.aliases) > 0 {
				line += "  (" + strings.Join(c.aliases, ", ") + ")"
			}
			m.add(dimStyle.Render(line))
		}
		for _, l := range keyHelp {
			m.add(dimStyle.Render("  " + l))
		}
		return *m, nil
	}

	m.add(errStyle.Render("✗ unknown command: " + fields[0]))
	return *m, nil
}

// View lays out the whole screen: transcript, status row, bar, input. Every
// section is a fixed height, so the input is always on the bottom row.
func (m Model) View() tea.View {
	v := tea.NewView("")
	// Left nil deliberately: the terminal keeps its own background, so the
	// alternate screen stays transparent.
	v.BackgroundColor = nil
	// The whole screen is ours, which is the only way to hold the input still.
	v.AltScreen = true
	// Clicks pick a message to reply to and the wheel scrolls the transcript.
	// The cost is that the terminal's own click-drag selection goes to the
	// program instead, which on the alternate screen was already limited.
	v.MouseMode = tea.MouseModeCellMotion

	if m.quit {
		return v
	}

	rows := make([]string, 0, m.height)
	rows = append(rows, m.transcript()...)
	if w := m.watched(); w != nil {
		f := spinnerFrames[m.frame%len(spinnerFrames)]
		i := 0
		for j, s := range m.subs {
			if s.id == w.id {
				i = j
				break
			}
		}
		rows = append(rows, strings.Split(w.render(m.innerWidth(), f, i, len(m.subs)), "\n")...)
	}
	if m.pick != nil {
		// What counts as "current" depends on what is being chosen: the dot
		// marks the model in use, or the branch checked out.
		current := m.ref
		if m.pick.kind == pickBranch {
			current = m.branch
		}
		rows = append(rows, strings.Split(m.pick.render(m.innerWidth(), current), "\n")...)
	}
	if m.keyAsk != nil {
		rows = append(rows, strings.Split(m.keyAsk.render(m.innerWidth()), "\n")...)
	}

	// The status row is always present, blank when idle, so the layout below
	// the transcript never changes height.
	status := ""
	if m.ask != nil {
		status = askStyle.Render("? run "+m.ask.Name) +
			dimStyle.Render(" "+summarize(m.ask.Args, max(10, m.innerWidth()-40))) +
			askStyle.Render("   y") + dimStyle.Render(" approve  ") +
			askStyle.Render("n") + dimStyle.Render(" decline")
	} else if len(m.subs) > 0 && m.watching == "" {
		// Sub-agents are running with the panel closed. This says so on the row
		// that is already there, rather than taking rows from the transcript to
		// show working-out nobody asked to see.
		f := spinnerFrames[m.frame%len(spinnerFrames)]
		status = subsHint(m.subs, f, m.innerWidth())
	} else if m.busy {
		f := spinnerFrames[m.frame%len(spinnerFrames)]
		// The model is mid-turn. Its thinking is not shown, so this only
		// reports that it is working and what to press to stop.
		tail := "  esc to cancel"
		if m.queued != "" {
			tail = "  ⏎ 1 queued  ·  esc to cancel"
		}
		status = spinStyle.Render(f) + " " +
			dimStyle.Render(ansi.Truncate("working", max(10, m.innerWidth()-len(tail)-6), "…")) +
			dimStyle.Render(tail)
	} else if m.queued != "" {
		status = dimStyle.Render("⏎ queued: " + ansi.Truncate(m.queued, max(10, m.innerWidth()-24), "…"))
	} else if m.scroll > 0 {
		unit := "lines"
		if m.scroll == 1 {
			unit = "line"
		}
		status = dimStyle.Render(fmt.Sprintf("↑ %d %s below · pgdn to follow", m.scroll, unit))
	}
	rows = append(rows, status)
	if m.replyTo != "" {
		first := strings.SplitN(m.replyTo, "\n", 2)[0]
		more := ""
		if n := strings.Count(m.replyTo, "\n"); n > 0 {
			more = fmt.Sprintf(" +%d lines", n)
		}
		rows = append(rows, dimStyle.Render("↩ replying to ")+
			quoteStyle.Render(ansi.Truncate(first, max(10, m.innerWidth()-28), "…"))+
			dimStyle.Render(more+"  esc to drop"))
	}
	// Directly above the input, so the list reads as belonging to the line
	// being typed rather than as another thing on the screen.
	if m.sug != nil {
		rows = append(rows, strings.Split(m.sug.render(m.innerWidth()), "\n")...)
	}
	rows = append(rows, strings.Split(m.box(), "\n")...)
	// The bar sits under the input: where you are and how full the context is
	// are reference, not something to read past on the way to typing.
	rows = append(rows, m.bar())
	for i := 0; i < padBottom; i++ {
		rows = append(rows, "")
	}

	// The gutter is applied once here rather than baked into every component,
	// so each of them can go on measuring against innerWidth alone. Blank rows
	// are left empty rather than padded, to avoid trailing whitespace.
	pad := strings.Repeat(" ", padX)
	for i, r := range rows {
		if r != "" {
			rows[i] = pad + r
		}
	}
	v.SetContent(strings.Join(rows, "\n"))

	// The cursor is positioned relative to the input widget; shift it past the
	// transcript, the status row, the bar and the border.
	// While the chooser is open the cursor belongs in its search field, so it
	// reads as somewhere to type rather than a static list.
	if m.pick != nil {
		cur := tea.NewCursor(padX+1+m.pick.cursorCol(), m.viewHeight()+1)
		if w := m.watched(); w != nil {
			cur.Y += w.height()
		}
		v.Cursor = cur
		return v
	}
	// While the key prompt is open the cursor belongs in it, not in the input.
	if m.keyAsk != nil {
		if cur := m.keyAsk.input.Cursor(); cur != nil {
			// Past the transcript and status row, the prompt's own border, and
			// the two heading rows inside it.
			cur.Y += m.viewHeight() + 1 + 3
			if w := m.watched(); w != nil {
				cur.Y += w.height()
			}
			cur.X += boxPadX + 1 + padX
			v.Cursor = cur
		}
		return v
	}
	if cur := m.input.Cursor(); cur != nil {
		// Past the transcript, the status row and the input's top border.
		below := m.viewHeight() + 2
		if m.replyTo != "" {
			below++
		}
		if w := m.watched(); w != nil {
			below += w.height()
		}
		if m.pick != nil {
			below += m.pick.height()
		}
		if m.sug != nil {
			below += m.sug.height()
		}
		cur.Y += below
		cur.X += boxPadX + 1 + padX
		v.Cursor = cur
	}
	return v
}

// transcript returns exactly viewHeight rows: the conversation from the top of
// the screen down, with blank rows filling whatever is left above the input.
// Once there is more content than fits, the newest end is what is shown.
// visibleStart is the index into content() of the topmost visible row. The
// renderer and the click handler both derive their positions from it, so they
// cannot disagree about which row a click landed on.
func (m Model) visibleStart() int {
	h := m.viewHeight()
	end := len(m.content()) - m.scroll
	if end < 0 {
		end = 0
	}
	return max(0, end-h)
}

// entryAtRow resolves a screen row to a transcript entry, or -1.
//
// Rows past the transcript belong to the live thinking block, which is not part
// of the conversation and has nothing to reply to.
func (m Model) entryAtRow(row int) int {
	if row < 0 || row >= m.viewHeight() {
		return -1
	}
	idx := m.visibleStart() + row
	if idx < 0 || idx >= len(m.owner) {
		return -1
	}
	return m.owner[idx]
}

func (m Model) transcript() []string {
	h := m.viewHeight()

	// Nothing said yet: show the mascot instead of an empty screen. It is not
	// part of the transcript, so it disappears the moment there is real content
	// and never has to be scrolled past.
	if len(m.entries) == 0 {
		companionLine := fmt.Sprintf("★ %d %s", m.comp.Level(), m.comp.Title())
		if art := welcomeRows(m.innerWidth(), h, m.comp.Level(), m.ag.Model(),
			m.ag.Mode().String(), shortRoot(m.root), companionLine); art != nil {
			rows := make([]string, 0, h)
			// Sit the block a little above centre, where the eye expects it.
			for i := 0; i < max(0, (h-len(art))/3); i++ {
				rows = append(rows, "")
			}
			rows = append(rows, art...)
			for len(rows) < h {
				rows = append(rows, "")
			}
			return rows[:h]
		}
	}
	all := m.content()
	end := len(all) - m.scroll
	if end < 0 {
		end = 0
	}
	start := max(0, end-h)
	visible := all[start:end]

	rows := make([]string, 0, h)
	rows = append(rows, visible...)
	for i := len(visible); i < h; i++ {
		rows = append(rows, "")
	}
	return rows
}

// box draws the input inside a rounded border. Only the border colour is set —
// no background — so the terminal still shows through the frame.
func (m Model) box() string {
	border := borderIdle
	if m.busy {
		border = borderBusy
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border.GetForeground()).
		Padding(0, boxPadX).
		Width(max(10, m.innerWidth()-2)).
		Render(m.input.View())
}

// bar renders the reference line under the input: mode, where you are, what
// you are talking to, and how full its context is — all on the left, reading
// as one sequence rather than split across the width.
func (m Model) bar() string {
	sep := dimStyle.Render(" · ")

	parts := []string{modeStyle(m.ag.Mode()).Render(m.ag.Mode().String())}
	if m.branch != "" {
		parts = append(parts, branchStyle.Render("⎇ "+m.branch))
	}
	parts = append(parts, modelStyle.Render(m.ag.Model()))
	if u := m.usage(); u != "" {
		parts = append(parts, u)
	}
	// Last, so it is the first thing dropped on a narrow terminal: a level is
	// the least urgent thing on the line.
	parts = append(parts, levelStyle.Render(fmt.Sprintf("★%d", m.comp.Level())))

	// Trimmed from the right, so the mode — the thing that changes what a
	// keystroke does — is the last to go.
	return ansi.Truncate(strings.Join(parts, sep), m.innerWidth(), "…")
}

// usage describes context consumption. With a declared context window it is a
// bar and a percentage; without one, just a token count.
func (m Model) usage() string {
	if m.ctxTokens == 0 {
		return ""
	}
	limit := m.contextLimit()
	if limit <= 0 {
		// No declared window: a raw count is honest, a percentage would not be.
		return dimStyle.Render(humanTokens(m.ctxTokens) + " tokens")
	}

	pct := min(m.ctxTokens*100/limit, 100)
	const cells = 10
	filled := min(cells, m.ctxTokens*cells/limit)
	st := usageStyle(pct)
	return st.Render(strings.Repeat("█", filled)) +
		dimStyle.Render(strings.Repeat("░", cells-filled)) +
		st.Render(fmt.Sprintf(" %d%%", pct)) +
		dimStyle.Render(" · "+humanTokens(m.ctxTokens))
}

// contextLimit is the window of whatever model is in use right now. It is read
// from the agent rather than the config, since escalation can change the model
// mid-conversation.
func (m Model) contextLimit() int { return m.ag.Context() }

// contextNearlyFull reports whether the conversation is close enough to the
// window that the server will start dropping the oldest messages — which means
// the system prompt and the original question.
func (m Model) contextNearlyFull() bool {
	limit := m.contextLimit()
	return limit > 0 && m.ctxTokens*100/limit >= 85
}

// humanTokens renders a token count compactly: 940, 1.2k, 24k, 2.4M. The
// companion deals in millions, so "2400k" would be a poor way to say it.
func humanTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1000)
	case n < 10_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	default:
		return fmt.Sprintf("%dM", n/1_000_000)
	}
}

// shortRoot abbreviates the home directory, which is where most of these
// paths live and is not worth the width.
func shortRoot(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func (m Model) wrap(s string) string {
	w := m.innerWidth()
	if w <= 0 {
		w = 80
	}
	return ansi.Wrap(s, w, "")
}

// splitLines separates complete lines from the unfinished trailing fragment.
func splitLines(s string) (lines []string, rest string) {
	i := strings.LastIndexByte(s, '\n')
	if i < 0 {
		return nil, s
	}
	return strings.Split(s[:i], "\n"), s[i+1:]
}

func waitFor(ch chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamDoneMsg{}
		}
		return eventMsg{ev}
	}
}

func tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// summarize renders tool arguments as a compact single line.
func summarize(args string, limit int) string {
	if limit < 10 {
		limit = 10
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return ansi.Truncate(strings.Join(strings.Fields(args), " "), limit, "…")
	}
	// Prefer the argument that identifies what is being acted on.
	for _, k := range []string{"command", "path", "pattern", "description"} {
		if v, ok := m[k]; ok {
			return ansi.Truncate(fmt.Sprintf("%v", v), limit, "…")
		}
	}
	return ansi.Truncate(strings.Join(strings.Fields(args), " "), limit, "…")
}

func resultSummary(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "done"
	}
	n := strings.Count(s, "\n") + 1
	if n > 1 {
		return fmt.Sprintf("%d lines", n)
	}
	return ansi.Truncate(s, 72, "…")
}
