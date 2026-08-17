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
