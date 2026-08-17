// Package config loads provider and model definitions from disk.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Provider is an OpenAI-compatible endpoint. Local runtimes (Ollama, LM Studio,
// llama.cpp, vLLM) and most hosted gateways all speak this same wire format, so
// adding one is a config entry rather than code.
type Provider struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key,omitempty"`
	// APIKeyEnv reads the key from the environment instead of storing it here.
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// Free marks an endpoint whose models cost nothing to call — a free tier, or
	// anything self-hosted. It has to be declared because most endpoints do not
	// say: OpenRouter publishes per-model pricing, but Groq, Cerebras and NVIDIA
	// simply do not, and an unstated price cannot be assumed to be zero.
	Free bool `json:"free,omitempty"`
	// Context is a default context window in tokens for this provider's models.
	// Most endpoints serve several models with different windows, so this is
	// only a fallback — declare per-model windows under Config.Models.
	Context int `json:"context,omitempty"`
}

// ModelConfig is per-model settings. A provider usually serves many models with
// very different windows, so the window belongs here rather than on the
// provider, which only carries a default for anything not listed.
type ModelConfig struct {
	// Context is the model's window in tokens. There is no way to ask an
	// OpenAI-compatible endpoint for this, so it has to be declared.
	Context int `json:"context,omitempty"`
}

// Key resolves the API key, preferring the environment variable when set.
// Local providers legitimately have no key at all.
func (p Provider) Key() string {
	if p.APIKeyEnv != "" {
		if v := os.Getenv(p.APIKeyEnv); v != "" {
			return v
		}
	}
	return p.APIKey
}

type Config struct {
	// Default is a "provider/model" reference, e.g. "ollama/qwen3-coder".
	Default   string              `json:"default"`
	Providers map[string]Provider `json:"providers"`
	// AutoSwitch turns on escalation: when the conversation outgrows the
	// current model, move to the next entry in Fallback rather than failing.
	AutoSwitch bool `json:"auto_switch"`
	// Fallback is an escalation ladder of "provider/model" references, tried in
	// the order given. Put larger contexts later.
	Fallback []string `json:"fallback"`
	// FreeFallback appends every free model the providers report to the ladder,
	// largest context first. Free tiers are rate limited rather than billed, so
	// a ladder of them is a way to keep going rather than to spend more.
	FreeFallback bool `json:"free_fallback"`
	// Subagents lets the model delegate self-contained work to a sub-agent with
	// its own context. It costs one more tool schema on every request, so it
	// can be turned off on a very small window.
	Subagents *bool `json:"subagents,omitempty"`
	// Models holds per-model settings, keyed by "provider/model". Anything not
	// listed falls back to its provider.
	Models map[string]ModelConfig `json:"models"`
	// Favourites are "provider/model" references the user has pinned for quick
	// access, in the order they were first pinned. They surface at the top of
	// /model and are toggled with /favourite.
	Favourites []string `json:"favourites,omitempty"`
	// System overrides the built-in system prompt when non-empty.
	System string `json:"system,omitempty"`

	// file is where this config was read from, so it can be written back.
	file string `json:"-"`
}

// SetKey stores an API key for a provider and writes the config out. The key
// lives in the file from then on, which is why Save tightens its permissions.
func (c *Config) SetKey(name, key string) error {
	p, ok := c.Providers[name]
	if !ok {
		return fmt.Errorf("unknown provider %q", name)
	}
	p.APIKey = key
	c.Providers[name] = p
	return c.Save()
}

// Save writes the config back where it was read from.
func (c *Config) Save() error {
	if c.file == "" {
		c.file = Path()
	}
	return save(c.file, c)
}

// SubagentsEnabled reports whether delegation is on, defaulting to on when the
// key is absent so an existing config gains the feature.
func (c *Config) SubagentsEnabled() bool {
	return c.Subagents == nil || *c.Subagents
}

// ModelContext returns a window declared for this exact model, or zero. It is
// kept separate from ContextFor because a provider-level context is a blunt
// default for a whole endpoint, while a discovered window is the truth for one
// model — so the discovered value should beat the default, and only an explicit
// per-model setting should beat both.
func (c *Config) ModelContext(ref string) int {
	if m, ok := c.Models[ref]; ok && m.Context > 0 {
		return m.Context
	}
	return 0
}

// ProviderContext returns the endpoint's default window, or zero.
func (c *Config) ProviderContext(ref string) int {
	if p, _, err := c.Resolve(ref); err == nil {
		return p.Context
	}
	return 0
}

// IsFavourite reports whether a reference is pinned. Order does not matter here.
func (c *Config) IsFavourite(ref string) bool {
	for _, f := range c.Favourites {
		if f == ref {
			return true
		}
	}
	return false
}

// ToggleFavourite pins a reference if it is not already pinned, or removes it if
// it is, keeping the remaining pins in their original order. The config is
// written back so the set is visible and editable in the file, like Default.
func (c *Config) ToggleFavourite(ref string) error {
	for i, f := range c.Favourites {
		if f == ref {
			c.Favourites = append(c.Favourites[:i], c.Favourites[i+1:]...)
			return c.Save()
		}
	}
	c.Favourites = append(c.Favourites, ref)
	return c.Save()
}

// ContextFor returns the window for a "provider/model" reference: the model's
// own if declared, otherwise its provider's default, otherwise zero for
// unknown. Zero disables the usage bar's percentage, history trimming and
// automatic switching, all of which need something to measure against.
func (c *Config) ContextFor(ref string) int {
	if m, ok := c.Models[ref]; ok && m.Context > 0 {
		return m.Context
	}
	if p, _, err := c.Resolve(ref); err == nil {
		return p.Context
	}
	return 0
}

// Resolve splits a "provider/model" reference and looks up the provider.
// The split is on the FIRST slash: Ollama model names may themselves contain
// slashes (e.g. "hf.co/user/repo"), so everything after it is the model.
func (c *Config) Resolve(ref string) (Provider, string, error) {
	name, model, ok := strings.Cut(ref, "/")
	if !ok {
		return Provider{}, "", fmt.Errorf("model %q must be in provider/model form", ref)
	}
	p, ok := c.Providers[name]
	if !ok {
		return Provider{}, "", fmt.Errorf("unknown provider %q (have: %s)", name, strings.Join(c.names(), ", "))
	}
	return p, model, nil
}

func (c *Config) names() []string {
	out := make([]string, 0, len(c.Providers))
	for k := range c.Providers {
		out = append(out, k)
	}
	return out
}

// Path is the default config location. It follows XDG rather than
// os.UserConfigDir, which on macOS points at ~/Library/Application Support —
// not where terminal tools are usually configured.
func Path() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "raunen", "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "raunen.json"
	}
	return filepath.Join(home, ".config", "raunen", "config.json")
}

// enabled is addressable so it can be pointed at from the default config.
var enabled = true

func defaults() *Config {
	return &Config{
		// Left empty on purpose: the first run picks whatever the endpoints
		// actually serve, rather than naming a model the user may not have.
		Default: "",
		// Off by default, and listed so the knobs are visible in the file that
		// gets written on first run.
		AutoSwitch:   false,
		Fallback:     []string{},
		FreeFallback: false,
		Models:       map[string]ModelConfig{},
		Subagents:    &enabled,
		Providers: map[string]Provider{
			// Ollama defaults to a 4096-token context regardless of what the
			// model supports; raise OLLAMA_CONTEXT_LENGTH and this together.
			"ollama":   {BaseURL: "http://localhost:11434/v1", Context: 4096},
			"lmstudio": {BaseURL: "http://localhost:1234/v1"},
			// A local gateway. No key: it binds to localhost and trusts local
			// callers, so declaring one would only make it look unavailable.
			"omniroute": {BaseURL: "http://localhost:20128/v1"},
			"llamacpp":  {BaseURL: "http://localhost:8080/v1"},
			"ollama-cloud": {
				BaseURL:   "https://ollama.com/v1",
				APIKeyEnv: "OLLAMA_API_KEY",
			},
			// Free tiers, all OpenAI-compatible, all needing a key of their own.
			// Marked free because none of them publish pricing, so it cannot be
			// worked out from the catalogue.
			"groq": {
				BaseURL:   "https://api.groq.com/openai/v1",
				APIKeyEnv: "GROQ_API_KEY",
				Free:      true,
			},
			"cerebras": {
				BaseURL:   "https://api.cerebras.ai/v1",
				APIKeyEnv: "CEREBRAS_API_KEY",
				Free:      true,
			},
			"nvidia": {
				BaseURL:   "https://integrate.api.nvidia.com/v1",
				APIKeyEnv: "NVIDIA_API_KEY",
				Free:      true,
			},
			"openrouter": {
				BaseURL:   "https://openrouter.ai/api/v1",
				APIKeyEnv: "OPENROUTER_API_KEY",
			},
		},
	}
}

// Load reads the config at path, writing a starter file if none exists.
func Load(path string) (*Config, error) {
	if path == "" {
		path = Path()
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		c := defaults()
		c.file = path
		if err := save(path, c); err != nil {
			return nil, err
		}
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if c.Providers == nil {
		c.Providers = map[string]Provider{}
	}
	c.file = path
	// Merge in any provider added to the defaults since this file was written,
	// so an existing config picks up new endpoints without being rewritten.
	// Only entirely absent names are added: anything the user has edited is
	// left exactly as they left it.
	for name, p := range defaults().Providers {
		if _, ok := c.Providers[name]; !ok {
			c.Providers[name] = p
		}
	}
	return &c, nil
}

func save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	// 0600, not 0644: this file can hold API keys, and a personal config has no
	// reason to be readable by anyone else either way.
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	// WriteFile only applies its mode when it creates the file, so an existing
	// config keeps whatever it had — which for anything written before keys
	// were storable is 0644. Set it explicitly.
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	// Renamed into place so an interrupted write cannot truncate a working
	// config, and so the key is never briefly visible in a half-written file.
	return os.Rename(tmp, path)
}
