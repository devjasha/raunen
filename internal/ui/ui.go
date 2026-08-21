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
	"net/http"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"raunen/internal/agent"
	"raunen/internal/attach"
	"raunen/internal/companion"
	"raunen/internal/config"
	"raunen/internal/permission"
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

type eventMsg struct {
	turn *turn
	ev   agent.Event
}
type streamDoneMsg struct{ turn *turn }
type tickMsg time.Time

// turn is one question being answered. There can be several at once: asking
// something while the model is working starts another rather than waiting for
// the first, which is the whole point of being able to delegate long work.
//
// Everything here is state that belongs to one answer and would be corrupted by
// sharing. Two replies streaming at the same time each need their own
// half-received line and their own fenced-code-block state, or one turn's
// backtick would put the other turn's prose inside a code block.
type turn struct {
	// seq numbers turns for the life of the session, so a sub-agent panel can
	// be attributed to the turn that opened it.
	seq int
	// ag answers this turn. It is a fork of the conversation for every turn
	// after the first in flight, and its exchange is merged back when it ends.
	// Nil for /compact, which drives this machinery without being a turn.
	ag     *agent.Agent
	events chan agent.Event
	cancel context.CancelFunc

	// start is when this turn began, so the status row can say how long it has
	// been going. A local model on a long turn gives no other sign of progress,
	// and "working" alone cannot be told apart from "wedged".
	start time.Time

	// pending is assistant text received but not yet turned into whole lines.
	pending string
	// think is the tail of a reasoning model's thinking, shown as a single
	// status row. It never enters the transcript.
	think string
	// md carries fenced-code-block state across streamed lines.
	md markdown
	// inText records whether the last thing this turn wrote was prose, so a
	// blank line can separate it from the tool calls around it.
	inText bool
	// warnedFull records that the context-pressure warning has been shown for
	// this turn, so it is not repeated on every request within it.
	warnedFull bool
	// rejected records that the chosen model refused outright, so a switch away
	// from it can be made permanent rather than repeated tomorrow.
	rejected bool
	// adopt is a model to remember as the default, but only after it answers.
	adopt string
	// tag is the gutter mark put in front of everything this turn writes, so
	// two answers arriving into one transcript can be told apart. Empty while a
	// turn has the conversation to itself, which is the usual case and wants no
	// decoration; set on every live turn the moment a second one starts, and
	// never cleared afterwards — the marks begin exactly where the overlap
	// does, which is where the reader starts needing them.
	tag string
	// block is the message id this turn's reply is being written under, held so
	// the reply stays one clickable message even when another turn's output
	// lands in the middle of it.
	block int
}

// turnMarks distinguish concurrent turns. Shape as well as colour, because
// colour alone is not enough: a terminal with a narrow palette, a screenshot in
// black and white, or a reader who cannot tell 5 from 6 would be left with two
// identical bars down the screen and no way to read the transcript apart.
//
// The colours are ANSI indices, like everything else here, so they follow the
// terminal's own palette. Blue is left out — the question is already blue, and
// the mark has to be distinguishable from the bar it sits next to.
var turnMarks = []struct{ glyph, color string }{
	{"┃", "5"},
	{"┆", "6"},
	{"╏", "2"},
	{"┇", "3"},
}

// turnMark builds a turn's gutter, given its number.
func turnMark(seq int) string {
	m := turnMarks[(seq-1)%len(turnMarks)]
	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.color)).Render(m.glyph)
}

type Model struct {
	cfg   *config.Config
	ag    *agent.Agent
	root  string
	input textarea.Model

	// turns are the questions being answered right now, oldest first. Usually
	// none or one; more when the user asked something else without waiting.
	turns []*turn
	// turnSeq numbers them.
	turnSeq int

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
	// attached are images waiting to go with the next message. They are held
	// here rather than sent on the /image command itself so a picture and the
	// question about it arrive together: an image on its own turn makes the
	// model describe it, which is rarely what was wanted.
	attached []provider.Image

	frame int

	// ref is the active "provider/model" reference, kept so the context limit
	// can be looked up again after /model.
	ref string
	// chosen is the model the user picked, which is not always the active one:
	// escalation moves ref to a roomier model for a turn, and that is a
	// temporary measure rather than a decision worth remembering.
	chosen string
	// branch is the git branch of root, empty outside a repository.
	branch string
	// ctxTokens is the conversation size reported by the last request.
	ctxTokens int
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
	// expanded is whether the watched panel is open to its full window
	// (steps and answer) rather than the small preview of the tail.
	expanded bool
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
	// mcp reports how many tools each MCP server contributes, keyed by server
	// name. Empty when none were configured; absent keys mean the server was
	// defined but never started. It is a function rather than a map because a
	// server that advertises Tools.ListChanged can revise its toolset mid-session,
	// and /mcp should show what is true now rather than what was true at startup.
	mcp func() map[string]int
	// mcpLazy reports that the MCP tools are held in a catalogue rather than
	// advertised, so /mcp can explain why the model does not see them directly.
	// A function for the same reason mcp is one: the servers connect after the
	// first frame is drawn, so this is not known until they have answered.
	mcpLazy func() bool
	// mcpLogin authorizes a server by name, and mcpLogout drops its credentials.
	// Both nil outside the terminal, where there is nobody to answer a browser.
	mcpLogin  func(ctx context.Context, name string) (string, error)
	mcpLogout func(name string) error
	// mcpFails reports why each server did not start. A function like mcp, and
	// for a sharper reason: the servers connect after the terminal has drawn, so
	// a failure reported on stderr from then on would be written into the
	// alternate screen and lost. /mcp is where it stays readable.
	mcpFails func() map[string]string
	// ready is closed once the work deferred past the first frame — connecting
	// to MCP servers, building the fallback ladder — has finished. See SetReady.
	ready <-chan struct{}
	// project names the AGENTS.md files in force, for /status. Empty when the
	// project has none, which is the common case and shows as a hint rather
	// than as a missing row.
	project string
	// lastInput is what the input held when the list was last rebuilt, which is
	// how a dismissal can tell "still the same word" from "typing again".
	lastInput string
	// lastCaret is where the caret was when the list was last rebuilt, as a
	// rune offset. Completion happens at the caret, so moving it is as much a
	// change of question as typing is: arrowing onto a different @token has to
	// offer that token's paths, and has to lift a dismissal that was about the
	// word the user has now left.
	lastCaret int
	// files is the snapshot @ mentions complete against, nil until the first
	// one is typed: a session that never mentions a file never pays for a scan.
	files *fileIndex
	// scanning records that a scan is in flight, so a burst of keystrokes
	// starts one walk of the tree rather than one per key.
	scanning bool

	width  int
	height int
	quit   bool

	// update is the latest published version, set once a background check finds
	// one newer than the running build. Empty until then — and stays empty if
	// the check fails or is offline — so nothing is shown unless there is news.
	update string
}

// SetMCPSummary installs a source for how many tools each MCP server
// contributes, so /mcp and /status can list them. It is read at render time
// rather than captured, because a server advertising Tools.ListChanged can add
// or drop tools while the session runs.
func (m *Model) SetMCPSummary(s map[string]int) {
	m.mcp = func() map[string]int { return s }
}

// SetMCPCounts installs a live source for the per-server tool counts. Prefer it
// over SetMCPSummary when the counts can change during the session.
func (m *Model) SetMCPCounts(f func() map[string]int) { m.mcp = f }

// SetMCPLazy records that the MCP tools are reached through search and select
// rather than advertised on every request, so /mcp can say so.
func (m *Model) SetMCPLazy(f func() bool) { m.mcpLazy = f }

// SetMCPFailures installs a source for why servers did not start, shown in /mcp.
func (m *Model) SetMCPFailures(f func() map[string]string) { m.mcpFails = f }

// SetMCPAuth installs how /mcp auth logs in to a server and /mcp logout clears
// it. Injected like the rest of the MCP surface so this package keeps no
// knowledge of the protocol: it asks for a server by name and is told what
// happened, in words it prints unaltered.
//
// authURL, when the login needs a browser that could not be opened, is the
// address to visit. It is returned rather than printed because stderr is not
// visible from here — that is the whole reason this command exists.
func (m *Model) SetMCPAuth(login func(ctx context.Context, name string) (authURL string, err error), logout func(name string) error) {
	m.mcpLogin, m.mcpLogout = login, logout
}

// readyMsg says the deferred startup work has finished and carries the message
// that arrived before it did, so the turn can be started for real.
type readyMsg struct{ text string }

// waitReady blocks in a command — off the UI goroutine — and hands the waiting
// message back once the tools are in place.
func waitReady(ready <-chan struct{}, text string) tea.Cmd {
	return func() tea.Msg {
		<-ready
		return readyMsg{text: text}
	}
}

// SetReady installs a gate that a turn waits on before it starts. It is how
// startup hands the terminal a drawable frame before the MCP servers have
// answered: the wiring lands on its own goroutine, and the first turn — which
// cannot come before the user has typed — waits here for it.
//
// Waited on rather than polled because a fork copies the registry, so a turn
// that started early would answer without those tools for its whole length.
func (m *Model) SetReady(ready <-chan struct{}) { m.ready = ready }

// SetProject records which instruction files were loaded, as a summary line for
// /status. Instructions that quietly did not arrive look exactly like a model
// ignoring them, so which files are in force has to be visible.
func (m *Model) SetProject(summary string) { m.project = summary }

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
	// The blink and the version check are independent and both run once at
	// startup; batching them fires the check without delaying the first frame.
	return tea.Batch(textarea.Blink, m.updateCmd())
}

// releasesURL is the GitHub endpoint that resolves to the newest published
// release. A semver comparison against the running build decides whether to
// say anything, so a dev build — version "dev" — is never nagged.
const releasesURL = "https://api.github.com/repos/devjasha/raunen/releases/latest"

// updateCmd checks for a newer release without blocking startup. It runs off
// the main loop and reports back with an updateMsg, which the model stores if
// the version found is newer than what is running.
func (m Model) updateCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
		if err != nil {
			return updateMsg{}
		}
		// GitHub's API asks for a User-Agent and refuses requests without one.
		req.Header.Set("User-Agent", "raunen/"+Version)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return updateMsg{}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return updateMsg{}
		}
		var rel struct {
			TagName string `json:"tag_name"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
			return updateMsg{}
		}
		if rel.TagName == "" {
			return updateMsg{}
		}
		// Only surface it when it is actually newer, so a rebuild of the same
		// release — or a dev build that cannot be compared — stays quiet.
		if newer(rel.TagName, Version) {
			return updateMsg{version: rel.TagName}
		}
		return updateMsg{}
	}
}

// updateMsg carries the result of the background version check. version is
// empty when there is nothing to report — either no update, or the check
// failed and we do not want to fuss the user over a flaky connection.
type updateMsg struct{ version string }

// newer reports whether a is a semver release tag strictly later than b.
// Both are expected stripped of a leading "v". A build with no comparable
// version (one of them empty, or "dev") is treated as not newer, so the hint
// only ever points forward to a concrete release.
func newer(a, b string) bool {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	if a == "" || b == "" || b == "dev" {
		return false
	}
	as, bs := splitVer(a), splitVer(b)
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] != bs[i] {
			return as[i] > bs[i]
		}
	}
	// A longer, equal-prefix version (e.g. 1.2.1 vs 1.2.0) is newer.
	return len(as) > len(bs)
}

// splitVer turns "1.2.3" into [1, 2, 3], ignoring any non-numeric tail so a
// tag like "1.2.3-rc1" still compares on its numeric parts.
func splitVer(v string) []int {
	out := []int{}
	for _, p := range strings.Split(v, ".") {
		n := 0
		for _, r := range p {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		out = append(out, n)
	}
	return out
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
	// turn is which concurrent turn wrote this line, zero for anything the UI
	// wrote itself. It is what lets a gap be opened when the transcript changes
	// speaker mid-answer.
	turn int
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
	// code, when set, makes this entry a bordered window of source rather than
	// a line of text. It is held as data, not as rendered rows, because rewrap
	// re-renders every entry at the new width after a resize — a stored block
	// of rows would keep the border width it was built at and leave a ragged
	// column down the screen. See codeBlock.
	code *codeBlock
	// toolLive marks a tool call — its start line, its code window, and its
	// result — as the agent's current step. The next tool call replaces it
	// rather than adding another line, so the transcript shows only what the
	// agent is doing now. What it was doing ten steps ago is not what the
	// reader came to see.
	toolLive bool
}

// blankLine reports whether an entry renders as empty space. A code window is
// not one: it carries no text of its own — everything it draws lives in its
// codeBlock — so testing the text fields alone would take it for a spacer, let
// blank() skip the gap after it and let lastKind look straight through it.
func (e entry) blankLine() bool {
	return e.text == "" && e.first == "" && e.code == nil
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
	// A code window owns the whole entry: it is drawn from its own data at the
	// current width, which is what keeps the border square after a resize.
	if e.code != nil {
		return e.code.rows(width)
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
		// A code window is left out of a quote. It is a picture of a change the
		// model itself just made, so sending the frame and the gutter markers
		// back would spend tokens telling it what it already knows.
		if e.code != nil {
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

// busy reports whether anything is being answered. It is a question about the
// whole screen — the spinner, the border, whether ctrl+c means "stop" or
// "quit" — so it asks about every turn rather than a particular one.
func (m Model) busy() bool { return len(m.turns) > 0 }

// elapsed is how long the oldest live turn has been running, or empty when
// nothing is. Turns started before this field existed — and the zero turns the
// tests build — report nothing rather than a duration measured from the epoch.
func (m Model) elapsed() string {
	oldest := time.Time{}
	for _, t := range m.turns {
		if t.start.IsZero() {
			continue
		}
		if oldest.IsZero() || t.start.Before(oldest) {
			oldest = t.start
		}
	}
	if oldest.IsZero() {
		return ""
	}
	return humanDuration(time.Since(oldest))
}

// live finds a turn by identity, nil once it has ended. Events carry the turn
// they came from rather than an index, so a turn finishing cannot misroute the
// events of one still running.
func (m Model) live(t *turn) *turn {
	for _, x := range m.turns {
		if x == t {
			return x
		}
	}
	return nil
}

// compacting reports whether the conversation is being rewritten. A compaction
// is the one piece of work that runs against m.ag itself — summarising the
// transcript is precisely a rewrite of it — so nothing may fork from it
// meanwhile. It is marked by carrying no agent of its own.
func (m Model) compacting() bool {
	for _, t := range m.turns {
		if t.ag == nil {
			return true
		}
	}
	return false
}

// stop cancels one turn. The turn is left in flight: the agent notices the
// cancellation, reports it, and closes its channel, and the turn is cleared
// where every other turn is — so a cancelled turn still merges what it managed
// to do rather than losing it.
func (m *Model) stop(t *turn) {
	if t != nil {
		t.cancel()
	}
}

// stopAll cancels everything running.
func (m *Model) stopAll() {
	for _, t := range m.turns {
		t.cancel()
	}
}

// drop removes a finished turn.
func (m *Model) drop(t *turn) {
	out := m.turns[:0]
	for _, x := range m.turns {
		if x != t {
			out = append(out, x)
		}
	}
	m.turns = out
}

// thinking is the tail of any reasoning happening now. With several turns in
// flight the transcript still shows one "thinking…" line: it says the machine
// is working, and saying it twice tells the reader nothing more.
func (m Model) thinking() bool {
	for _, t := range m.turns {
		if t.think != "" {
			return true
		}
	}
	return false
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
	m.keepSubs(func(s *subView) bool { return s.id != id })
}

// dropSubsOf closes the panels belonging to a turn that has ended. Sub-agents
// another turn is still running are left alone.
func (m *Model) dropSubsOf(t *turn) {
	m.keepSubs(func(s *subView) bool { return s.owner != t })
}

func (m *Model) keepSubs(keep func(*subView) bool) {
	out := m.subs[:0]
	watched := false
	for _, s := range m.subs {
		if !keep(s) {
			continue
		}
		out = append(out, s)
		watched = watched || s.id == m.watching
	}
	m.subs = out
	if !watched {
		// What was being watched has gone. Move to another running sub-agent
		// rather than closing the panel out from under the reader.
		m.watching = ""
		if len(m.subs) > 0 {
			m.watching = m.subs[0].id
		}
	}
}

// newBlock starts a new message, so a click can select one whole.
func (m *Model) newBlock() { m.blockSeq++ }

func (m *Model) push(e entry) {
	// A block set already is one a turn is holding open across other turns'
	// output, so a click still selects the whole reply rather than the run of
	// it that happened not to be interrupted.
	if e.block == 0 {
		e.block = m.blockSeq
	}
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

// dropLive removes the entries marked toolLive — the agent's previous step —
// so the next one can replace them. Only the trailing run is touched, because
// live entries are always the most recent: a tool call is born live and stays
// live until it is superseded by the next, and nothing is appended after it
// but the next step or the prose that ends the turn.
func (m *Model) dropLive() {
	dropped := false
	for len(m.entries) > 0 && m.entries[len(m.entries)-1].toolLive {
		m.entries = m.entries[:len(m.entries)-1]
		dropped = true
	}
	// The blank lines in front of the step were opened to separate it from what
	// came before — one for the change of kind, another when a second turn was
	// writing — so they belong to the step and leave with it. Kept, they would
	// sit there unclaimed: lastKind and lastTurn both read straight through a
	// blank, so the next step sees the same prose, asks for the same separation
	// again, and the live line walks one row further down the screen with every
	// tool call. Whatever the replacement needs, pushTurn and pushKind decide
	// again from what is left.
	//
	// A rule is not a separator of this kind: it opens a turn, carries the
	// time, and only looks blank because it holds no text of its own.
	for dropped && len(m.entries) > 0 {
		if last := m.entries[len(m.entries)-1]; !last.blankLine() || last.rule {
			break
		}
		m.entries = m.entries[:len(m.entries)-1]
	}
	m.rewrap()
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
		if e.blankLine() {
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
	if !m.thinking() {
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
		h -= w.height(m.expanded, m.height-chromeLines-1, m.innerWidth())
	}
	if m.sug != nil {
		h -= m.sug.height()
	}
	if m.replyTo != "" {
		h--
	}
	if len(m.attached) > 0 {
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

	case updateMsg:
		// A newer release was found in the background. Stored, not printed: it
		// shows up on the bar beneath the input, which is drawn every frame.
		if msg.version != "" {
			m.update = msg.version
		}
		return m, nil

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
		if !m.busy() {
			return m, nil
		}
		m.frame++
		return m, tick()

	case eventMsg:
		return m.onEvent(msg.turn, msg.ev)

	case readyMsg:
		// The tools have landed; the message that was waiting on them can go.
		m.ready = nil
		return m.send(msg.text)

	case pastedMsg:
		if msg.err != nil {
			m.add(errStyle.Render("✗ " + msg.err.Error()))
			return m, nil
		}
		m.stage(msg.img)
		return m, nil

	case mcpAuthMsg:
		m.showMCPAuthResult(msg)
		return m, nil

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
		t := m.live(msg.turn)
		if t == nil {
			return m, nil
		}
		// Whatever this turn worked out is folded back into the conversation
		// now that it has stopped writing to its own copy, so the transcript
		// holds every turn even though they were answered side by side.
		if t.ag != nil {
			t.ag.Merge()
		}
		t.cancel()
		m.drop(t)
		// A sub-agent cannot outlive the turn that spawned it, so its panels
		// close here even if the turn ended badly. Only this turn's, though:
		// another turn may still have children of its own running.
		m.dropSubsOf(t)
		m.persist()
		m.input.Focus()
		return m, textarea.Blink
	}

	// Dragging a file onto a terminal window pastes its path, so a paste that
	// is nothing but image paths is a drop and stages them instead of typing
	// them out. Checked before the widgets below get it, because the input is
	// where the path would otherwise land — but only while the main input has
	// the keyboard: a path dropped into an API key prompt is a path.
	if p, ok := msg.(tea.PasteMsg); ok && m.keyAsk == nil && m.pick == nil && m.ask == nil {
		if m.dropped(p.Content) {
			return m, nil
		}
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
	v, caret := m.input.Value(), m.caretOffset()
	if v != m.lastInput || caret != m.lastCaret {
		// Typing something new is a fresh question, so an earlier dismissal
		// stops applying — and so is moving the caret, since the word being
		// completed is the one it sits after. Arrowing off a dismissed mention
		// and onto another one asks about the second one.
		m.lastInput, m.lastCaret = v, caret
		m.sugOff = false
	}
	if m.sugOff || m.pick != nil || m.keyAsk != nil || m.ask != nil {
		m.sug = nil
		return nil
	}
	prev := m.sug
	m.sug = suggestFor(v, caret, m.files, m.cfg)
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

// caretOffset is where the caret sits in Value(), counted in runes. The
// textarea reports the caret as a logical row plus a rune column, so the rows
// above it are measured as runes too — a prompt with an em dash or an ellipsis
// in it would land the splice a byte or two off otherwise.
func (m *Model) caretOffset() int {
	lines := strings.Split(m.input.Value(), "\n")
	row := min(max(m.input.Line(), 0), len(lines)-1)
	off := 0
	for _, l := range lines[:row] {
		off += len([]rune(l)) + 1 // the newline the split ate
	}
	return off + min(max(m.input.Column(), 0), len([]rune(lines[row])))
}

// setInput replaces the whole text and leaves the caret at a rune offset
// inside it.
//
// The textarea has no "put the caret at offset n" call, and stepping to it
// with CursorDown would count wrapped rows rather than logical ones. So the
// text goes in in two halves: the part after the caret first, then the part
// before it inserted at the very beginning — inserting leaves the caret at the
// end of what was inserted, which is precisely the offset wanted, however many
// newlines or wide runes it contains.
func (m *Model) setInput(text string, caret int) {
	r := []rune(text)
	caret = min(max(caret, 0), len(r))
	m.input.SetValue(string(r[caret:]))
	m.input.MoveToBegin()
	m.input.InsertString(string(r[:caret]))
}

// acceptSuggest replaces the token being completed with the highlighted entry,
// wherever in the text that token is, and leaves the caret just after what was
// inserted so typing carries on from there rather than jumping to the end of a
// prompt the user was editing the middle of.
func (m *Model) acceptSuggest() (tea.Model, tea.Cmd) {
	it, ok := m.sug.selected()
	if !ok {
		return *m, nil
	}
	insert := it.insert
	if it.space {
		insert += " "
	}
	r := []rune(m.input.Value())
	start := min(max(m.sug.start, 0), len(r))
	end := min(max(m.sug.end, start), len(r))
	text := string(r[:start]) + insert + string(r[end:])
	caret := start + len([]rune(insert))
	m.setInput(text, caret)
	// A finished completion closes the list. One that carries on — a directory
	// to step into — leaves it open, and the rebuild below fills it with what
	// is inside.
	m.lastInput, m.lastCaret, m.sugOff, m.sug = text, caret, it.space, nil
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
			if m.pick.kind == pickMCP {
				sel := m.pick.selected()
				m.pick = nil
				if sel == "" {
					return *m, nil
				}
				return *m, m.showMCPServer(sel)
			}
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
		case "a", "A":
			// "Yes, and stop asking." The grant covers this session only:
			// agreeing to something once, while looking at it, is a different
			// act from writing a rule that applies next month in a repository
			// you have not thought about yet.
			m.grant()
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
			if !m.busy() {
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
			// Judged on the token rather than the whole input: a command can
			// now sit on its own line under a prompt, and there the input holds
			// the prose above it too.
			if m.sug.kind == sugCommand && !isCommand(m.sug.token) {
				return m.acceptSuggest()
			}
		}
	}

	switch msg.String() {
	case "tab":
		m.ag.SetMode(m.ag.Mode().Next())
		return *m, nil

	case "ctrl+c":
		if m.busy() {
			// First interrupt cancels; it does not exit. Quitting mid-tool-call
			// would leave the transcript inconsistent.
			//
			// Everything running stops, because this is the panic key: with
			// several turns in flight the one thing the user certainly means by
			// it is "stop". esc is the finer instrument.
			m.stopAll()
			return *m, nil
		}
		m.quit = true
		return *m, tea.Quit

	case "esc":
		// Stopping the agent comes first. A pending reply also clears on esc,
		// but if a turn is running that is not what the key is for — and having
		// clicked a message mid-turn, the first esc appeared to do nothing.
		//
		// With several running it stops the newest, so a question asked by
		// mistake can be taken back without losing the long piece of work still
		// going underneath it. Pressing it again walks back through the rest.
		if m.busy() {
			m.stop(m.turns[len(m.turns)-1])
			return *m, nil
		}
		m.replyTo = ""
		return *m, nil

	case "ctrl+d":
		if !m.busy() && m.input.Value() == "" {
			m.quit = true
			return *m, tea.Quit
		}

	case "ctrl+o":
		// One key cycles one sub-agent's window closed → preview → expanded →
		// closed. Which sub-agent is a separate question, answered by ←/→:
		// cycling size and selection through the same key meant six presses to
		// see the third of three, and no way back to the first. Doing nothing
		// when none is running is deliberate — the key means "show me the
		// sub-agent", and inventing something for it when there isn't one would
		// only surprise.
		switch {
		case len(m.subs) == 0:
			// Nothing running.
		case m.watching == "":
			// Closed → preview of the first running sub-agent.
			m.watching = m.subs[0].id
			m.expanded = false
		case !m.expanded:
			// Preview → expanded window (steps and, once done, the answer).
			m.expanded = true
		default:
			m.watching = ""
			m.expanded = false
		}
		// The panel takes its rows from the transcript, so what is visible
		// changes under the reader; keep the newest end in view.
		m.clampScroll()
		return *m, nil

	// ←/→ move between sub-agents while a panel is open, keeping its size: with
	// several running, "which one" is the question actually being asked, and the
	// arrows are where a reader looks for it.
	//
	// Only with an empty input, and only with a panel open on more than one
	// sub-agent. Moving the cursor through what is being typed is the arrows'
	// first job and it must not become unreliable; an empty line has no cursor
	// to move, so there is nothing to take away.
	case "left", "right":
		if m.watched() == nil || len(m.subs) < 2 || m.input.Value() != "" {
			break
		}
		step := 1
		if msg.String() == "left" {
			step = -1
		}
		for j, s := range m.subs {
			if s.id == m.watching {
				// Wraps, so the arrows are a ring rather than a dead end at
				// either edge.
				m.watching = m.subs[(j+step+len(m.subs))%len(m.subs)].id
				break
			}
		}
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
		// Sends whether or not anything is running. A turn already in flight is
		// blocked on a tool result it asked for and cannot take a second
		// question, so this one is answered beside it rather than queued behind
		// it — which is the point of being able to set long work going and
		// carry on talking.
		//
		// The exception is a compaction, which is rewriting the very transcript
		// a new turn would be forked from. It takes one model call, so waiting
		// is brief, and the text is left in the input rather than swallowed.
		if m.compacting() {
			m.add(errStyle.Render("✗ wait for the conversation to finish compacting"))
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
	if last := m.entries[len(m.entries)-1]; last.blankLine() && !last.rule {
		return
	}
	m.push(entry{})
}

// openTurn marks the start of an exchange: a dim rule carrying the time, then
// the question against a coloured bar. The rule is the main thing that makes a
// long conversation scannable — it is where the eye lands when scrolling back.
// t names the turn being opened, so a question asked while another is being
// answered carries the same mark as the reply it will get.
func (m *Model) openTurn(t *turn, text, quote string) {
	m.blank()
	m.newBlock()
	m.push(entry{rule: true, stamp: time.Now().Format("15:04"), turn: t.seq})
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
	// The bar already colours the question, so the mark goes outside it rather
	// than replacing it: what makes a question a question is worth keeping, and
	// which answer it belongs to is the extra thing being said.
	bar, pad := barStyle.Render("▌ "), ""
	if t.tag != "" {
		bar, pad = t.tag+" "+bar, t.tag+" "
	}
	for i, l := range strings.Split(text, "\n") {
		first := bar
		if i > 0 {
			first = pad + barStyle.Render("▌ ")
		}
		m.push(entry{
			kind:  kindUser,
			first: first,
			cont:  first,
			text:  l,
			style: &userStyle,
			turn:  t.seq,
		})
	}
	// The gap under the question is opened by whatever comes next, via
	// pushKind — a blank line here would double it.
}

// grant records a "don't ask again" for the call being asked about.
//
// What it grants is deliberately narrow. For a command it is the program and
// its subcommand — "git commit *" from `git commit -m "..."` — because the
// argument is what varies between calls while the verb is what the user
// actually agreed to. Granting the whole tool from one prompt would hand over
// far more than was on screen, and granting the exact string would be useless,
// since the next call differs by a filename.
func (m *Model) grant() {
	if m.ask == nil {
		return
	}
	target := permission.Target(json.RawMessage(m.ask.Args))
	pattern := grantPattern(m.ask.Name, target)
	m.ag.Permissions().Grant(m.ask.Name, pattern, permission.Allow)

	rule := m.ask.Name
	if pattern != "" {
		rule += " " + pattern
	}
	m.add(dimStyle.Render("  will not ask again this session for " + rule))
}

// grantPattern works out what a single approval should cover.
//
// A path grants the file, not its directory: approving one edit to main.go says
// nothing about the rest of the tree. A command grants the verb, since that is
// the part the user read and the part that stays the same.
func grantPattern(tool, target string) string {
	if target == "" {
		return ""
	}
	if tool != "bash" {
		return target
	}
	fields := strings.Fields(target)
	if len(fields) == 0 {
		return ""
	}
	// Two words where the second is a subcommand rather than an option or a
	// path: "git commit", "npm test", "go build". Anything else keeps one.
	if len(fields) > 1 && !strings.HasPrefix(fields[1], "-") && !strings.ContainsAny(fields[1], "/.") {
		return fields[0] + " " + fields[1] + " *"
	}
	return fields[0] + " *"
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
	// A turn forks the agent, and a fork copies the registry — so the MCP tools
	// have to be in it before this runs. Startup defers connecting past the
	// first frame, and this is where that debt is settled.
	//
	// Waited on in a command rather than inline, because this is the UI
	// goroutine: blocking it would freeze the terminal until the slowest server
	// answered — no repaint, no scrolling, and no ctrl+c, which is exactly when
	// someone would reach for it. The message comes back as readyMsg and send
	// runs again from the top. Checked before anything is allocated so the
	// re-entry is a clean retry rather than a half-built turn.
	if m.ready != nil {
		select {
		case <-m.ready:
			m.ready = nil
		default:
			return *m, waitReady(m.ready, text)
		}
	}
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
	// A skill named in the message brings its instructions with it. Expanded on
	// the way out only: the transcript keeps the short reference the user typed,
	// which is the point of having one — a page of house style pasted into the
	// screen every time it is used would bury the conversation it is about.
	sent, skills := m.cfg.ExpandSkills(sent)
	// Staged attachments first, then any image path written into the message
	// itself. Both go with this one turn and nothing carries over.
	images := append(m.takeAttached(), m.detectImagePaths(text)...)
	ctx, cancel := context.WithCancel(context.Background())
	m.turnSeq++
	// Every turn answers in a fork, including one asked with nothing else
	// running. m.ag is the conversation of record and never runs: it is read
	// and written only here on the UI goroutine, at fork and at merge, so a
	// question asked while an answer is arriving cannot race with it.
	//
	// Letting a lone turn run on m.ag directly and forking only from the second
	// looked cheaper and was wrong — forking reads the transcript that the
	// first turn is in the middle of appending to, which is exactly the race
	// this is all here to avoid.
	t := &turn{
		seq:    m.turnSeq,
		ag:     m.ag.Fork(),
		cancel: cancel,
		events: make(chan agent.Event, 64),
		start:  time.Now(),
	}
	if m.busy() {
		// Two answers are about to arrive into one transcript, so from here on
		// every line says which one it belongs to — including the turn that was
		// running on its own until a moment ago.
		t.tag = turnMark(t.seq)
		for _, x := range m.turns {
			if x.tag == "" {
				x.tag = turnMark(x.seq)
				// Its earlier lines were written when it had the transcript to
				// itself and carry no mark. They are marked now, going back, so
				// the whole answer reads as one rather than as an unattributed
				// beginning and a marked rest.
				m.remark(x)
			}
		}
	}
	m.turns = append(m.turns, t)
	m.frame = 0
	// Sending jumps back to the newest output: the reply is what you want to
	// see, wherever you had scrolled to.
	m.scroll = 0
	// The input stays focused and enter keeps working while the model works, so
	// the next question can be asked without waiting for this one.
	go t.ag.RunWith(ctx, sent, images, t.events)

	m.openTurn(t, text, quote)
	// What went with the question, shown under it: the transcript is the only
	// record of a turn, and an attachment that appears nowhere in it leaves no
	// way to tell an image that was sent from one that failed to load.
	for _, img := range images {
		m.pushTurn(t, entry{
			kind: kindNotice,
			text: dimStyle.Render(fmt.Sprintf("  %s %s  %s", imageMark, img.Name, byteSize(len(img.Data)))),
		})
	}
	// Said out loud, because everything else about the expansion is invisible:
	// the transcript shows the reference rather than the instructions, so
	// without this there is no way to tell a skill that was pulled in from a
	// name that was misspelled and quietly went to the model as prose.
	if len(skills) > 0 {
		m.pushTurn(t, entry{
			kind: kindNotice,
			text: dimStyle.Render("  " + skillMark + " " + strings.Join(skills, ", ")),
		})
	}
	return *m, tea.Batch(waitFor(t, t.events), tick())
}

func (m *Model) onEvent(t *turn, ev agent.Event) (tea.Model, tea.Cmd) {
	// A cancelled turn can have events already in flight behind the
	// cancellation. Its state is gone, so there is nothing to write them into.
	if m.live(t) == nil {
		return *m, nil
	}
	next := waitFor(t, t.events)

	switch e := ev.(type) {

	case agent.TextDelta:
		// Once the real answer starts, the thinking has served its purpose.
		t.think = ""
		t.pending += e.Text
		var lines []string
		lines, t.pending = splitLines(t.pending)
		m.addText(t, lines)
		return *m, next

	case agent.ReasoningDelta:
		t.think = t.think + e.Text
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
			s.add(s.paint("⏺ "+e.Name) +
				dimStyle.Render("  "+summarize(e.Args, max(10, m.innerWidth()-len(e.Name)-14))))
			// The same window the transcript draws for the main conversation, so
			// the panel shows the file the sub-agent is editing rather than only
			// its name. Built here, at ToolStart, for the same reason the
			// transcript does: the tool runs next, and by ToolEnd the old contents
			// are gone, so there would be nothing to diff against. The empty
			// indent keeps the box flush against the panel's own border instead of
			// hanging under a tool marker that is not there.
			if c := codeWindow(m.root, e.Name, e.Args, ""); c != nil {
				s.codes = append(s.codes, c)
				// The steps ahead are already trimmed to bound the lines slice;
				// the windows keep pace so the panel does not grow without end
				// over a long-running sub-agent.
				if len(s.codes) > subViewRows*8 {
					s.codes = s.codes[len(s.codes)-subViewRows*8:]
				}
			}
			return *m, next
		}
		m.settle(t)
		// The previous step is now in the past; drop it so this one takes its
		// place rather than piling another line onto the transcript.
		m.dropLive()
		pad := strings.Repeat("  ", 1+e.Depth)
		m.pushTurn(t, entry{
			kind:     kindWork,
			toolLive: true,
			first:    pad + workStyle.Render("⏺ "),
			cont:     pad + "    ",
			text:     workStyle.Render(e.Name) + workDim.Render("  "+summarize(e.Args, max(20, m.innerWidth()-len(e.Name)-10))),
		})
		// Built here rather than at ToolEnd because the window is a picture of
		// the file as it was: the tool runs next, and by the time the result
		// arrives the old contents are gone.
		if c := codeWindow(m.root, e.Name, e.Args, pad+"  "); c != nil {
			m.pushTurn(t, entry{kind: kindWork, toolLive: true, code: c})
		}
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
			s.add("  " + s.paint("↳ ") + style.Render(text))
			return *m, next
		}
		pad := strings.Repeat("  ", 2+e.Depth)
		m.pushTurn(t, entry{
			kind:     kindWork,
			toolLive: true,
			first:    pad + workDim.Render("↳ "),
			cont:     pad + "  ",
			text:     style.Render(text),
		})
		return *m, next

	case agent.TaskStart:
		m.comp.Tasks++
		m.settle(t)
		// The panel opens here and takes the sub-agent's steps from now on. It
		// is tagged with the turn that delegated it, so it closes when that
		// turn ends rather than when any turn does. The colour is assigned from
		// the sub-agent palette and reused on the panel, its live lines, and the
		// two transcript markers, so several running at once are told apart by
		// colour rather than by where they sit.
		s := &subView{id: e.ID, desc: e.Description, owner: t, color: nextSubColor(), start: time.Now()}
		m.subs = append(m.subs, s)
		m.pushTurn(t, entry{
			kind:  kindWork,
			first: "  " + s.paint("◆ "),
			cont:  "    ",
			text:  s.paint("task") + workDim.Render("  "+e.Description),
		})
		return *m, next

	case agent.TaskEnd:
		// The panel does not close here: the sub-agent has reported back and
		// its answer is worth keeping in the panel, not dropping the instant
		// it finishes. The transcript still gets its one lightweight line -- the
		// caller wanted an answer, not a record of the working-out -- but it is
		// recoloured to this sub-agent so it is told from its siblings.
		if s := m.sub(e.ID); s != nil {
			s.done = true
			s.stop = time.Now()
			s.answer = e.Summary
			s.err = e.Err
		}
		text, style := fmt.Sprintf("returned %d chars after %d steps", len(e.Summary), e.Steps), workDim
		if e.Err != nil {
			text, style = e.Err.Error(), workErr
		}
		mark := "↳ "
		if s := m.sub(e.ID); s != nil {
			mark = s.paint("↳ ")
		}
		m.pushTurn(t, entry{
			kind:  kindWork,
			first: "    " + mark,
			cont:  "      ",
			text:  style.Render(text),
		})
		return *m, next

	case agent.Usage:
		m.ctxTokens = e.Total
		// Context is the one thing every provider charges in, so it is what the
		// companion grows on — whichever model happened to serve this request.
		if before, after := m.comp.Feed(m.ref, int64(e.Total)); after > before {
			// Over five hundred levels the number moves often and the name
			// rarely, so they are not worth the same interruption. A plain
			// level is a dim aside; a new title — or the top of the ladder —
			// is the thing to look up from the conversation for.
			mark, text := dimStyle.Render("★ "), dimStyle.Render(fmt.Sprintf("level %d", after))
			switch {
			case after >= companion.MaxLevel:
				mark = levelStyle.Render("✦ ")
				text = levelStyle.Render(fmt.Sprintf("level %d — %s, fully grown", after, m.comp.Title())) +
					dimStyle.Render("  /prestige to begin again")
			case companion.TitleForLevel(before) != companion.TitleForLevel(after):
				mark = levelStyle.Render("★ ")
				text = levelStyle.Render(fmt.Sprintf("level %d — %s", after, m.comp.Title())) +
					dimStyle.Render("  /companion")
			}
			m.pushTurn(t, entry{
				kind:  kindNotice,
				first: "  " + mark,
				cont:  "    ",
				text:  text,
			})
		}
		// Warn before the window overflows rather than after: once the server
		// starts truncating, the model loses the question it was asked and
		// begins answering something else entirely.
		if m.contextNearlyFull() && !t.warnedFull {
			t.warnedFull = true
			m.add(errStyle.Render(fmt.Sprintf(
				"⚠ context %d%% full — replies will degrade. /compact to summarise it, /clear to start over.",
				m.ctxTokens*100/m.contextLimit())))
		}
		return *m, next

	case agent.Approval:
		m.settle(t)
		m.ask = &e
		return *m, next

	case agent.ModeChanged:
		return *m, next

	case agent.Switched:
		m.ref = e.To
		t.warnedFull = false
		// A model that refused outright will refuse again next session, so
		// leaving it as the default means starting every session with the same
		// failure. The replacement is adopted only once it has actually
		// answered, though — adopting it here churned the default through a
		// whole ladder of models that were failing too.
		if t.rejected && e.From == m.chosen {
			t.adopt = e.To
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
			t.rejected = true
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
		t.warnedFull = false
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
		t.think = ""
		m.comp.Turns++
		// The turn finished, so whatever produced it works. Now it is worth
		// remembering in place of the model that refused.
		if t.adopt != "" {
			from, to := m.chosen, t.adopt
			t.adopt, t.rejected, m.chosen = "", false, to
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
		m.flush(t)
		// Reasoning models sometimes finish a turn having written only
		// thinking, leaving nothing to show. Say so rather than returning to
		// the prompt as if nothing happened.
		if !t.inText {
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
		t.think = ""
		m.flush(t)
		msg := e.Err.Error()
		if errors.Is(e.Err, context.Canceled) {
			msg = "cancelled"
		}
		m.blank()
		m.add(errStyle.Render("✗ " + msg))
		// Not saved here. The turn is about to end, and what it did reaches the
		// conversation only when it is merged back — saving now would write out
		// a transcript missing the very exchange that just failed.
		return *m, next
	}

	return *m, next
}

// addText styles a turn's assistant lines as markdown and appends them, opening
// the block with a blank line so prose is set off from the tool calls above it.
func (m *Model) addText(t *turn, lines []string) {
	if len(lines) == 0 {
		return
	}
	if !t.inText {
		t.inText = true
		// The reply is its own message, so a click anywhere in it quotes all
		// of it rather than the one line under the pointer.
		m.newBlock()
		t.block = m.blockSeq
	}
	for _, l := range lines {
		e := t.md.entry(l)
		e.kind = kindReply
		// Only the first line can need a gap opening before it; once inside a
		// reply, consecutive lines are the same kind and stay together.
		m.pushTurn(t, e)
	}
}

// flush turns a turn's buffered text into transcript lines, including a final
// line with no trailing newline.
func (m *Model) flush(t *turn) {
	lines, rest := splitLines(t.pending)
	if strings.TrimSpace(rest) != "" {
		lines = append(lines, rest)
	}
	t.pending = ""
	m.addText(t, lines)
}

// settle closes off whatever prose a turn had in flight, which is what every
// non-text event has to do before writing a line of its own.
func (m *Model) settle(t *turn) {
	t.think = ""
	m.flush(t)
	t.inText = false
}

// pushTurn writes a line on a turn's behalf: its gutter mark in front, and its
// own block so a click selects the whole reply even when another turn's output
// landed in the middle of it.
//
// It is also where interleaving is kept readable. Two turns writing
// alternate lines into one transcript would otherwise run together; the mark
// says which answer a line belongs to, and a change of speaker opens a gap the
// way a change of kind does.
func (m *Model) pushTurn(t *turn, e entry) {
	if t.tag != "" {
		e.first = t.tag + " " + e.first
		e.cont = t.tag + " " + e.cont
		if m.lastTurn() != t.seq {
			m.push(entry{})
		}
	}
	e.block = t.block
	e.turn = t.seq
	m.pushKind(e)
}

// remark puts a turn's gutter in front of the lines it wrote before it had one.
// A turn that starts alone is unmarked — there is nothing to tell it apart
// from — and acquires a mark only when a second turn joins it, so its opening
// lines have to be caught up.
func (m *Model) remark(t *turn) {
	for i := range m.entries {
		e := &m.entries[i]
		if e.turn != t.seq || strings.HasPrefix(e.first, t.tag) {
			continue
		}
		// A rule spans the width and is redrawn on resize; marking it would put
		// the gutter inside the line rather than in front of it.
		if e.rule {
			continue
		}
		e.first = t.tag + " " + e.first
		e.cont = t.tag + " " + e.cont
	}
	m.rewrap()
}

// lastTurn is which turn wrote the last non-blank line, zero when none did.
func (m Model) lastTurn() int {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if e := m.entries[i]; !e.blankLine() {
			return e.turn
		}
	}
	return 0
}

// midTurn are the commands that cannot run while anything is being answered,
// because each rewrites the conversation a live turn is in the middle of
// appending to — clearing it, replacing it with a saved one, summarising it, or
// moving the files underneath it.
//
// This list used to be unnecessary: enter did nothing mid-turn, so no command
// could be reached either. Now that a question can be asked while another is
// running, commands arrive at the same time as turns and have to say so
// themselves. /compact and /branch already did, and this is the rest of them.
var midTurn = map[string]string{
	"/clear":   "/clear",
	"/new":     "/clear",
	"/resume":  "/resume",
	"/compact": "/compact",
	"/branch":  "/branch",
	"/br":      "/branch",
}

func (m *Model) command(line string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(line)
	if name, ok := midTurn[fields[0]]; ok && m.busy() {
		m.add(errStyle.Render("✗ " + name + " has to wait for the turn to finish"))
		return *m, nil
	}
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
		// Refused mid-turn by midTurn above: the agent has tools running
		// against these files, and moving the working tree underneath it would
		// have it read one branch and write to another.
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
		// Driven as a turn, so it gets the spinner, esc to cancel and the busy
		// state without any machinery of its own. Refused while anything else
		// is running — it rewrites the very messages a turn is appending to —
		// which is why it can use the conversation's own agent rather than a
		// fork, and why its turn carries no agent to merge back.
		focus := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		ctx, cancel := context.WithCancel(context.Background())
		m.turnSeq++
		t := &turn{seq: m.turnSeq, cancel: cancel, events: make(chan agent.Event, 8), start: time.Now()}
		m.turns = append(m.turns, t)
		m.frame = 0
		m.scroll = 0
		go m.ag.RunCompact(ctx, focus, t.events)

		m.blank()
		note := "⋯ compacting the conversation…"
		if focus != "" {
			// Quoted rather than folded into the sentence: it is whatever the
			// user typed, and no phrasing reads well around all of it.
			note = fmt.Sprintf("⋯ compacting the conversation, keeping %q in view…", focus)
		}
		m.add(askStyle.Render(note))
		return *m, tea.Batch(waitFor(t, t.events), tick())

	case "/companion", "/comp":
		for _, e := range m.companionRows() {
			m.push(e)
		}
		return *m, nil

	case "/prestige":
		// Deliberately a command rather than something that happens on its own.
		// Reaching the top is the achievement; starting over is a choice made
		// after it, not a punishment for arriving.
		if !m.comp.AtTop() {
			togo := companion.TokensForLevel(companion.MaxLevel) - m.comp.Tokens
			m.add(errStyle.Render(fmt.Sprintf("✗ /prestige waits for level %d — %s tokens to go",
				companion.MaxLevel, humanTokens(int(togo)))))
			return *m, nil
		}
		m.comp.Ascend()
		// Saved now rather than at the end of the next turn: an ascent the user
		// asked for should survive closing the terminal straight afterwards.
		if err := m.comp.Save(); err != nil {
			m.add(errStyle.Render("✗ could not save the companion: " + err.Error()))
			return *m, nil
		}
		m.add(levelStyle.Render(fmt.Sprintf("✦ ascended — climb %d begins at level 1, %s",
			m.comp.Prestige+1, m.comp.Title())))
		m.add(dimStyle.Render(fmt.Sprintf("  %s lifetime tokens kept  ·  /companion",
			humanTokens(int(m.comp.Lifetime)))))
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
		// Subcommands first, so a server can still be called "auth" without the
		// listing becoming unreachable — "/mcp auth" with no name is the verb,
		// "/mcp auth <name>" acts on a server.
		if len(fields) > 2 {
			switch fields[1] {
			case "auth", "login":
				return m.mcpAuth(fields[2])
			case "logout":
				return m.mcpLogoutServer(fields[2])
			}
		}
		if len(fields) > 1 {
			return *m, m.showMCPServer(fields[1])
		}
		// No argument: offer the configured servers in the same overlay
		// /model and /branch use, so picking one is the same gesture. With
		// none defined there is nothing to choose between, so fall back to
		// the listing, which says how to define one.
		if len(m.cfg.MCP) == 0 {
			return *m, m.showMCP()
		}
		counts := map[string]int{}
		if m.mcp != nil {
			counts = m.mcp()
		}
		fails := map[string]string{}
		if m.mcpFails != nil {
			fails = m.mcpFails()
		}
		lazy := m.mcpLazy != nil && m.mcpLazy()
		m.pick = newMCPPicker(m.cfg, counts, fails, lazy)
		return *m, nil

	case "/image", "/img":
		if len(fields) < 2 {
			m.add(errStyle.Render("✗ /image needs a path"))
			return *m, nil
		}
		// Everything after the command, so a path with spaces in it works
		// without quoting — screenshots are full of them.
		m.attachImage(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0])))
		return *m, nil

	case "/images":
		if len(fields) > 1 && fields[1] == "clear" {
			n := len(m.attached)
			m.attached = nil
			m.add(dimStyle.Render(fmt.Sprintf("  dropped %d %s", n, plural(n, "image", "images"))))
			return *m, nil
		}
		if len(m.attached) == 0 {
			m.add(dimStyle.Render("  no images attached"))
			return *m, nil
		}
		for _, img := range m.attached {
			m.add(dimStyle.Render(fmt.Sprintf("  %s %s  %s", imageMark, img.Name, byteSize(len(img.Data)))))
		}
		return *m, nil

	case "/paste":
		// Reading the clipboard shells out to a helper, which can block on a
		// slow compositor. Off the UI goroutine, so the terminal stays live.
		return *m, func() tea.Msg {
			img, err := attach.Clipboard(context.Background())
			return pastedMsg{img: img, err: err}
		}

	case "/skills":
		m.showSkills()
		return *m, nil

	case "/permissions", "/perms":
		m.showPermissions()
		return *m, nil

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
		rows = append(rows, strings.Split(
			w.render(m.innerWidth(), f, i, len(m.subs), w.height(m.expanded, m.height-chromeLines-1, m.innerWidth()), m.expanded),
			"\n")...)
	}
	if m.pick != nil {
		// What counts as "current" depends on what is being chosen: the dot
		// marks the model in use, or the branch checked out.
		current := m.ref
		switch m.pick.kind {
		case pickBranch:
			current = m.branch
		case pickMCP:
			// Nothing is "in use" among servers: they are all on at once.
			current = ""
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
			dimStyle.Render(" "+summarize(m.ask.Args, max(10, m.innerWidth()-56))) +
			askStyle.Render("   y") + dimStyle.Render(" approve  ") +
			askStyle.Render("a") + dimStyle.Render(" always  ") +
			askStyle.Render("n") + dimStyle.Render(" decline")
	} else if len(m.subs) > 0 && m.watching == "" {
		// Sub-agents are running with the panel closed. This says so on the row
		// that is already there, rather than taking rows from the transcript to
		// show working-out nobody asked to see.
		f := spinnerFrames[m.frame%len(spinnerFrames)]
		status = subsHint(m.subs, f, m.innerWidth())
	} else if m.busy() {
		f := spinnerFrames[m.frame%len(spinnerFrames)]
		// Something is being answered. The thinking is not shown, so this only
		// reports that work is in flight, how much, and what to press to stop.
		//
		// The count is the thing worth saying when there are several: it is the
		// difference between "still going" and "I started three of these", and
		// it is the only sign on the screen that the question just typed went
		// somewhere rather than nowhere.
		work, tail := "working", "  esc to cancel"
		if n := len(m.turns); n > 1 {
			work = fmt.Sprintf("working on %d turns", n)
			tail = "  esc cancels the newest  ·  ctrl+c all"
		}
		// How long the oldest live turn has been going. With several running
		// that is the one worth reporting: it is the one closest to being
		// stuck, and the newer ones are bounded by it.
		if el := m.elapsed(); el != "" {
			work += " · " + el
		}
		status = spinStyle.Render(f) + " " +
			dimStyle.Render(ansi.Truncate(work, max(10, m.innerWidth()-len(tail)-6), "…")) +
			dimStyle.Render(tail)
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
	// Staged images, named above the input for the same reason the reply is:
	// what the next message will carry should be visible while typing it.
	if len(m.attached) > 0 {
		names := make([]string, 0, len(m.attached))
		for _, img := range m.attached {
			names = append(names, img.Name)
		}
		rows = append(rows, dimStyle.Render(imageMark+" ")+
			quoteStyle.Render(ansi.Truncate(strings.Join(names, ", "), max(10, m.innerWidth()-30), "…"))+
			dimStyle.Render("  /images clear to drop"))
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
			cur.Y += w.height(m.expanded, m.height-chromeLines-1, m.innerWidth())
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
				cur.Y += w.height(m.expanded, m.height-chromeLines-1, m.innerWidth())
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
		if len(m.attached) > 0 {
			below++
		}
		if w := m.watched(); w != nil {
			below += w.height(m.expanded, m.height-chromeLines-1, m.innerWidth())
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
		if m.comp.Prestige > 0 {
			companionLine += fmt.Sprintf("  ✦%d", m.comp.Prestige)
		}
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
	if m.busy() {
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
	star := fmt.Sprintf("★%d", m.comp.Level())
	if m.comp.Prestige > 0 {
		// Without this a companion that has climbed twice looks like one that
		// has just started.
		star += fmt.Sprintf("✦%d", m.comp.Prestige)
	}
	parts = append(parts, levelStyle.Render(star))

	left := strings.Join(parts, sep)

	// A newer release, when one was found in the background, sits on the right
	// of the same row — below the input, where the eye lands — so it reads as
	// a quiet aside rather than a takeover. The reference line yields room to
	// it, dropping from the right first so the mode is the last thing to go.
	if m.update != "" {
		hint := okStyle.Render(fmt.Sprintf("↑ v%s — curl -fsSL https://raunen.sh | sh", m.update))
		// Width of the hint plus a gap it needs on a narrow screen to stay
		// readable. If there is no room, the reference line wins — the hint
		// can wait for a wider terminal, the bar cannot.
		room := m.innerWidth() - ansi.StringWidth(hint) - 2
		if room > 10 {
			left = ansi.Truncate(left, room, "…")
		}
		pad := m.innerWidth() - ansi.StringWidth(left) - ansi.StringWidth(hint)
		if pad < 0 {
			pad = 0
		}
		return left + strings.Repeat(" ", pad) + hint
	}
	return ansi.Truncate(left, m.innerWidth(), "…")
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

// humanTokens renders a token count compactly: 940, 1.2k, 24k, 2.4M, 2.5B. The
// companion deals in millions by the middle of its ladder and billions at the
// top of it, so "2400k" would be a poor way to say either.
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
	case n < 1_000_000_000:
		return fmt.Sprintf("%dM", n/1_000_000)
	// The top of the ladder is billions of tokens, and "2490M" is a worse way
	// to say 2.5 billion than the extra unit is.
	case n < 10_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	default:
		return fmt.Sprintf("%dB", n/1_000_000_000)
	}
}

// humanDuration renders elapsed time as 8s, 2m 13s, 1h 04m. Seconds are dropped
// past an hour: at that length the second is noise, and a field that stops
// changing every tick reads as progress rather than a stopwatch.
//
// Under a second renders as 0s rather than empty, so the field appears the
// moment work starts instead of blinking into existence a second later.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
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

// waitFor blocks on one event from a turn's channel. The turn travels with the
// message so the model can route it even when several are streaming at once —
// an index would go stale the moment an earlier turn finished.
func waitFor(t *turn, ch chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamDoneMsg{turn: t}
		}
		return eventMsg{turn: t, ev: ev}
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
