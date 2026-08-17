package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// command is one slash command as it appears to the user. The table below is
// the single description of the command surface: /help prints it and the
// completion list filters it, so the two can never drift apart.
//
// It deliberately says nothing about behaviour — dispatch stays in command(),
// where the cases can return commands to the runtime.
type command struct {
	// name is the canonical spelling, the one completion fills in.
	name string
	// args is the argument sketch, empty when the command takes none. It is
	// what decides whether completing leaves a trailing space to type into.
	args string
	help string
	// aliases are accepted spellings that are not worth a row of their own.
	// They still match while typing, so /q finds /quit.
	aliases []string
}

var commands = []command{
	{name: "/model", args: "[provider/model]", help: "choose a model from a list, or switch directly"},
	{name: "/favourite", args: "[provider/model]", help: "pin or unpin a model for quick access", aliases: []string{"/fav"}},
	{name: "/status", help: "model, context, ladder, endpoints"},
	{name: "/companion", help: "your dragon's level and what fed it", aliases: []string{"/comp"}},
	{name: "/providers", help: "list configured endpoints"},
	{name: "/key", args: "<provider>", help: "add an api key"},
	{name: "/clear", help: "start a new session", aliases: []string{"/new"}},
	{name: "/sessions", help: "list saved sessions"},
	{name: "/resume", args: "<id>", help: "pick up a saved session"},
	{name: "/mcp", help: "list connected MCP servers and their tools"},
	{name: "/help", help: "list the commands"},
	{name: "/quit", help: "exit", aliases: []string{"/exit", "/q"}},
}

// keyHelp is the other half of /help: what the keyboard does, which has no
// completion because there is nothing to type.
var keyHelp = []string{
	"esc                      cancel the running turn",
	"pgup/pgdn, shift+↑/↓     scroll the transcript",
	"shift+enter              newline without sending",
	"tab                      cycle auto / accept edits / plan",
	"↑/↓ then tab             complete a / command or an @ path",
	"@                        mark a file or folder for the model",
}

// label is the command as it is offered: name and arguments together.
func (c command) label() string {
	if c.args == "" {
		return c.name
	}
	return c.name + " " + c.args
}

// matches reports whether what has been typed so far could still become this
// command. Prefix matching rather than the picker's fuzzy search: these names
// are short and few, and a list that reorders under the cursor while typing is
// worse than one that only ever shrinks.
func (c command) matches(typed string) bool {
	if strings.HasPrefix(c.name, typed) {
		return true
	}
	for _, a := range c.aliases {
		if strings.HasPrefix(a, typed) {
			return true
		}
	}
	return false
}

// item is one row of the completion list: what it says, and what it puts in
// the input when it is taken.
type item struct {
	// insert replaces the token being completed.
	insert string
	// space asks for a space after it, which is how a finished completion is
	// told from one that carries on — a file ends the mention, a directory is
	// something to keep typing into.
	space  bool
	label  string
	detail string
}

// suggest is the completion list above the input, open while a / command or an
// @ mention is being typed. Unlike the model chooser it never takes the
// keyboard: typing carries on going to the input, and only the keys that would
// otherwise do nothing useful — the arrows and tab — are borrowed.
type suggest struct {
	items  []item
	cursor int
	// kind decides what enter means. A half-typed command completes, because
	// sending it could only earn an "unknown command"; a mention does not,
	// because the message it sits in is finished and wants sending.
	kind sugKind
	// token is the word being completed, which is what an accepted item
	// replaces.
	token string
	// scanning marks a file list that has been asked for and not yet arrived,
	// so the popup can say so rather than appearing to have found nothing.
	scanning bool
}

type sugKind int

const (
	sugCommand sugKind = iota
	sugFile
)

// suggestRows is how many completions are visible at once. The list comes out
// of the transcript's height, so it stays small; the rest scrolls.
const suggestRows = 6

// mention is what starts a file or folder reference.
const mention = "@"

// lastToken is the word the cursor is on, which for a prompt being typed is
// the last one. Completion works on the end of the input rather than wherever
// the caret happens to be: a mention is written as it is reached, and reading
// an exact caret offset back out of a wrapped textarea is not reliable enough
// to edit someone's text with.
func lastToken(line string) string {
	if i := strings.LastIndexAny(line, " \t\n"); i >= 0 {
		return line[i+1:]
	}
	return line
}

// suggestFor builds the list for what is currently in the input, or nil when
// there is nothing to offer. files may be nil, which is the state before the
// first scan has come back.
func suggestFor(line string, files *fileIndex) *suggest {
	token := lastToken(line)
	switch {
	// A command is the whole line or it is not a command: once a space has
	// been typed the user has moved on to the argument, and a slash further in
	// is prose.
	case strings.HasPrefix(line, "/") && token == line:
		return commandSuggest(line)
	case strings.HasPrefix(token, mention):
		return fileSuggest(token, files)
	}
	return nil
}

func commandSuggest(line string) *suggest {
	s := &suggest{kind: sugCommand, token: line}
	for _, c := range commands {
		if c.matches(line) {
			s.items = append(s.items, item{
				insert: c.name,
				// A command that takes arguments leaves the caret where the
				// argument goes; one that does not is left ready for enter.
				space:  c.args != "",
				label:  c.label(),
				detail: c.help,
			})
		}
	}
	if len(s.items) == 0 {
		// A typo has nothing to complete to. An empty box saying so would only
		// take rows off the transcript to report a non-event.
		return nil
	}
	return s
}

// fileSuggest offers paths for an @ mention. A bare @, or one ending in a
// slash, lists that directory — so the mention doubles as a way to look
// around; anything else is a query over the whole tree.
func fileSuggest(token string, files *fileIndex) *suggest {
	s := &suggest{kind: sugFile, token: token}
	if files == nil {
		s.scanning = true
		return s
	}

	q := strings.TrimPrefix(token, mention)
	var paths []string
	if q == "" || strings.HasSuffix(q, "/") {
		paths = files.children(q)
	} else {
		paths = files.search(q)
	}
	for _, p := range paths {
		isDir := strings.HasSuffix(p, "/")
		s.items = append(s.items, item{
			insert: mention + p,
			// A directory is a step on the way, so completing one leaves the
			// mention open and lists what is inside it.
			space: !isDir,
			label: mention + p,
		})
	}
	if len(s.items) == 0 {
		return nil
	}
	return s
}

// isCommand reports whether a line is already a command in full, alias
// included. That is what separates "run this" from "finish typing it".
func isCommand(line string) bool {
	for _, c := range commands {
		if c.name == line {
			return true
		}
		for _, a := range c.aliases {
			if a == line {
				return true
			}
		}
	}
	return false
}

func (s *suggest) move(d int) {
	if len(s.items) == 0 {
		return
	}
	s.cursor = (s.cursor + d + len(s.items)) % len(s.items)
}

func (s *suggest) selected() (item, bool) {
	if s.cursor < 0 || s.cursor >= len(s.items) {
		return item{}, false
	}
	return s.items[s.cursor], true
}

// height is the rows the list occupies, so the transcript above it can give up
// exactly that much and the input stays welded to the bottom.
func (s *suggest) height() int {
	if s.scanning {
		return 1
	}
	return min(len(s.items), suggestRows)
}

// render draws the list at the given width. No border: it sits directly on top
// of the input box and reads as part of it, and a frame would cost two of the
// rows it is trying to keep for the transcript.
func (s *suggest) render(width int) string {
	if s.scanning {
		return dimStyle.Render("  looking for files…")
	}

	// The label column is only padded out when there is something to line the
	// descriptions up against. Paths have none, and padding them would leave a
	// ragged margin of nothing down the middle of the list.
	labelW := 0
	for _, it := range s.items {
		if it.detail != "" {
			labelW = max(labelW, lipgloss.Width(it.label))
		}
	}
	labelW = min(labelW, max(10, width/2))

	// Scroll the window so the cursor stays visible.
	start := 0
	if s.cursor >= suggestRows {
		start = s.cursor - suggestRows + 1
	}
	end := min(len(s.items), start+suggestRows)

	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		it := s.items[i]
		// Rendered only when there is one: styling an empty string still emits
		// the escape codes around it.
		detail := ""
		if it.detail != "" {
			detail = dimStyle.Render("  " + it.detail)
		}
		label := padTo(it.label, labelW)
		if i == s.cursor {
			rows = append(rows, ansi.Truncate(pickerSelected.Render("❯ "+label)+detail, width, "…"))
			continue
		}
		rows = append(rows, ansi.Truncate("  "+label+detail, width, "…"))
	}

	// Say what is out of sight, or the arrows look like they do nothing: with
	// the cursor on the first row there is no other sign that the list scrolls.
	if hidden := len(s.items) - (end - start); hidden > 0 && len(rows) > 0 {
		tail := fmt.Sprintf("  +%d more", hidden)
		last := len(rows) - 1
		rows[last] = ansi.Truncate(rows[last], max(10, width-lipgloss.Width(tail)), "…") +
			dimStyle.Render(tail)
	}
	return strings.Join(rows, "\n")
}

// padTo widens a string to a column, measuring what will be displayed rather than
// what is stored.
func padTo(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}
