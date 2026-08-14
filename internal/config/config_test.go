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
