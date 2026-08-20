package ui

import (
	"strings"
	"testing"

	"raunen/internal/agent"
	"raunen/internal/companion"
	"raunen/internal/config"
	"raunen/internal/provider"
	"raunen/internal/session"
	"raunen/internal/tools"
)

func mcpCfg() *config.Config {
	return &config.Config{MCP: map[string]config.MCP{
		"github": {Type: "http", URL: "https://api.example.com/mcp/"},
		"gitlab": {Type: "http", URL: "https://gitlab.example.com/mcp/"},
		"local":  {Command: "go", Args: []string{"run", "x"}},
	}}
}

// A bare "/mcp " offers every configured server, which is what makes the
// completion box a way to see what is defined rather than something that only
// helps once the name is already known.
func TestMCPArgSuggestListsServers(t *testing.T) {
	text := "/mcp "
	s := suggestFor(text, len([]rune(text)), nil, mcpCfg())
	if s == nil {
		t.Fatal("expected suggestions after /mcp")
	}
	if s.kind != sugMCP {
		t.Errorf("kind = %v, want sugMCP", s.kind)
	}
	var got []string
	for _, it := range s.items {
		got = append(got, it.insert)
	}
	if len(got) != 3 {
		t.Errorf("expected all 3 servers, got %v", got)
	}
	// Stable order, so the list does not reshuffle between looks.
	if got[0] != "github" || got[1] != "gitlab" || got[2] != "local" {
		t.Errorf("expected sorted names, got %v", got)
	}
}

// Typing narrows the list by prefix, the same way commands and skills do.
func TestMCPArgSuggestFiltersByPrefix(t *testing.T) {
	text := "/mcp git"
	s := suggestFor(text, len([]rune(text)), nil, mcpCfg())
	if s == nil {
		t.Fatal("expected suggestions")
	}
	if len(s.items) != 2 {
		t.Fatalf("expected github and gitlab, got %d", len(s.items))
	}
	text = "/mcp gith"
	s = suggestFor(text, len([]rune(text)), nil, mcpCfg())
	if s == nil || len(s.items) != 1 || s.items[0].insert != "github" {
		t.Errorf("expected only github, got %+v", s)
	}
}

// The detail is where the server is reached, which is what tells two names
// apart in a list that is otherwise just words.
func TestMCPArgSuggestShowsEndpoint(t *testing.T) {
	text := "/mcp github"
	s := suggestFor(text, len([]rune(text)), nil, mcpCfg())
	if s == nil || len(s.items) == 0 {
		t.Fatal("expected a suggestion")
	}
	if !strings.Contains(s.items[0].detail, "api.example.com") {
		t.Errorf("detail should show the url, got %q", s.items[0].detail)
	}
}

// A name that matches nothing offers nothing, rather than an empty box taking
// rows off the transcript.
func TestMCPArgSuggestNoMatch(t *testing.T) {
	text := "/mcp zzz"
	if s := suggestFor(text, len([]rune(text)), nil, mcpCfg()); s != nil {
		t.Errorf("expected no suggestions, got %+v", s.items)
	}
}

// /mcp takes one argument, so a second word is prose and must not keep
// offering server names.
func TestMCPArgSuggestStopsAfterOneArg(t *testing.T) {
	text := "/mcp github extra"
	if s := suggestFor(text, len([]rune(text)), nil, mcpCfg()); s != nil && s.kind == sugMCP {
		t.Errorf("should not offer servers for a second argument, got %+v", s.items)
	}
}

// The command itself still completes: adding an argument must not shadow the
// list of commands that a bare "/mc" produces.
func TestMCPCommandStillCompletes(t *testing.T) {
	text := "/mc"
	s := suggestFor(text, len([]rune(text)), nil, mcpCfg())
	if s == nil || s.kind != sugCommand {
		t.Fatalf("expected the command list, got %+v", s)
	}
	var found bool
	for _, it := range s.items {
		if it.insert == "/mcp" {
			found = true
			if !strings.Contains(it.label, "[server]") {
				t.Errorf("label should show the argument, got %q", it.label)
			}
		}
	}
	if !found {
		t.Error("/mcp missing from the command list")
	}
}

// /mcp <server> reports that one server in full, including how it is reached —
// the detail the one-line list has no room for.
func TestMCPServerDetail(t *testing.T) {
	ag := agent.New(provider.New("http://localhost:1/v1", "", "m"),
		tools.Default(t.TempDir(), 4096), "")
	m := New(mcpCfg(), ag, t.TempDir(), "x/m", session.New(t.TempDir(), "x/m"), companion.Load())
	m.SetMCPSummary(map[string]int{"github": 44})
	m.width, m.height = 80, 40

	ret, _ := m.command("/mcp github")
	out := ret.(Model).View().Content
	for _, want := range []string{"api.example.com", "44 tools", "http"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail should contain %q:\n%s", want, out)
		}
	}
}

// A server that is defined but never started is the case worth explaining, so
// the detail says so rather than reporting zero tools and leaving it there.
func TestMCPServerDetailNotStarted(t *testing.T) {
	ag := agent.New(provider.New("http://localhost:1/v1", "", "m"),
		tools.Default(t.TempDir(), 4096), "")
	m := New(mcpCfg(), ag, t.TempDir(), "x/m", session.New(t.TempDir(), "x/m"), companion.Load())
	m.SetMCPSummary(nil)
	m.width, m.height = 80, 40

	ret, _ := m.command("/mcp github")
	if out := ret.(Model).View().Content; !strings.Contains(out, "not started") {
		t.Errorf("expected a not-started explanation:\n%s", out)
	}
}

// An unknown name says so and lists what is defined, rather than printing an
// empty report for a server that does not exist.
func TestMCPServerDetailUnknown(t *testing.T) {
	ag := agent.New(provider.New("http://localhost:1/v1", "", "m"),
		tools.Default(t.TempDir(), 4096), "")
	m := New(mcpCfg(), ag, t.TempDir(), "x/m", session.New(t.TempDir(), "x/m"), companion.Load())
	m.width, m.height = 80, 40

	ret, _ := m.command("/mcp nope")
	out := ret.(Model).View().Content
	if !strings.Contains(out, "no MCP server called nope") {
		t.Errorf("expected an unknown-server message:\n%s", out)
	}
	if !strings.Contains(out, "github") {
		t.Errorf("expected the defined names to be listed:\n%s", out)
	}
}

// Secrets in a server definition must not be printed: the detail names the env
// and header keys and never their values.
func TestMCPServerDetailHidesSecrets(t *testing.T) {
	cfg := &config.Config{MCP: map[string]config.MCP{
		"secret": {
			Type: "http", URL: "https://x.example.com/mcp/",
			Headers: map[string]string{"Authorization": "Bearer hunter2token"},
			Env:     map[string]string{"API_KEY": "swordfish"},
		},
	}}
	ag := agent.New(provider.New("http://localhost:1/v1", "", "m"),
		tools.Default(t.TempDir(), 4096), "")
	m := New(cfg, ag, t.TempDir(), "x/m", session.New(t.TempDir(), "x/m"), companion.Load())
	m.width, m.height = 80, 40

	ret, _ := m.command("/mcp secret")
	out := ret.(Model).View().Content
	for _, leak := range []string{"hunter2token", "swordfish"} {
		if strings.Contains(out, leak) {
			t.Errorf("secret %q leaked into the transcript:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "Authorization") || !strings.Contains(out, "API_KEY") {
		t.Errorf("expected the key names to be listed:\n%s", out)
	}
}
