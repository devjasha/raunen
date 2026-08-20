package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"raunen/internal/agent"
	"raunen/internal/companion"
	"raunen/internal/config"
	"raunen/internal/provider"
	"raunen/internal/session"
	"raunen/internal/tools"
)

// TestMCPCommandRenders confirms that /mcp produces visible output in the live
// view, both when servers are configured and when none are. This guards against
// the command silently doing nothing — it is the only user-facing surface for
// MCP, so a no-op there is invisible to the user.
func TestMCPCommandRenders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cfg    *config.Config
		counts map[string]int
		want   string
	}{
		{"configured", &config.Config{MCP: map[string]config.MCP{
			"demo": {Command: "go", Args: []string{"run", "x"}},
		}}, map[string]int{"demo": 2}, "demo"},
		{"none", &config.Config{}, nil, "no MCP servers configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ag := agent.New(provider.New("http://localhost:1/v1", "", "m"),
				tools.Default(t.TempDir(), 4096), "")
			m := New(tc.cfg, ag, t.TempDir(), "x/m", session.New(t.TempDir(), "x/m"), companion.Load())
			m.SetMCPSummary(tc.counts)
			m.width = 80
			// Without a height the transcript pane collapses and View clips
			// everything but the last line or two, hiding what /mcp printed.
			m.height = 40
			ret, _ := m.command("/mcp")
			mm := ret.(Model)
			if !strings.Contains(mm.View().Content, tc.want) {
				t.Errorf("/mcp view does not contain %q:\n%s", tc.want, mm.View().Content)
			}
		})
	}
}

// newTestModel builds a model wired to a config, sized so View renders a full
// screen rather than the clipped last line or two.
func newTestModel(t *testing.T, cfg *config.Config, counts map[string]int) Model {
	t.Helper()
	ag := agent.New(provider.New("http://localhost:1/v1", "", "m"),
		tools.Default(t.TempDir(), 4096), "")
	m := New(cfg, ag, t.TempDir(), "x/m", session.New(t.TempDir(), "x/m"), companion.Load())
	m.SetMCPSummary(counts)
	m.width, m.height = 80, 40
	return m
}

// TestMCPOpensPicker checks that a bare /mcp opens the chooser rather than
// printing a list, which is what makes it read like /model and /branch. The
// state of each server is annotated in the list, since "which one is broken"
// is the usual reason for opening it.
func TestMCPOpensPicker(t *testing.T) {
	cfg := &config.Config{MCP: map[string]config.MCP{
		"demo":  {Command: "go", Args: []string{"run", "x"}},
		"other": {Type: "http", URL: "https://x.example.com/mcp/"},
	}}
	m := newTestModel(t, cfg, map[string]int{"demo": 2})

	ret, _ := m.command("/mcp")
	mm := ret.(Model)
	if mm.pick == nil {
		t.Fatal("/mcp did not open the picker")
	}
	if mm.pick.kind != pickMCP {
		t.Fatalf("picker kind = %v, want pickMCP", mm.pick.kind)
	}
	view := mm.View().Content
	for _, want := range []string{"search", "demo", "other", "2 tools", "not started"} {
		if !strings.Contains(view, want) {
			t.Errorf("picker view does not contain %q:\n%s", want, view)
		}
	}
}

// TestMCPPickerFilters confirms the overlay narrows as you type, so a long
// catalogue of servers stays usable.
func TestMCPPickerFilters(t *testing.T) {
	cfg := &config.Config{MCP: map[string]config.MCP{
		"github": {Type: "http", URL: "https://api.example.com/mcp/"},
		"gitlab": {Type: "http", URL: "https://gitlab.example.com/mcp/"},
		"linear": {Type: "http", URL: "https://linear.example.com/mcp/"},
	}}
	m := newTestModel(t, cfg, nil)
	ret, _ := m.command("/mcp")
	mm := ret.(Model)

	mm.pick.filter = "git"
	mm.pick.apply()
	if got := len(mm.pick.filtered); got != 2 {
		t.Fatalf("filtered to %d entries, want 2: %v", got, mm.pick.filtered)
	}
	if strings.Contains(strings.Join(mm.pick.filtered, " "), "linear") {
		t.Errorf("filter kept a non-match: %v", mm.pick.filtered)
	}
}

// TestMCPPickerEnterReports checks that choosing a server closes the overlay
// and prints that server in full — the same detail /mcp <server> gives, which
// is what the list is for.
func TestMCPPickerEnterReports(t *testing.T) {
	cfg := &config.Config{MCP: map[string]config.MCP{
		"github": {Type: "http", URL: "https://api.example.com/mcp/"},
	}}
	m := newTestModel(t, cfg, map[string]int{"github": 3})
	ret, _ := m.command("/mcp")
	mm := ret.(Model)

	ret2, _ := mm.onKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	out := ret2.(Model)
	if out.pick != nil {
		t.Error("picker stayed open after enter")
	}
	view := out.View().Content
	for _, want := range []string{"github", "https://api.example.com/mcp/", "3 tools"} {
		if !strings.Contains(view, want) {
			t.Errorf("report does not contain %q:\n%s", want, view)
		}
	}
}

// TestMCPWithNoServersStillExplains keeps the empty case printing advice: an
// empty chooser would be a dead end where the listing says where to define one.
func TestMCPWithNoServersStillExplains(t *testing.T) {
	m := newTestModel(t, &config.Config{}, nil)
	ret, _ := m.command("/mcp")
	mm := ret.(Model)
	if mm.pick != nil {
		t.Fatal("opened an empty picker instead of explaining")
	}
	if !strings.Contains(mm.View().Content, "no MCP servers configured") {
		t.Errorf("missing the advice:\n%s", mm.View().Content)
	}
}
