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

func statusModel(t *testing.T, summary string) Model {
	t.Helper()
	ag := agent.New(provider.New("http://localhost:1/v1", "", "m"),
		tools.Default(t.TempDir(), 4096), "")
	m := New(&config.Config{}, ag, t.TempDir(), "x/m", session.New(t.TempDir(), "x/m"), companion.Load())
	m.SetProject(summary)
	m.width, m.height = 100, 60
	return m
}

// The row has to name the files, since "2 files" does not answer the only
// question anyone asks here: whether the one just written is among them.
func TestStatusNamesInstructionFiles(t *testing.T) {
	m := statusModel(t, "AGENTS.md, apps/web/AGENTS.md")
	m.status()
	view := m.View().Content
	for _, want := range []string{"project", "apps/web/AGENTS.md"} {
		if !strings.Contains(view, want) {
			t.Errorf("status is missing %q:\n%s", want, view)
		}
	}
}

// With no file, the row points at the thing to create rather than vanishing.
func TestStatusSuggestsInstructionFile(t *testing.T) {
	m := statusModel(t, "")
	m.status()
	if view := m.View().Content; !strings.Contains(view, "AGENTS.md") {
		t.Errorf("status does not mention AGENTS.md when none is loaded:\n%s", view)
	}
}
