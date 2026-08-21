package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"raunen/internal/config"
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
	{name: "/branch", args: "[name]", help: "switch branch, or choose one from a list", aliases: []string{"/br"}},
	{name: "/status", help: "model, context, ladder, endpoints"},
	{name: "/companion", help: "your dragon's level and what fed it", aliases: []string{"/comp"}},
	{name: "/prestige", help: "start a new climb once your dragon is fully grown"},
	{name: "/providers", help: "list configured endpoints"},
	{name: "/key", args: "<provider>", help: "add an api key"},
	{name: "/compact", args: "[what to keep]", help: "summarise the conversation to win back context"},
	{name: "/clear", help: "start a new session", aliases: []string{"/new"}},
	{name: "/sessions", help: "list saved sessions"},
	{name: "/resume", args: "<id>", help: "pick up a saved session"},
	{name: "/image", args: "<path>", help: "attach an image to the next message", aliases: []string{"/img"}},
	{name: "/images", args: "[clear]", help: "list attached images, or drop them"},
	{name: "/paste", help: "attach the image on the clipboard"},
	{name: "/mcp", args: "[server]", help: "choose an MCP server to see its tools and config"},
	{name: "/skills", help: "list the skills you can reference with #"},
	{name: "/permissions", help: "what runs without asking", aliases: []string{"/perms"}},
	{name: "/help", help: "list the commands"},
	{name: "/quit", help: "exit", aliases: []string{"/exit", "/q"}},
}

// keyHelp is the other half of /help: what the keyboard does, which has no
// completion because there is nothing to type.
var keyHelp = []string{
	"enter                    ask — even while an answer is still arriving",
	"esc                      cancel the newest turn",
	"ctrl+c                   cancel everything running",
	"pgup/pgdn, shift+↑/↓     scroll the transcript",
	"shift+enter              newline without sending",
	"tab                      cycle auto / accept edits / plan",
	"↑/↓ then tab             complete a / command or an @ path",
	"@                        mark a file or folder for the model",
	"#                        pull a saved skill into the prompt",
	"ctrl+o                   watch the running sub-agent",
	"←/→                      switch between running sub-agents",
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
	// replaces. start and end are its bounds as rune offsets into the whole
	// input, so accepting can splice it out of the middle of a long prompt
	// rather than assuming it hangs off the end.
	token      string
	start, end int
	// scanning marks a file list that has been asked for and not yet arrived,
	// so the popup can say so rather than appearing to have found nothing.
	scanning bool
}

type sugKind int

const (
	sugCommand sugKind = iota
	sugFile
	sugSkill
	// sugMCP is an argument rather than a command, so enter sends the line as
	// it stands instead of completing it, the same as a mention.
	sugMCP
)

// suggestRows is how many completions are visible at once. The list comes out
// of the transcript's height, so it stays small; the rest scrolls.
const suggestRows = 6

// mention is what starts a file or folder reference.
const mention = "@"

// skillMark is what starts a skill reference. It is the config package's
// constant rather than a second copy of it: completion offers the reference and
// the config expands it, and a mark that differed between the two would offer
// something that then did nothing.
const skillMark = config.SkillMark

// tokenAt is the word being typed at the caret: the run of non-whitespace that
// ends where the caret is, given as rune offsets into text so a completion can
// be spliced back in without counting bytes.
//
// It stops at the caret rather than running on to the next space, so editing
// "@foo" in the middle of "see @foo here" completes what has been typed rather
// than the whole word. The alternative — take the word the caret is inside —
// reads better in a text editor, but here it would mean that putting the caret
// after the "u" of "@ui.go" and pressing tab silently ate ".go"; a completion
// that only ever grows what is to the left of the caret is the one people can
// predict.
func tokenAt(text string, caret int) (token string, start, end int) {
	r := []rune(text)
	caret = min(max(caret, 0), len(r))
	start = caret
	for start > 0 && !isTokenBreak(r[start-1]) {
		start--
	}
	return string(r[start:caret]), start, caret
}

func isTokenBreak(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }

// suggestFor builds the list for the token at the caret, or nil when there is
// nothing to offer. caret is a rune offset into text. files may be nil, which
// is the state before the first scan has come back, and so may cfg: skills are
// an optional file, so nothing here may assume there are any.
func suggestFor(text string, caret int, files *fileIndex, cfg *config.Config) *suggest {
	token, start, end := tokenAt(text, caret)
	switch {
	// A command has to open a line of its own. That is looser than the old
	// "the slash is the whole input", so a command can be typed on a fresh
	// line under a long prompt, but no looser: a slash anywhere else is prose
	// or a path — "and/or", "src/main.go", "/model openai/gpt" — and a list
	// popping up over the transcript for every one of those would be worse
	// than never offering one at all. Leading whitespace does not count as the
	// start of a line either; indented text is code being quoted far more
	// often than it is a command.
	// An argument to /mcp completes to a server name. This is checked before
	// the command case because by then the slash is behind a space and the
	// token being completed is the name, not the command.
	case mcpArgAt(text, start):
		return mcpSuggest(token, start, end, cfg)
	case strings.HasPrefix(token, "/") && atLineStart(text, start):
		return commandSuggest(token, start, end)
	case strings.HasPrefix(token, mention):
		return fileSuggest(token, start, end, files)
	case strings.HasPrefix(token, skillMark):
		return skillSuggest(token, start, end, cfg)
	}
	return nil
}

// atLineStart reports whether a rune offset is the first character of a
// logical line.
func atLineStart(text string, off int) bool {
	if off == 0 {
		return true
	}
	r := []rune(text)
	return off <= len(r) && r[off-1] == '\n'
}

func commandSuggest(line string, start, end int) *suggest {
	s := &suggest{kind: sugCommand, token: line, start: start, end: end}
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
func fileSuggest(token string, start, end int, files *fileIndex) *suggest {
	s := &suggest{kind: sugFile, token: token, start: start, end: end}
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

// mcpArgAt reports whether the token starting at off is the argument of a /mcp
// on the same line — that is, "/mcp " and then a single word. A second word is
// not an argument this command has, so completion stops rather than offering
// server names forever.
func mcpArgAt(text string, off int) bool {
	r := []rune(text)
	if off > len(r) {
		return false
	}
	// Back up to the start of the line the token sits on.
	ls := off
	for ls > 0 && r[ls-1] != '\n' {
		ls--
	}
	before := string(r[ls:off])
	rest := strings.TrimPrefix(before, "/mcp")
	if rest == before {
		return false
	}
	// Everything between the command and the caret must be the one space that
	// separates them; anything else is a second argument or prose.
	return rest != "" && strings.TrimLeft(rest, " ") == ""
}

// mcpSuggest offers the configured server names as arguments to /mcp, so the
// set of servers can be seen from the input line the way commands and skills
// can, rather than only after running the command.
func mcpSuggest(token string, start, end int, cfg *config.Config) *suggest {
	if cfg == nil {
		return nil
	}
	s := &suggest{kind: sugMCP, token: token, start: start, end: end}
	q := strings.ToLower(token)
	for _, name := range cfg.MCPNames() {
		if !strings.HasPrefix(strings.ToLower(name), q) {
			continue
		}
		def := cfg.MCP[name]
		// The detail says where the server comes from, which is what
		// distinguishes two names in a list that is otherwise just words.
		detail := def.Command
		if def.Type == "http" {
			detail = def.URL
		}
		s.items = append(s.items, item{
			insert: name,
			space:  true,
			label:  name,
			detail: detail,
		})
	}
	if len(s.items) == 0 {
		return nil
	}
	return s
}

// skillSuggest offers the skills whose names carry on from what has been typed.
// A bare # lists all of them, which is what makes the mark a way to remember
// what has been defined rather than something only useful once the name is
// known.
//
// Prefix matching, like the commands and unlike the paths: a set of skills is a
// short hand-written list, and a name that jumped around under the cursor
// because it fuzzily matched would be harder to take than to type out.
func skillSuggest(token string, start, end int, cfg *config.Config) *suggest {
	if cfg == nil {
		return nil
	}
	s := &suggest{kind: sugSkill, token: token, start: start, end: end}
	q := strings.ToLower(strings.TrimPrefix(token, skillMark))
	for _, name := range cfg.SkillNames() {
		if !strings.HasPrefix(strings.ToLower(name), q) {
			continue
		}
		s.items = append(s.items, item{
			insert: skillMark + name,
			// A skill name is the whole reference, so completing one ends it and
			// the sentence around it carries on.
			space:  true,
			label:  skillMark + name,
			detail: cfg.Skills[name].Description,
		})
	}
	if len(s.items) == 0 {
		// Nothing defined, or nothing that matches. An empty box would take rows
		// off the transcript to report that a # is just a #.
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
