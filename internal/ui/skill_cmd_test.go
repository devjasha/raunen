package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"

	"raunen/internal/agent"
	"raunen/internal/companion"
	"raunen/internal/config"
	"raunen/internal/provider"
	"raunen/internal/session"
	"raunen/internal/tools"
)

// testSkills is the small set the completion tests share: two names starting
// with the same letter, so narrowing has something to narrow.
func testSkills() *config.Config {
	return &config.Config{Skills: map[string]config.Skill{
		"review": {Description: "review checklist", Prompt: "Check for races."},
		"revert": {Prompt: "Undo it cleanly."},
		"commit": {Description: "house commit style", Prompt: "Imperative mood."},
	}}
}

// A # in a prompt offers the skills it could still become, and nothing else
// does — a hash in prose is a heading or an issue number far more often than it
// is a reference, and a list popping up over the transcript for each of those
// would be worse than never offering one.
func TestSkillSuggestOpensOnlyOnTheMark(t *testing.T) {
	cfg := testSkills()
	tests := []struct {
		line string
		open bool
	}{
		{"#", true},
		{"#rev", true},
		{"apply #com", true},
		{"", false},
		{"fix the bug", false},
		// The mark has to start the word, so neither a URL fragment nor an
		// issue number opens a list.
		{"see example.com/x#review", false},
		{"closes #4213", false},
		// A name that cannot become a skill has nothing to offer.
		{"#zzz", false},
	}

	for _, tt := range tests {
		got := suggestFor(tt.line, caretEnd(tt.line), nil, cfg) != nil
		if got != tt.open {
			t.Errorf("suggestFor(%q) open = %v, want %v", tt.line, got, tt.open)
		}
	}
}

func TestSkillSuggestMatches(t *testing.T) {
	cfg := testSkills()
	tests := []struct {
		name string
		line string
		want []string
	}{
		{
			// A bare mark is how you remember what you defined, so it lists
			// everything rather than waiting for a first letter.
			name: "a bare mark offers everything",
			line: "#",
			want: []string{"#commit", "#return-nothing", "#revert", "#review"},
		},
		{
			name: "prefix narrows",
			line: "#rev",
			want: []string{"#revert", "#review"},
		},
		{
			// Typed mid-sentence, a capital is a typing habit rather than a
			// different skill.
			name: "case does not matter",
			line: "#REV",
			want: []string{"#revert", "#review"},
		},
		{
			name: "a mention in the middle of a prompt",
			line: "please apply #com",
			want: []string{"#commit"},
		},
	}

	// The name that sorts between the two "rev" ones, so the order is testable.
	cfg.Skills["return-nothing"] = config.Skill{Prompt: "x"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := suggestFor(tt.line, caretEnd(tt.line), nil, cfg)
			if s == nil {
				t.Fatalf("suggestFor(%q) offered nothing", tt.line)
			}
			if s.kind != sugSkill {
				t.Errorf("kind = %v, want a skill completion", s.kind)
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

// The description is what tells one skill from another while choosing, and it
// belongs to the person choosing rather than to the model.
func TestSkillSuggestShowsTheDescription(t *testing.T) {
	s := suggestFor("#com", caretEnd("#com"), nil, testSkills())
	it, ok := s.selected()
	if !ok {
		t.Fatal("no selection")
	}
	if it.detail != "house commit style" {
		t.Errorf("detail = %q, want the description", it.detail)
	}
	// A skill name is the whole reference, so completing one ends it and the
	// sentence carries on after it.
	if !it.space {
		t.Error("completing a skill left no space to keep typing")
	}
}

// A config with no skills in it is the common case, and it must not put an
// empty box over the transcript every time someone types a hash.
func TestSkillSuggestSilentWithNoneDefined(t *testing.T) {
	for _, cfg := range []*config.Config{nil, {}} {
		if s := suggestFor("#rev", caretEnd("#rev"), nil, cfg); s != nil {
			t.Errorf("a config with no skills offered %v", names(s))
		}
	}
}

// Taking a completion rewrites the reference and leaves the sentence around it
// alone, the way a file mention does.
func TestAcceptSkillSplicesTheToken(t *testing.T) {
	tests := []struct {
		name        string
		typed, want string
	}{
		// The space the completion adds is its own, as it is for a file: the
		// caret is left where typing carries on rather than at the end of a
		// prompt the user was editing the middle of.
		{"the sentence survives", "please #rev this diff", "please #revert  this diff"},
		{"at the end of a prompt", "look at main.go #com", "look at main.go #commit "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ta := textarea.New()
			ta.SetWidth(60)
			ta.SetValue(tt.typed)

			cfg := testSkills()
			m := Model{input: ta, cfg: cfg}
			// The caret sits after the reference, which for the first case is in
			// the middle of the prompt.
			caret := strings.Index(tt.typed, "#") + len("#rev")
			m.sug = suggestFor(tt.typed, caret, nil, cfg)
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

// /skills is the only place the defined skills are listed, so a no-op there is
// invisible — and with none defined it has to say where to put them rather than
// print nothing at all.
func TestSkillsCommandRenders(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{"defined", testSkills(), "#review"},
		// The description stands in for the prompt's own opening words when
		// there is none.
		{"undescribed", testSkills(), "Undo it cleanly."},
		{"none", &config.Config{}, "no skills defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ag := agent.New(provider.New("http://localhost:1/v1", "", "m"),
				tools.Default(t.TempDir(), 4096), "")
			m := New(tc.cfg, ag, t.TempDir(), "x/m", session.New(t.TempDir(), "x/m"), companion.Load())
			m.width = 80
			// Without a height the transcript pane collapses and View clips
			// everything but the last line or two.
			m.height = 40
			ret, _ := m.command("/skills")
			if got := ret.(Model).View().Content; !strings.Contains(got, tc.want) {
				t.Errorf("/skills view does not contain %q:\n%s", tc.want, got)
			}
		})
	}
}

// Sending a message that names a skill leaves the transcript showing what was
// typed — the point of a reference is that a page of instructions does not
// arrive on screen every time it is used — and says which skills went with it,
// since nothing else about the expansion is visible.
func TestSendEchoesTheReferenceAndNotesTheSkill(t *testing.T) {
	m := testModel(t)
	m.cfg = testSkills()

	ret, _ := m.send("#review this diff")
	got := transcript(ret.(Model))

	if !strings.Contains(got, "#review this diff") {
		t.Errorf("the question was not echoed as typed:\n%s", got)
	}
	if strings.Contains(got, "Check for races.") {
		t.Errorf("the skill's prompt was printed into the transcript:\n%s", got)
	}
	if !strings.Contains(got, "# review") {
		t.Errorf("nothing said which skill was pulled in:\n%s", got)
	}
}

// A message that names nothing must not grow a line saying so: the notice is
// there to explain an expansion, and there was none.
func TestSendSaysNothingWithoutASkill(t *testing.T) {
	m := testModel(t)
	m.cfg = testSkills()

	ret, _ := m.send("what does main.go do?")
	if got := transcript(ret.(Model)); strings.Contains(got, "# review") {
		t.Errorf("an unreferenced skill was announced:\n%s", got)
	}
}
