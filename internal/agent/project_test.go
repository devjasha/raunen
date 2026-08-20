package agent

import (
	"strings"
	"testing"

	"raunen/internal/provider"
	"raunen/internal/tools"
)

// newBare builds an agent without a client, which is enough to inspect how the
// system message is composed.
func newBare() *Agent {
	a := &Agent{system: DefaultSystem}
	a.messages = []provider.Message{{Role: provider.System, Content: a.prompt()}}
	return a
}

// TestProjectReachesTheSystemPrompt is the whole point: instructions that are
// loaded but never sent are indistinguishable from a model ignoring them.
func TestProjectReachesTheSystemPrompt(t *testing.T) {
	a := newBare()
	a.SetProject("Run the tests with `zig build test`.")

	got := a.messages[0].Content
	if !strings.Contains(got, "zig build test") {
		t.Errorf("system message does not carry the project instructions:\n%s", got)
	}
	if !strings.Contains(got, DefaultSystem) {
		t.Error("project instructions replaced the built-in prompt instead of adding to it")
	}
}

// TestModeChangeKeepsProject guards the ordering bug this design exists to
// avoid: SetMode rewrites the system message, and a naive rewrite drops
// whatever the project said.
func TestModeChangeKeepsProject(t *testing.T) {
	a := newBare()
	a.SetProject("House rule: no global state.")
	a.SetMode(ModePlan)

	got := a.messages[0].Content
	if !strings.Contains(got, "House rule") {
		t.Errorf("changing mode dropped the project instructions:\n%s", got)
	}
	if !strings.Contains(got, "plan mode") {
		t.Errorf("mode guidance is missing:\n%s", got)
	}
}

// TestModeGuidanceOutranksProject checks the order the prompt is assembled in.
// The mode's rules are not something a project file may talk its way out of, so
// the project block must not be the last word on what the agent may do — it
// comes after the guidance, but the guidance is what plan mode enforces in code
// regardless.
func TestModeGuidanceOutranksProject(t *testing.T) {
	a := newBare()
	a.SetMode(ModePlan)
	a.SetProject("Always commit your work when finished.")

	got := a.messages[0].Content
	plan := strings.Index(got, "plan mode")
	proj := strings.Index(got, "Always commit")
	if plan < 0 || proj < 0 {
		t.Fatalf("prompt is missing a part:\n%s", got)
	}
	if plan > proj {
		t.Error("project block precedes the mode rules; the mode must be read as binding")
	}
}

// TestEmptyProjectAddsNothing keeps the common case free. A project with no
// AGENTS.md must send exactly the prompt it sent before this feature existed.
func TestEmptyProjectAddsNothing(t *testing.T) {
	a := newBare()
	before := a.messages[0].Content
	a.SetProject("   \n  ")
	if got := a.messages[0].Content; got != before {
		t.Errorf("an empty project block changed the prompt:\n%q", got)
	}
	if a.Project() != "" {
		t.Errorf("Project() = %q, want empty", a.Project())
	}
}

// TestProjectCanBeReplaced covers re-reading the files: the new block has to
// replace the old one rather than stack on top of it.
func TestProjectCanBeReplaced(t *testing.T) {
	a := newBare()
	a.SetProject("first")
	a.SetProject("second")

	got := a.messages[0].Content
	if strings.Contains(got, "first") {
		t.Errorf("the previous project block survived:\n%s", got)
	}
	if !strings.Contains(got, "second") {
		t.Errorf("the new project block is missing:\n%s", got)
	}
}

// TestForkInheritsProject matters because a second question asked while the
// first is running is answered by a fork, and it edits the same working
// directory.
func TestForkInheritsProject(t *testing.T) {
	a := newBare()
	a.tools = &tools.Registry{}
	a.SetProject("House rule: no global state.")

	f := a.Fork()
	if !strings.Contains(f.messages[0].Content, "House rule") {
		t.Errorf("fork lost the project instructions:\n%s", f.messages[0].Content)
	}
}
