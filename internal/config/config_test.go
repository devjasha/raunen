package config

import (
	"os"
	"testing"
)

func testConfig() *Config {
	return &Config{
		Providers: map[string]Provider{
			"ollama":       {BaseURL: "http://localhost:11434/v1", Context: 4096},
			"ollama-cloud": {BaseURL: "https://ollama.com/v1", APIKeyEnv: "OLLAMA_API_KEY"},
		},
		Models: map[string]ModelConfig{
			"ollama/qwen3-coder:30b":        {Context: 32768},
			"ollama-cloud/qwen3-coder:480b": {Context: 262144},
		},
	}
}

// One endpoint serves many models with very different windows, so a per-model
// setting has to win over the provider's default.
func TestContextForPrefersTheModel(t *testing.T) {
	c := testConfig()
	tests := []struct {
		ref  string
		want int
	}{
		{"ollama/qwen3-coder:30b", 32768},         // model wins over provider
		{"ollama/some-other:8b", 4096},            // falls back to provider
		{"ollama-cloud/qwen3-coder:480b", 262144}, // model wins with no provider default
		{"ollama-cloud/gpt-oss:120b", 0},          // unknown, and honestly so
		{"nonsense/model", 0},                     // unresolvable
	}
	for _, tt := range tests {
		if got := c.ContextFor(tt.ref); got != tt.want {
			t.Errorf("ContextFor(%q) = %d, want %d", tt.ref, got, tt.want)
		}
	}
}

// Model names contain slashes on Ollama and OpenRouter alike, so the split has
// to be on the first one only.
func TestResolveSplitsOnFirstSlash(t *testing.T) {
	c := &Config{Providers: map[string]Provider{"p": {BaseURL: "http://x/v1"}}}
	for _, tt := range []struct{ ref, want string }{
		{"p/model", "model"},
		{"p/hf.co/user/repo", "hf.co/user/repo"},
		{"p/anthropic/claude-sonnet-4", "anthropic/claude-sonnet-4"},
	} {
		_, model, err := c.Resolve(tt.ref)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", tt.ref, err)
		}
		if model != tt.want {
			t.Errorf("Resolve(%q) model = %q, want %q", tt.ref, model, tt.want)
		}
	}

	if _, _, err := c.Resolve("no-slash"); err == nil {
		t.Error("Resolve accepted a reference with no provider")
	}
	if _, _, err := c.Resolve("missing/model"); err == nil {
		t.Error("Resolve accepted an unknown provider")
	}
}

// The key may come from the environment so it never has to be written to disk.
// Favourites pin a reference and unpin it again, and the set is written to the
// config so it is visible and editable in the file like the default model.
func TestToggleFavouritePinsAndUnpins(t *testing.T) {
	dir := t.TempDir()
	c := &Config{Providers: map[string]Provider{"ollama": {BaseURL: "http://x/v1"}}}
	c.file = dir + "/config.json"

	if c.IsFavourite("ollama/a") {
		t.Fatal("nothing pinned yet")
	}
	if err := c.ToggleFavourite("ollama/a"); err != nil {
		t.Fatal(err)
	}
	if !c.IsFavourite("ollama/a") {
		t.Fatal("ollama/a should be pinned")
	}
	// Order is preserved across a second pin.
	if err := c.ToggleFavourite("ollama/b"); err != nil {
		t.Fatal(err)
	}
	if got := c.Favourites; len(got) != 2 || got[0] != "ollama/a" || got[1] != "ollama/b" {
		t.Fatalf("Favourites = %v, want [ollama/a ollama/b]", got)
	}
	// Unpinning the first leaves the second, in its place.
	if err := c.ToggleFavourite("ollama/a"); err != nil {
		t.Fatal(err)
	}
	if c.IsFavourite("ollama/a") {
		t.Fatal("ollama/a should be unpinned")
	}
	if got := c.Favourites; len(got) != 1 || got[0] != "ollama/b" {
		t.Fatalf("Favourites = %v, want [ollama/b]", got)
	}

	// The file is written, and the pins round-trip through it.
	again, err := Load(c.file)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Favourites; len(got) != 1 || got[0] != "ollama/b" {
		t.Fatalf("loaded Favourites = %v, want [ollama/b]", got)
	}
}

func TestProviderKeyPrefersEnvironment(t *testing.T) {
	t.Setenv("RAUNEN_TEST_KEY", "from-env")
	p := Provider{APIKey: "from-file", APIKeyEnv: "RAUNEN_TEST_KEY"}
	if got := p.Key(); got != "from-env" {
		t.Errorf("Key() = %q, want the environment to win", got)
	}

	p = Provider{APIKey: "from-file", APIKeyEnv: "RAUNEN_TEST_UNSET"}
	if got := p.Key(); got != "from-file" {
		t.Errorf("Key() = %q, want the file value when the variable is unset", got)
	}

	if got := (Provider{}).Key(); got != "" {
		t.Errorf("Key() = %q, want empty for a local provider with no key", got)
	}
}

// An existing config should pick up providers added to the defaults later,
// without touching anything the user has already set.
func TestLoadMergesNewDefaultProviders(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	if err := os.WriteFile(path, []byte(`{
	  "default": "ollama/mine",
	  "providers": { "ollama": { "base_url": "http://elsewhere:1234/v1", "context": 99 } }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Providers["ollama"].BaseURL; got != "http://elsewhere:1234/v1" {
		t.Errorf("ollama base_url = %q, want the user's value untouched", got)
	}
	if got := c.Providers["ollama"].Context; got != 99 {
		t.Errorf("ollama context = %d, want the user's value untouched", got)
	}
	if _, ok := c.Providers["ollama-cloud"]; !ok {
		t.Error("ollama-cloud was not merged in")
	}
	if c.Default != "ollama/mine" {
		t.Errorf("default = %q, want it untouched", c.Default)
	}
}

// A config that can hold API keys must not be world-readable. WriteFile only
// applies its mode when creating a file, so an existing 0644 config would keep
// those permissions and leak the key.
func TestSaveTightensPermissionsOnAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"

	// A config from before keys were storable.
	if err := os.WriteFile(path, []byte(`{"providers":{"groq":{"base_url":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetKey("groq", "secret"); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %04o, want 0600 — the key is readable by others", perm)
	}

	// And it round-trips.
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Providers["groq"].APIKey; got != "secret" {
		t.Errorf("api_key = %q, want %q", got, "secret")
	}
}

// A skill is expanded onto the end of the message rather than spliced into it,
// so what the user typed still reads as what they typed. The reference itself
// stays put, which is what keeps the prompt legible in the transcript.
func TestExpandSkills(t *testing.T) {
	c := &Config{Skills: map[string]Skill{
		"review": {Description: "review checklist", Prompt: "  Check for races.  "},
		"style":  {Prompt: "Tabs, not spaces."},
	}}

	tests := []struct {
		name string
		text string
		want string
		used []string
	}{
		{
			name: "a reference appends its prompt",
			text: "#review this diff",
			want: "#review this diff\n\n[skill: review]\nCheck for races.",
			used: []string{"review"},
		},
		{
			// Two skills in one message are labelled apart, or the model cannot
			// tell where one set of instructions ends.
			name: "two skills, in the order they appear",
			text: "#style then #review",
			want: "#style then #review\n\n[skill: style]\nTabs, not spaces." +
				"\n\n[skill: review]\nCheck for races.",
			used: []string{"style", "review"},
		},
		{
			// Naming it twice is still one set of instructions; sending it twice
			// would only spend context saying the same thing.
			name: "a repeat costs nothing",
			text: "#review and again #review",
			want: "#review and again #review\n\n[skill: review]\nCheck for races.",
			used: []string{"review"},
		},
		{
			// Typed mid-sentence, a capital is a typing habit rather than a
			// different skill.
			name: "case does not matter",
			text: "#Review please",
			want: "#Review please\n\n[skill: Review]\nCheck for races.",
			used: []string{"Review"},
		},
		{
			// A skill at the end of a sentence is followed by a full stop far
			// more often than not.
			name: "trailing punctuation is not part of the name",
			text: "apply #review.",
			want: "apply #review.\n\n[skill: review]\nCheck for races.",
			used: []string{"review"},
		},
		{
			// Headings, issue numbers and colours all start with a hash, and
			// rewriting prose that was never a reference is worse than ignoring
			// one that was.
			name: "an undefined name is left alone",
			text: "see issue #4213 and #heading",
			want: "see issue #4213 and #heading",
		},
		{
			// The mark has to start the word, so a fragment of a URL cannot
			// become a reference.
			name: "the mark has to start the word",
			text: "https://example.com/x#review",
			want: "https://example.com/x#review",
		},
		{
			name: "a message with no references is untouched",
			text: "what does main.go do?",
			want: "what does main.go do?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, used := c.ExpandSkills(tt.text)
			if got != tt.want {
				t.Errorf("ExpandSkills(%q) =\n%q\nwant\n%q", tt.text, got, tt.want)
			}
			if len(used) != len(tt.used) {
				t.Fatalf("used = %v, want %v", used, tt.used)
			}
			for i, w := range tt.used {
				if used[i] != w {
					t.Errorf("used[%d] = %q, want %q", i, used[i], w)
				}
			}
		})
	}
}

// With nothing defined the message must come back exactly as it went in — the
// common case is a config with no skills at all, and it must not start
// rewriting prompts that happen to contain a hash.
func TestExpandSkillsWithNoneDefined(t *testing.T) {
	c := &Config{}
	const text = "fix #123 and #this"
	got, used := c.ExpandSkills(text)
	if got != text {
		t.Errorf("ExpandSkills = %q, want the message untouched", got)
	}
	if used != nil {
		t.Errorf("used = %v, want nothing", used)
	}
}

// Skills live beside the config rather than in it, so they round-trip through
// their own file — and a name that could never be typed as one word is dropped
// rather than offered.
func TestLoadSkills(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// Nothing there yet: a starter file is written, as it is for MCP.
	first, err := LoadSkills()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 0 {
		t.Errorf("a fresh install has %d skills, want none", len(first))
	}
	if _, err := os.Stat(SkillsPath()); err != nil {
		t.Errorf("no starter file written: %v", err)
	}

	if err := os.WriteFile(SkillsPath(), []byte(`{
	  "review": { "description": "review checklist", "prompt": "Check for races." },
	  "two words": { "prompt": "unreachable" }
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := LoadSkills()
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 {
		t.Fatalf("loaded %d skills, want only the usable one: %v", len(m), m)
	}
	if got := m["review"].Prompt; got != "Check for races." {
		t.Errorf("prompt = %q, want it round-tripped", got)
	}
	if got := m["review"].Description; got != "review checklist" {
		t.Errorf("description = %q, want it round-tripped", got)
	}
	if _, ok := m["two words"]; ok {
		t.Error("a name with a space in it was kept, but it can never be referenced")
	}
}

// The list is what /skills prints and what completion offers, so it has to be
// stable — ranging the map directly would reshuffle it between one look and the
// next.
func TestSkillNamesAreSorted(t *testing.T) {
	c := &Config{Skills: map[string]Skill{
		"review": {}, "style": {}, "commit": {},
	}}
	want := []string{"commit", "review", "style"}
	got := c.SkillNames()
	if len(got) != len(want) {
		t.Fatalf("SkillNames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SkillNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
