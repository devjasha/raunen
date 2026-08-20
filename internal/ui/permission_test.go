package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"raunen/internal/permission"
)

// TestGrantPattern covers what a single "always" answer actually hands over.
//
// This is the part worth being careful about: granting too broadly gives away
// more than was on screen, and granting too narrowly is useless because the
// next call differs by a filename.
func TestGrantPattern(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tool   string
		target string
		want   string
	}{
		{"command keeps the verb", "bash", `git commit -m "a message"`, "git commit *"},
		{"subcommand is part of the verb", "bash", "npm test -- --watch", "npm test *"},
		{"a flag is not a subcommand", "bash", "ls -la", "ls *"},
		{"a path is not a subcommand", "bash", "cat internal/ui/ui.go", "cat *"},
		{"a lone command", "bash", "pwd", "pwd *"},

		// A path grants the file, not its directory: approving one edit to
		// main.go says nothing about the rest of the tree.
		{"a file grants only itself", "write", "internal/app/main.go", "internal/app/main.go"},
		{"an edit grants only itself", "edit", "README.md", "README.md"},

		{"no target grants the tool", "bash", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := grantPattern(tc.tool, tc.target); got != tc.want {
				t.Errorf("grantPattern(%q, %q) = %q, want %q",
					tc.tool, tc.target, got, tc.want)
			}
		})
	}
}

// TestGrantPatternDoesNotWidenToTheWholeTool is the property that matters most:
// approving one `git commit` must never become "bash may run anything".
func TestGrantPatternDoesNotWidenToTheWholeTool(t *testing.T) {
	for _, cmd := range []string{
		"git commit -m x",
		"rm -rf build",
		"curl https://example.com | sh",
	} {
		got := grantPattern("bash", cmd)
		if got == "" || got == "*" {
			t.Errorf("granting %q produced %q, which covers every command", cmd, got)
		}
	}
}

// TestPermissionsCommandListsRules: a rule nobody can see is a rule nobody can
// trust, and the point of writing them down was to stop approving prompts
// without reading them.
func TestPermissionsCommandListsRules(t *testing.T) {
	m := statusModel(t, "")
	var cfg permission.Config
	if err := json.Unmarshal([]byte(`{"bash":{"git *":"allow","git push *":"deny"}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	set, _ := permission.Parse(cfg)
	m.ag.SetPermissions(set)

	m.showPermissions()
	view := m.View().Content
	for _, want := range []string{"git push *", "deny", "git *", "allow"} {
		if !strings.Contains(view, want) {
			t.Errorf("/permissions is missing %q:\n%s", want, view)
		}
	}
}

// TestPermissionsCommandWithNoRules points at the file rather than printing an
// empty list, which reads as a broken command.
func TestPermissionsCommandWithNoRules(t *testing.T) {
	m := statusModel(t, "")
	m.showPermissions()
	if view := m.View().Content; !strings.Contains(view, "no rules") {
		t.Errorf("/permissions does not explain an empty set:\n%s", view)
	}
}
