package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"raunen/internal/permission"
	"raunen/internal/provider"
	"raunen/internal/tools"
)

// permAgent builds an agent whose only tool records whether it ran, so a test
// can tell "refused" from "allowed" without a model or a filesystem.
func permAgent(t *testing.T, rules string, mode Mode) (*Agent, *bool) {
	t.Helper()

	ran := false
	reg := &tools.Registry{}
	reg.Add(tools.Tool{
		Name:    "bash",
		Mutates: true,
		Run: func(context.Context, json.RawMessage) (string, error) {
			ran = true
			return "ok", nil
		},
	})

	a := New(provider.New("http://127.0.0.1:1/v1", "", "m"), reg, "")
	a.SetMode(mode)

	var cfg permission.Config
	if rules != "" {
		if err := json.Unmarshal([]byte(rules), &cfg); err != nil {
			t.Fatalf("bad fixture: %v", err)
		}
	}
	set, problems := permission.Parse(cfg)
	if len(problems) > 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	a.SetPermissions(set)
	return a, &ran
}

// dispatchOnce runs one tool call and returns what the model was told.
func dispatchOnce(t *testing.T, a *Agent, command string) string {
	t.Helper()
	out := make(chan Event, 16)
	tc := provider.ToolCall{
		ID: "c1",
		Function: provider.Function{
			Name:      "bash",
			Arguments: `{"command":"` + command + `"}`,
		},
	}
	result, _ := a.dispatch(context.Background(), tc, out)
	close(out)
	return result
}

// TestDenyHoldsInAutoMode is the property the whole feature rests on. Auto mode
// runs everything, which is exactly where an unattended agent most needs
// "never push" to mean never.
func TestDenyHoldsInAutoMode(t *testing.T) {
	a, ran := permAgent(t, `{"bash": {"git push *": "deny"}}`, ModeAuto)

	result := dispatchOnce(t, a, "git push origin main")
	if *ran {
		t.Error("a denied command ran in auto mode")
	}
	if !strings.Contains(result, "refused") {
		t.Errorf("model was told %q, want a refusal", result)
	}
	// The rule is quoted so the model can see what it ran into rather than
	// guessing, and so it does not retry the same thing.
	if !strings.Contains(result, "git push") {
		t.Errorf("refusal does not name the rule: %q", result)
	}
}

// TestDenyAppliesToReadOnlyTools: a denial is not advice about mutating calls.
// "Never read the secrets file" has to hold even though reading changes nothing.
func TestDenyAppliesToReadOnlyTools(t *testing.T) {
	ran := false
	reg := &tools.Registry{}
	reg.Add(tools.Tool{
		Name: "read", // not mutating
		Run: func(context.Context, json.RawMessage) (string, error) {
			ran = true
			return "contents", nil
		},
	})

	a := New(provider.New("http://127.0.0.1:1/v1", "", "m"), reg, "")
	var cfg permission.Config
	_ = json.Unmarshal([]byte(`{"read": {"*.env": "deny"}}`), &cfg)
	set, _ := permission.Parse(cfg)
	a.SetPermissions(set)

	out := make(chan Event, 16)
	result, _ := a.dispatch(context.Background(), provider.ToolCall{
		ID:       "c1",
		Function: provider.Function{Name: "read", Arguments: `{"path":"secrets.env"}`},
	}, out)
	close(out)

	if ran {
		t.Error("a denied read ran because the tool is read-only")
	}
	if !strings.Contains(result, "refused") {
		t.Errorf("model was told %q, want a refusal", result)
	}
}

// TestAllowSkipsThePromptInAcceptMode is what makes accept mode usable: without
// it the twentieth identical prompt is approved without being read.
func TestAllowSkipsThePromptInAcceptMode(t *testing.T) {
	a, ran := permAgent(t, `{"bash": {"git status*": "allow"}}`, ModeAccept)

	// No approval is answered here. If the rule did not take effect the
	// dispatch would block on an Approval nobody replies to, so the test would
	// hang rather than fail — which is why the channel is buffered and the
	// assertion is on ran.
	dispatchOnce(t, a, "git status")
	if !*ran {
		t.Error("an allowed command still asked for approval in accept mode")
	}
}

// TestUnmatchedStillAsksInAcceptMode is the compatibility half: a command no
// rule covers behaves exactly as it did before rules existed.
func TestUnmatchedStillAsksInAcceptMode(t *testing.T) {
	a, ran := permAgent(t, `{"bash": {"git status*": "allow"}}`, ModeAccept)

	out := make(chan Event, 16)
	done := make(chan string, 1)
	go func() {
		r, _ := a.dispatch(context.Background(), provider.ToolCall{
			ID:       "c1",
			Function: provider.Function{Name: "bash", Arguments: `{"command":"rm -rf build"}`},
		}, out)
		done <- r
	}()

	// The dispatch must ask before running anything.
	var asked bool
	for ev := range out {
		if ap, ok := ev.(Approval); ok {
			asked = true
			ap.Reply <- false
			break
		}
	}
	<-done

	if !asked {
		t.Error("an unmatched command did not ask in accept mode")
	}
	if *ran {
		t.Error("the command ran despite being declined")
	}
}

// TestPlanModeOutranksAllow pins the layering. A mode is a decision about this
// session; a rule written last week must not quietly undo it.
func TestPlanModeOutranksAllow(t *testing.T) {
	a, ran := permAgent(t, `{"bash": {"git *": "allow"}}`, ModePlan)

	result := dispatchOnce(t, a, "git commit -m x")
	if *ran {
		t.Error("an allow rule let a command run in plan mode")
	}
	if !strings.Contains(result, "plan mode") {
		t.Errorf("model was told %q, want the plan-mode refusal", result)
	}
}

// TestGrantsAreInherited: a denial must not be escapable by delegating past it,
// and a grant already given should not be asked for again by a sub-agent.
func TestGrantsAreInherited(t *testing.T) {
	a, _ := permAgent(t, `{"bash": {"git push *": "deny"}}`, ModeAuto)
	a.tools = a.tools.Clone()

	child := &Agent{perms: a.perms, tools: a.tools, mode: a.mode}
	if d, _, _ := child.perms.Decide("bash", "git push origin main"); d != permission.Deny {
		t.Errorf("child decided %v, want Deny — rules must reach sub-agents", d)
	}

	// A grant made by the parent is visible to the child, since the set is
	// shared by pointer rather than copied.
	a.perms.Grant("bash", "npm test*", permission.Allow)
	if d, _, _ := child.perms.Decide("bash", "npm test"); d != permission.Allow {
		t.Errorf("child decided %v, want the parent's grant to apply", d)
	}
}

// TestNoRulesBehavesAsBefore is the guarantee for every existing config.
func TestNoRulesBehavesAsBefore(t *testing.T) {
	a, ran := permAgent(t, "", ModeAuto)
	dispatchOnce(t, a, "anything at all")
	if !*ran {
		t.Error("a session with no rules refused a command")
	}
}
