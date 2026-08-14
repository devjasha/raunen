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
	// Context is the model's context window in tokens. It is optional and only
	// drives the usage bar: set it and the bar shows how full the context is,
	// leave it out and the bar shows a raw token count. There is no way to ask
	// an OpenAI-compatible endpoint for this, so it has to be declared.
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
	// System overrides the built-in system prompt when non-empty.
	System string `json:"system,omitempty"`
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

func defaults() *Config {
	return &Config{
		Default: "ollama/qwen3.5:latest",
		Providers: map[string]Provider{
			// Ollama defaults to a 4096-token context regardless of what the
			// model supports; raise OLLAMA_CONTEXT_LENGTH and this together.
			"ollama":   {BaseURL: "http://localhost:11434/v1", Context: 4096},
			"lmstudio": {BaseURL: "http://localhost:1234/v1"},
			"llamacpp": {BaseURL: "http://localhost:8080/v1"},
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
	if len(c.Providers) == 0 {
		c.Providers = defaults().Providers
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
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
