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
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"raunen/internal/agent"
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

// maxThinkBytes caps how much of a reasoning model's thinking is kept for
// display. Only the tail matters; it is shown live and never stored.
const maxThinkBytes = 8000

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
	// branch is the git branch of root, empty outside a repository.
	branch string
	// ctxTokens is the conversation size reported by the last request.
	ctxTokens int
	// warnedFull records that the context-pressure warning has been shown for
	// the current turn, so it is not repeated on every request within it.
	warnedFull bool
	// sess is the conversation on disk, written after each turn.
	sess *session.Session
	// sub is the panel a running sub-agent works in, nil when none is running.
	sub *subView
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

	width  int
	height int
	quit   bool
}

func New(cfg *config.Config, ag *agent.Agent, root, ref string, sess *session.Session) Model {
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
		sess:   sess,
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
			m.push(entry{
				first: barStyle.Render("▌ "),
				cont:  barStyle.Render("▌ "),
				text:  userStyle.Render(msg.Content),
			})
			m.blank()
		case provider.Assistant:
			for _, tc := range msg.ToolCalls {
				m.push(entry{
					first: "  " + toolStyle.Render("⏺ "),
					cont:  "      ",
					text:  toolStyle.Render(tc.Function.Name) + dimStyle.Render("  "+summarize(tc.Function.Arguments, 40)),
				})
			}
			if t := strings.TrimSpace(msg.Content); t != "" {
				m.blank()
				for _, l := range strings.Split(t, "\n") {
					m.push(md.entry(l))
				}
			}
		case provider.ToolRole:
			m.push(entry{
				first: "    " + dimStyle.Render("↳ "),
				cont:  "      ",
				text:  dimStyle.Render(resultSummary(msg.Content)),
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
	// rule makes this a turn separator drawn to the full width, optionally
	// labelled with stamp. It is re-rendered on resize rather than stored, so
	// the rule always spans the terminal.
	rule  bool
	stamp string
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
func (m *Model) push(e entry) {
	m.entries = append(m.entries, e)
	rows := e.rows(m.innerWidth())
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

// rewrap rebuilds the wrapped view of the transcript, after a resize or a trim.
func (m *Model) rewrap() {
	m.wrapWidth = m.innerWidth()
	m.display = m.display[:0]
	for _, e := range m.entries {
		m.display = append(m.display, e.rows(m.innerWidth())...)
	}
}

// content is what the transcript area shows: the conversation, followed by the
// live thinking block if a reasoning model is mid-thought. Thinking is rendered
// here rather than on the status row so it can run to several lines.
func (m Model) content() []string {
	if m.think == "" {
		return m.display
	}
	rows := make([]string, 0, len(m.display)+8)
	rows = append(rows, m.display...)
	rows = append(rows, m.thinkRows()...)
	return rows
}

// thinkRows renders the thinking as dim wrapped rows under a quiet label.
func (m Model) thinkRows() []string {
	text := strings.TrimSpace(m.think)
	if text == "" {
		return nil
	}
	rows := []string{"", dimStyle.Render("thinking")}
	for _, para := range strings.Split(text, "\n") {
		for _, row := range strings.Split(m.wrap(para), "\n") {
			rows = append(rows, thinkStyle.Render(row))
		}
	}
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
	if m.sub != nil {
		h -= m.sub.height()
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
		return m.onKey(msg)

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
		if m.pick != nil {
			m.pick.err = msg.err
			m.pick.needsKey = msg.needsKey
			m.pick.setModels(msg.models)
		}
		return m, nil

	case streamDoneMsg:
		m.busy = false
		m.cancel = nil
		m.events = nil
		// A sub-agent cannot outlive the turn that spawned it, so the panel
		// closes here even if the turn ended badly.
		m.sub = nil
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
	return m, cmd
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
		case "enter":
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
		if m.busy {
			m.cancel()
		}
		return *m, nil

	case "ctrl+d":
		if !m.busy && m.input.Value() == "" {
			m.quit = true
			return *m, tea.Quit
		}

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
		m.pick = &picker{loading: true}
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
	m.add(okStyle.Render("✓ switched to " + ref))
	return *m, nil
}

// persist writes the conversation out. A failure to save is reported inline
// rather than returned: losing a session is worth telling the user about, but
// not worth interrupting the conversation for.
func (m *Model) persist() {
	m.sess.Messages = m.ag.Messages()
	if m.sess.Model == "" {
		m.sess.Model = m.ref
	}
	if err := m.sess.Save(); err != nil {
		m.add(errStyle.Render("✗ could not save session: " + err.Error()))
	}
	// Keep the advertised title current, so the picker shows what this
	// instance is about rather than "(new session)".
	m.sess.UpdateTitle()
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
func (m *Model) openTurn(text string) {
	m.blank()
	m.push(entry{rule: true, stamp: time.Now().Format("15:04")})
	m.push(entry{
		first: barStyle.Render("▌ "),
		cont:  barStyle.Render("▌ "),
		text:  userStyle.Render(text),
	})
	m.blank()
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
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.busy = true
	m.frame = 0
	m.pending = ""
	m.think = ""
	m.md = markdown{}
	m.inText = false
	m.warnedFull = false
	m.events = make(chan agent.Event, 64)
	// Sending jumps back to the newest output: the reply is what you want to
	// see, wherever you had scrolled to.
	m.scroll = 0
	// The input stays focused while the model works so the next message can be
	// composed in the meantime. Enter is ignored until the turn finishes, and
	// the typed text simply stays in the box.
	go m.ag.Run(ctx, text, m.events)

	m.openTurn(text)
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
		m.think = tailOf(m.think+e.Text, maxThinkBytes)
		return *m, next

	case agent.ToolStart:
		// Delegation announces itself through TaskStart, which opens the panel.
		// Reporting the call as an ordinary tool as well says it twice.
		if e.Name == "task" && e.Depth == 0 {
			return *m, next
		}
		if e.Depth > 0 && m.sub != nil {
			m.sub.add(toolStyle.Render("⏺ "+e.Name) +
				dimStyle.Render("  "+summarize(e.Args, max(10, m.innerWidth()-len(e.Name)-14))))
			return *m, next
		}
		m.think = ""
		m.flush()
		m.inText = false
		pad := strings.Repeat("  ", 1+e.Depth)
		m.push(entry{
			first: pad + toolStyle.Render("⏺ "),
			cont:  pad + "    ",
			text:  toolStyle.Render(e.Name) + dimStyle.Render("  "+summarize(e.Args, max(20, m.innerWidth()-len(e.Name)-10))),
		})
		return *m, next

	case agent.ToolEnd:
		// Likewise: TaskEnd reports what came back.
		if e.Name == "task" && e.Depth == 0 {
			return *m, next
		}
		text, style := resultSummary(e.Result), dimStyle
		if e.Err != nil {
			text, style = e.Err.Error(), errStyle
		}
		if e.Depth > 0 && m.sub != nil {
			m.sub.add("  " + dimStyle.Render("↳ ") + style.Render(text))
			return *m, next
		}
		pad := strings.Repeat("  ", 2+e.Depth)
		m.push(entry{
			first: pad + dimStyle.Render("↳ "),
			cont:  pad + "  ",
			text:  style.Render(text),
		})
		return *m, next

	case agent.TaskStart:
		m.think = ""
		m.flush()
		m.inText = false
		// The panel opens here and takes the sub-agent's steps from now on.
		m.sub = &subView{desc: e.Description}
		m.push(entry{
			first: "  " + taskStyle.Render("◆ "),
			cont:  "    ",
			text:  taskStyle.Render("task") + dimStyle.Render("  "+e.Description),
		})
		return *m, next

	case agent.TaskEnd:
		// The panel collapses, leaving one line in the transcript. What the
		// sub-agent did was working-out, not conversation.
		m.sub = nil
		text, style := fmt.Sprintf("returned %d chars after %d steps", len(e.Summary), e.Steps), dimStyle
		if e.Err != nil {
			text, style = e.Err.Error(), errStyle
		}
		m.push(entry{
			first: "    " + dimStyle.Render("↳ "),
			cont:  "      ",
			text:  style.Render(text),
		})
		return *m, next

	case agent.Usage:
		m.ctxTokens = e.Total
		// Warn before the window overflows rather than after: once the server
		// starts truncating, the model loses the question it was asked and
		// begins answering something else entirely.
		if m.contextNearlyFull() && !m.warnedFull {
			m.warnedFull = true
			m.add(errStyle.Render(fmt.Sprintf(
				"⚠ context %d%% full — replies will degrade. /clear to reset, or raise the model's context.",
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
		m.push(entry{
			first: "  " + okStyle.Render("⇅ "),
			cont:  "    ",
			text: okStyle.Render("switched to "+e.To) +
				dimStyle.Render("  — "+e.Reason),
		})
		return *m, next

	case agent.Rejected:
		m.push(entry{
			first: "  " + errStyle.Render("✗ "),
			cont:  "    ",
			text: errStyle.Render(e.Ref+" refused") +
				dimStyle.Render("  — "+e.Reason),
		})
		return *m, next

	case agent.Tripped:
		m.push(entry{
			first: "  " + askStyle.Render("⚠ "),
			cont:  "    ",
			text: askStyle.Render(e.Provider+" taken out of rotation") +
				dimStyle.Render(fmt.Sprintf("  — repeated failures, retrying in %s", e.For)),
		})
		return *m, next

	case agent.Trimmed:
		m.add(dimStyle.Render(fmt.Sprintf(
			"⋯ dropped %d earlier messages to fit the context", e.Messages)))
		return *m, next

	case agent.TurnEnd:
		m.think = ""
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
					"(no reply — the context is full. /clear to reset, or raise the model's context.)"))
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
		m.blank()
	}
	for _, l := range lines {
		m.push(m.md.entry(l))
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
			m.pick = &picker{loading: true}
			return *m, fetchModels(m.cfg)
		}
		return m.switchModel(fields[1])

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

	case "/status":
		m.status()
		return *m, probeProviders(m.cfg)

	case "/providers":
		for name, p := range m.cfg.Providers {
			m.add(dimStyle.Render(fmt.Sprintf("  %-12s %s", name, p.BaseURL)))
		}
		return *m, nil

	case "/help":
		for _, l := range []string{
			"  /model                   choose a model from a list",
			"  /model <provider/model>  switch directly",
			"  /status                  model, context, ladder, endpoints",
			"  /providers               list configured endpoints",
			"  /key <provider>          add an api key",
			"  /clear                   start a new session",
			"  /sessions                list saved sessions",
			"  /resume <id>             pick up a saved session",
			"  /quit                    exit",
			"  esc                      cancel the running turn",
			"  pgup/pgdn, shift+↑/↓     scroll the transcript",
			"  shift+enter              newline without sending",
			"  tab                      cycle auto / accept edits / plan",
		} {
			m.add(dimStyle.Render(l))
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

	if m.quit {
		return v
	}

	rows := make([]string, 0, m.height)
	rows = append(rows, m.transcript()...)
	if m.sub != nil {
		f := spinnerFrames[m.frame%len(spinnerFrames)]
		rows = append(rows, strings.Split(m.sub.render(m.innerWidth(), f), "\n")...)
	}
	if m.pick != nil {
		rows = append(rows, strings.Split(m.pick.render(m.innerWidth(), m.ref), "\n")...)
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
	} else if m.busy {
		f := spinnerFrames[m.frame%len(spinnerFrames)]
		// The thinking itself is shown in the transcript area, so the status
		// row only reports state.
		s := "working"
		if m.think != "" {
			s = "thinking"
		}
		tail := "  esc to cancel"
		if m.queued != "" {
			tail = "  ⏎ 1 queued  ·  esc to cancel"
		}
		status = spinStyle.Render(f) + " " +
			dimStyle.Render(ansi.Truncate(s, max(10, m.innerWidth()-len(tail)-6), "…")) +
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
	// While the key prompt is open the cursor belongs in it, not in the input.
	if m.keyAsk != nil {
		if cur := m.keyAsk.input.Cursor(); cur != nil {
			// Past the transcript and status row, the prompt's own border, and
			// the two heading rows inside it.
			cur.Y += m.viewHeight() + 1 + 3
			if m.sub != nil {
				cur.Y += m.sub.height()
			}
			cur.X += boxPadX + 1 + padX
			v.Cursor = cur
		}
		return v
	}
	if cur := m.input.Cursor(); cur != nil {
		// Past the transcript, the status row and the input's top border.
		below := m.viewHeight() + 2
		if m.sub != nil {
			below += m.sub.height()
		}
		if m.pick != nil {
			below += m.pick.height()
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
func (m Model) transcript() []string {
	h := m.viewHeight()

	// Nothing said yet: show the mascot instead of an empty screen. It is not
	// part of the transcript, so it disappears the moment there is real content
	// and never has to be scrolled past.
	if len(m.entries) == 0 {
		if art := welcomeRows(m.innerWidth(), h, m.ag.Model(), m.ag.Mode().String(), shortRoot(m.root)); art != nil {
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

// humanTokens renders a token count compactly: 940, 1.2k, 24k.
func humanTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 10000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%dk", n/1000)
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

// tailOf keeps the last n bytes, on a rune boundary.
func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
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
