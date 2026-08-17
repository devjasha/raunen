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
		// A slash on a later line is prose, not a command.
		{"a\n/model", false},
		// A name that cannot become a command has nothing to offer.
		{"/zzz", false},
		// An address is not a mention: the @ has to start the word.
		{"mail someone@example.com", false},
	}

	for _, tt := range tests {
		got := suggestFor(tt.line, nil) != nil
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
			s := suggestFor(tt.line, nil)
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

	s := suggestFor("have a look at @ui.go", idx)
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

	s := suggestFor("@internal", idx)
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
	inside := suggestFor("@internal/", idx)
	if inside == nil || inside.items[0].insert != "@internal/ui/" {
		t.Fatalf("stepping into a folder offered %v", names(inside))
	}
}

// The tree is scanned in the background, so the first mention arrives before
// the paths do. It has to say so rather than look like nothing matched.
func TestMentionSaysWhenStillScanning(t *testing.T) {
	s := suggestFor("@ui", nil)
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
			m.sug = suggestFor(tt.typed, m.files)
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
	s := suggestFor("/s", nil)
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
