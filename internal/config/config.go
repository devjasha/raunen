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
	// MaxSteps is an opt-in backstop on how many tool-calling steps one turn may
	// take, for the rare model that loops instead of finishing. Zero — the
	// default — means no limit: a turn ends when the work is done, and running
	// low on context escalates up the ladder rather than cutting the turn off.
	MaxSteps int `json:"max_steps,omitempty"`
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

	// MCP holds Model Context Protocol servers, keyed by a name of the user's
	// choosing. Each one is started on launch and its tools added to the agent.
	// They live in their own file (mcp.json) rather than the main config so a
	// server that needs a secret — an API key in its env — is not shoulder-to-
	// shoulder with the model defaults, and can be shared without dragging the
	// rest of the config along.
	MCP map[string]MCP `json:"mcp,omitempty"`
	// EnabledMCP names the MCP servers to actually start, out of those defined.
	// A server can be defined but left out of this list so it stays configured
	// but idle; the empty list means "start every defined server".
	EnabledMCP []string `json:"mcp_enabled,omitempty"`

	// file is where this config was read from, so it can be written back.
	file string `json:"-"`
}

// MCP is one Model Context Protocol server definition.
type MCP struct {
	// Command is the program to run for a stdio server, resolved on PATH like
	// any other command.
	Command string `json:"command"`
	// Args are passed to Command after its name.
	Args []string `json:"args,omitempty"`
	// Env is extra environment for the server process — typically a token it
	// needs. Inherited from the parent, so PATH carries over. Ignored for http.
	Env map[string]string `json:"env,omitempty"`
	// Type selects the transport: "" or "stdio" for a local subprocess, "http"
	// for a remote Streamable-HTTP server. Empty means stdio.
	Type string `json:"type,omitempty"`
	// URL is the Streamable-HTTP endpoint, used when Type is "http".
	URL string `json:"url,omitempty"`
	// Headers are extra HTTP headers for an http server, e.g. an Authorization
	// bearer token. Forwarded verbatim on every request.
	Headers map[string]string `json:"headers,omitempty"`
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

// MCPPath is where MCP server definitions are stored. Kept apart from the main
// config: a server definition can carry secrets in its env, and the set of
// servers is a different thing to manage from the model defaults.
func MCPPath() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "raunen", "mcp.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "raunen-mcp.json"
	}
	return filepath.Join(home, ".config", "raunen", "mcp.json")
}

// LoadMCP reads the MCP server definitions, writing a starter file if none
// exists. It is deliberately independent of Load: the two files have different
// contents and different reasons to be shared or edited.
func LoadMCP() (map[string]MCP, error) {
	path := MCPPath()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		starter := map[string]MCP{}
		if err := writeMCP(path, starter); err != nil {
			return nil, err
		}
		return starter, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]MCP
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if m == nil {
		m = map[string]MCP{}
	}
	return m, nil
}

// ActiveMCP returns the subset of defined servers that should be started: if
// EnabledMCP names any, those (that are defined) are used; otherwise every
// defined server is active. A name in EnabledMCP with no matching definition is
// skipped.
func (c *Config) ActiveMCP() map[string]MCP {
	if len(c.EnabledMCP) == 0 {
		return c.MCP
	}
	out := map[string]MCP{}
	for _, name := range c.EnabledMCP {
		if s, ok := c.MCP[name]; ok {
			out[name] = s
		}
	}
	return out
}

func writeMCP(path string, m map[string]MCP) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
