package ui

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
)

// Completion is for commands and mentions and nothing else: a message that
// happens to contain a slash or an address must not put a list over the
// transcript.
func TestSuggestOnlyForCommands(t *testing.T) {
	tests := []struct {
		line string
		open bool
	}{
		{"", false},
		{"hello", false},
		{"what does a/b do", false},
		{"/", true},
		{"/mo", true},
		{"/model", true},
		// Past the first word the user is typing an argument, not a name.
		{"/model ", false},
		{"/model openai/gpt", false},
		// A command may open any line, so one can be typed under a long
		// prompt. This used to be false, back when a command had to be the
		// whole input.
		{"a\n/model", true},
		// Still only at the start of a line: a slash inside prose or a path is
		// not a command, wherever in the text it falls.
		{"a\nand/or", false},
		{"a\nsrc/main.go", false},
		{"see src/main.go", false},
		// Indented text is quoted code far more often than it is a command.
		{"a\n  /model", false},
		// A name that cannot become a command has nothing to offer.
		{"/zzz", false},
		// An address is not a mention: the @ has to start the word.
		{"mail someone@example.com", false},
	}

	for _, tt := range tests {
		got := suggestFor(tt.line, caretEnd(tt.line), nil) != nil
		if got != tt.open {
			t.Errorf("suggestFor(%q) open = %v, want %v", tt.line, got, tt.open)
		}
	}
}

func TestSuggestMatches(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		want  []string
		first string
	}{
		{
			name:  "a bare slash offers everything",
			line:  "/",
			first: "/model",
		},
		{
			name: "prefix narrows",
			line: "/s",
			want: []string{"/status", "/sessions"},
		},
		{
			// The canonical name is what gets offered, so the alias teaches it.
			name: "aliases match",
			line: "/q",
			want: []string{"/quit"},
		},
		{
			name: "alias that shares no prefix with its command",
			line: "/new",
			want: []string{"/clear"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := suggestFor(tt.line, caretEnd(tt.line), nil)
			if s == nil {
				t.Fatalf("suggestFor(%q) offered nothing", tt.line)
			}
			if tt.first != "" {
				if len(s.items) != len(commands) {
					t.Errorf("matched %d commands, want all %d", len(s.items), len(commands))
				}
				if s.items[0].insert != tt.first {
					t.Errorf("first = %q, want %q", s.items[0].insert, tt.first)
				}
				return
			}
			if len(s.items) != len(tt.want) {
				t.Fatalf("matched %d, want %d: %v", len(s.items), len(tt.want), names(s))
			}
			for i, w := range tt.want {
				if s.items[i].insert != w {
					t.Errorf("match %d = %q, want %q", i, s.items[i].insert, w)
				}
			}
		})
	}
}

// A mention is completed wherever it sits in the sentence, and only the
// mention is: the words before it are the message and must survive.
func TestMentionCompletesTheLastWord(t *testing.T) {
	idx := indexOf("main.go", "internal/ui/ui.go")

	s := suggestFor("have a look at @ui.go", caretEnd("have a look at @ui.go"), idx)
	if s == nil {
		t.Fatal("a mention offered nothing")
	}
	if s.kind != sugFile {
		t.Errorf("kind = %v, want a file completion", s.kind)
	}
	if s.token != "@ui.go" {
		t.Errorf("token = %q, want the mention alone", s.token)
	}
	it, _ := s.selected()
	if it.insert != "@internal/ui/ui.go" {
		t.Errorf("insert = %q, want the path with its @", it.insert)
	}
	// A file ends the mention; the sentence carries on after it.
	if !it.space {
		t.Error("completing a file left no space to keep typing")
	}
}

// Completing a folder has to leave the mention open, or stepping into one
// would mean deleting the space it just added.
func TestMentionKeepsGoingIntoAFolder(t *testing.T) {
	idx := indexOf("internal/ui/ui.go")

	s := suggestFor("@internal", caretEnd("@internal"), idx)
	it, ok := s.selected()
	if !ok {
		t.Fatal("no selection")
	}
	if it.insert != "@internal/" {
		t.Errorf("insert = %q, want the folder with its slash", it.insert)
	}
	if it.space {
		t.Error("completing a folder ended the mention")
	}

	// And the completed folder lists what is inside it.
	inside := suggestFor("@internal/", caretEnd("@internal/"), idx)
	if inside == nil || inside.items[0].insert != "@internal/ui/" {
		t.Fatalf("stepping into a folder offered %v", names(inside))
	}
}

// The tree is scanned in the background, so the first mention arrives before
// the paths do. It has to say so rather than look like nothing matched.
func TestMentionSaysWhenStillScanning(t *testing.T) {
	s := suggestFor("@ui", caretEnd("@ui"), nil)
	if s == nil {
		t.Fatal("a mention with no index offered nothing at all")
	}
	if !s.scanning {
		t.Error("want the list to report that it is still scanning")
	}
	if s.height() != 1 {
		t.Errorf("height = %d while scanning, want a single row", s.height())
	}
}

// Taking a completion rewrites the word it belongs to and nothing else.
func TestAcceptSuggestSplicesTheToken(t *testing.T) {
	tests := []struct {
		name        string
		typed, want string
	}{
		{"a mention keeps the sentence around it", "compare @ui.go", "compare @internal/ui/ui.go "},
		{"a folder stays open", "look in @internal", "look in @internal/"},
		{"a command replaces the line", "/mod", "/model "},
		{"a second mention leaves the first alone",
			"diff @internal/ui/ui.go against @main", "diff @internal/ui/ui.go against @main.go "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ta := textarea.New()
			ta.SetWidth(60)
			ta.SetValue(tt.typed)

			m := Model{input: ta, files: indexOf("main.go", "internal/ui/ui.go")}
			m.sug = suggestFor(tt.typed, caretEnd(tt.typed), m.files)
			if m.sug == nil {
				t.Fatalf("%q offered nothing to accept", tt.typed)
			}

			next, _ := m.acceptSuggest()
			if got := next.(Model).input.Value(); got != tt.want {
				t.Errorf("input = %q, want %q", got, tt.want)
			}
		})
	}
}

func names(s *suggest) []string {
	var out []string
	for _, it := range s.items {
		out = append(out, it.label)
	}
	return out
}

// The cursor wraps rather than sticking at either end, so a list of three is
// reachable in either direction.
func TestSuggestMoveWraps(t *testing.T) {
	s := suggestFor("/s", caretEnd("/s"), nil)
	s.move(-1)
	if s.cursor != len(s.items)-1 {
		t.Errorf("cursor = %d after moving up from the top, want the last entry", s.cursor)
	}
	s.move(1)
	if s.cursor != 0 {
		t.Errorf("cursor = %d after wrapping back, want 0", s.cursor)
	}
}

// Enter runs a command typed out in full and completes one that is not, so
// isCommand has to be exact — a prefix of a command is not a command.
func TestIsCommand(t *testing.T) {
	for _, line := range []string{"/model", "/quit", "/q", "/new", "/comp"} {
		if !isCommand(line) {
			t.Errorf("isCommand(%q) = false, want true", line)
		}
	}
	for _, line := range []string{"/mod", "/", "", "/models", "model"} {
		if isCommand(line) {
			t.Errorf("isCommand(%q) = true, want false", line)
		}
	}
}

// Every command the table advertises has to be one command() actually
// dispatches, or completion would offer a name that answers "unknown command".
//
// The dispatcher is read out of the source rather than restated here: a copy of
// the list would drift alongside the thing it is meant to be checking.
func TestEveryAdvertisedCommandIsHandled(t *testing.T) {
	src, err := os.ReadFile("ui.go")
	if err != nil {
		t.Fatal(err)
	}
	handled := map[string]bool{}
	for _, m := range regexp.MustCompile(`case ("/\w+"(?:, "/\w+")*):`).FindAllStringSubmatch(string(src), -1) {
		for _, name := range strings.Split(m[1], ", ") {
			handled[strings.Trim(name, `"`)] = true
		}
	}
	if len(handled) == 0 {
		t.Fatal("found no dispatched commands — has command() moved?")
	}

	for _, c := range commands {
		for _, name := range append([]string{c.name}, c.aliases...) {
			if !handled[name] {
				t.Errorf("%s is offered while typing but not handled by command()", name)
			}
		}
	}
}

// caretEnd is the caret sitting at the end of the text, in runes, which is
// where it is for anything typed straight through.
func caretEnd(s string) int { return len([]rune(s)) }

// The point of completing at the caret: a prompt is often written, then gone
// back into. A mention typed into the middle of one has to be offered, and
// what surrounds it has to survive being completed.
func TestSuggestAtCaretInTheMiddleOfAProse(t *testing.T) {
	idx := indexOf("main.go", "internal/ui/ui.go")
	const text = "please look at @ui and tell me what it does"
	caret := len([]rune("please look at @ui"))

	s := suggestFor(text, caret, idx)
	if s == nil {
		t.Fatal("a mention in the middle of a prompt offered nothing")
	}
	if s.kind != sugFile {
		t.Errorf("kind = %v, want a file completion", s.kind)
	}
	if s.token != "@ui" {
		t.Errorf("token = %q, want just the mention at the caret", s.token)
	}
}

// The token stops at the caret. Completing "@ui" with the caret after the "i"
// must not swallow the " and tell me" that follows, nor the rest of a word.
func TestTokenAtStopsAtTheCaret(t *testing.T) {
	const text = "see @ui.go here"
	// Caret after "@ui", i.e. inside the word.
	caret := len([]rune("see @ui"))

	tok, start, end := tokenAt(text, caret)
	if tok != "@ui" {
		t.Errorf("token = %q, want the text up to the caret only", tok)
	}
	if start != 4 || end != caret {
		t.Errorf("bounds = %d..%d, want 4..%d", start, end, caret)
	}
}

// A command on its own line under a prompt is the case the old rule could not
// express, and the one people keep reaching for.
func TestCommandOnALaterLine(t *testing.T) {
	const text = "here is a long thought\nspanning two lines\n/mod"

	s := suggestFor(text, caretEnd(text), nil)
	if s == nil {
		t.Fatal("a command on its own line offered nothing")
	}
	if s.kind != sugCommand {
		t.Errorf("kind = %v, want a command completion", s.kind)
	}
	if s.token != "/mod" {
		t.Errorf("token = %q, want the command alone rather than the whole prompt", s.token)
	}
}

// Splicing has to put back exactly what was around the token, and leave the
// caret where typing carries on.
func TestAcceptSuggestSplicesMidText(t *testing.T) {
	idx := indexOf("main.go", "internal/ui/ui.go")
	const text = "compare @ui.go with the rest"
	caret := len([]rune("compare @ui.go"))

	ta := textarea.New()
	ta.SetWidth(60)
	ta.SetValue(text)

	m := Model{input: ta, files: idx}
	m.sug = suggestFor(text, caret, idx)
	if m.sug == nil {
		t.Fatal("nothing to accept")
	}

	next, _ := m.acceptSuggest()
	got := next.(Model)
	const want = "compare @internal/ui/ui.go  with the rest"
	if got.input.Value() != want {
		t.Fatalf("input = %q, want %q", got.input.Value(), want)
	}
	// The caret belongs just after what was inserted, not at the end of the
	// prompt: the user was editing the middle of it.
	wantCaret := len([]rune("compare @internal/ui/ui.go "))
	if c := got.caretOffset(); c != wantCaret {
		t.Errorf("caret = %d, want %d", c, wantCaret)
	}
}

// Columns are rune indices, so a prompt with wide or multi-byte characters in
// it has to splice in the same place as a plain one. This is the case that
// silently corrupts text if any of the arithmetic is done in bytes.
func TestAcceptSuggestWithNonASCIIBefore(t *testing.T) {
	idx := indexOf("main.go", "internal/ui/ui.go")
	const prefix = "an em dash — and an ellipsis … then @ui.go"
	const text = prefix + " and more"
	caret := len([]rune(prefix))

	ta := textarea.New()
	ta.SetWidth(80)
	ta.SetValue(text)

	m := Model{input: ta, files: idx}
	m.sug = suggestFor(text, caret, idx)
	if m.sug == nil {
		t.Fatal("nothing to accept")
	}

	next, _ := m.acceptSuggest()
	got := next.(Model).input.Value()
	const want = "an em dash — and an ellipsis … then @internal/ui/ui.go  and more"
	if got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
}

// Moving the caret onto a different mention asks a different question, so the
// list has to follow it even though the text has not changed.
func TestCaretMoveRebuildsTheList(t *testing.T) {
	idx := indexOf("main.go", "internal/ui/ui.go")
	const text = "@main and @internal"

	first := suggestFor(text, len([]rune("@main")), idx)
	if first == nil || first.token != "@main" {
		t.Fatalf("caret on the first mention offered %v", first)
	}
	second := suggestFor(text, caretEnd(text), idx)
	if second == nil || second.token != "@internal" {
		t.Fatalf("caret on the second mention offered %v", second)
	}
}
