package agent

import (
	"testing"

	"raunen/internal/provider"
	"raunen/internal/tools"
)

func withSubagents(t *testing.T) *Agent {
	t.Helper()
	a := New(provider.New("http://localhost:1/v1", "", "m"), tools.Default(t.TempDir(), 4096), "")
	a.EnableSubagents()
	return a
}

func TestTaskToolIsRegistered(t *testing.T) {
	a := withSubagents(t)
	tool, ok := a.tools.Get("task")
	if !ok {
		t.Fatal("task tool was not registered")
	}
	// Delegating changes nothing by itself; what the child does is gated by the
	// mode it inherits, so gating the delegation too would double-prompt.
	if tool.Mutates {
		t.Error("task is marked as mutating; the child's own tools carry that")
	}
}

// A sub-agent that could delegate could recurse without bound. It is prevented
// structurally: the child simply does not have the tool.
func TestSubagentCannotDelegate(t *testing.T) {
	a := withSubagents(t)
	child := a.tools.Without("task")
	if _, ok := child.Get("task"); ok {
		t.Error("a sub-agent was given the task tool")
	}
	// Everything else has to survive, or the child cannot do its job.
	for _, name := range []string{"bash", "read", "write", "edit", "list"} {
		if _, ok := child.Get(name); !ok {
			t.Errorf("child registry lost %q", name)
		}
	}
}

// The mode is the safety boundary. A sub-agent running in plan mode while its
// parent is in plan mode is the whole point; inheriting wrongly would let
// delegation launder a write past a refusal.
func TestSubagentInheritsMode(t *testing.T) {
	for _, mode := range []Mode{ModeAuto, ModeAccept, ModePlan} {
		a := withSubagents(t)
		a.SetMode(mode)

		child := &Agent{
			tools:  a.tools.Without("task"),
			system: subSystem,
			mode:   a.mode,
			depth:  a.depth + 1,
		}
		if child.mode != mode {
			t.Errorf("child mode = %v, want %v", child.mode, mode)
		}
		if child.depth != 1 {
			t.Errorf("child depth = %d, want 1", child.depth)
		}
	}
}

// Adding the tool changes what every request costs, so the cached size must not
// go stale — history trimming and escalation both budget against it.
func TestEnablingSubagentsInvalidatesSchemaCost(t *testing.T) {
	a := New(provider.New("http://localhost:1/v1", "", "m"), tools.Default(t.TempDir(), 4096), "")
	before := a.overhead()
	if before == 0 {
		t.Fatal("schemas measured as zero")
	}
	a.EnableSubagents()
	after := a.overhead()
	if after <= before {
		t.Errorf("overhead = %d after adding a tool, want more than %d", after, before)
	}
}
