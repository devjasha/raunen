package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"raunen/internal/provider"
	"raunen/internal/tools"
)

// looper stands in for a model that keeps calling a tool. It asks for `calls`
// tool calls and then stops, so a test can tell "the turn ran to completion"
// apart from "something cut it off".
func looper(t *testing.T, calls int) *httptest.Server {
	t.Helper()
	var seen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen++
		w.Header().Set("Content-Type", "text/event-stream")

		var frame []byte
		if calls < 0 || seen <= calls {
			frame, _ = json.Marshal(map[string]any{
				"choices": []map[string]any{{
					"delta": map[string]any{"tool_calls": []map[string]any{{
						"index": 0,
						"id":    fmt.Sprintf("c%d", seen),
						"type":  "function",
						"function": map[string]any{
							"name":      "list",
							"arguments": `{"path":"."}`,
						},
					}}},
					"finish_reason": "tool_calls",
				}},
			})
		} else {
			frame, _ = json.Marshal(map[string]any{
				"choices": []map[string]any{{
					"delta":         map[string]any{"content": "done"},
					"finish_reason": "stop",
				}},
			})
		}
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func steppingAgent(t *testing.T, srv *httptest.Server) *Agent {
	t.Helper()
	a := New(provider.New(srv.URL+"/v1", "", "m"), tools.Default(t.TempDir(), 4096), "")
	return a
}

// collect runs a turn to completion and returns how many tools it ran and how
// it ended.
func collect(t *testing.T, a *Agent, prompt string) (tools int, end string, failed error) {
	t.Helper()
	events := make(chan Event, 1024)
	go a.Run(context.Background(), prompt, events)
	for ev := range events {
		switch e := ev.(type) {
		case ToolStart:
			tools++
		case TurnEnd:
			end = e.Text
		case Failed:
			failed = e.Err
		}
	}
	return tools, end, failed
}

// The bug this replaced: a turn stopped after a fixed number of steps and told
// the user it had "gave up", mid-task. Long tasks are legitimate, so by default
// nothing counts steps at all — including past the 40 that used to be the cap.
func TestATurnIsUnboundedByDefault(t *testing.T) {
	const calls = 50
	a := steppingAgent(t, looper(t, calls))
	a.SetMode(ModeAuto)

	tools, end, failed := collect(t, a, "work through it")

	if failed != nil {
		t.Fatalf("the turn failed: %v", failed)
	}
	if tools != calls {
		t.Errorf("ran %d tools, want %d — something is still capping the turn", tools, calls)
	}
	if end != "done" {
		t.Errorf("turn ended with %q, want the model's own closing text", end)
	}
	if a.maxSteps != 0 {
		t.Errorf("maxSteps defaults to %d, want 0 (unlimited)", a.maxSteps)
	}
}

// The backstop is opt-in, and exists for the model that loops rather than
// finishes. It has to stop, and it has to say why.
func TestMaxStepsStopsALoopingModel(t *testing.T) {
	a := steppingAgent(t, looper(t, -1)) // never stops asking
	a.SetMode(ModeAuto)
	a.SetMaxSteps(5)

	tools, _, failed := collect(t, a, "loop forever")

	if failed == nil {
		t.Fatal("a model that never stops was not stopped")
	}
	if tools != 5 {
		t.Errorf("ran %d tools, want 5", tools)
	}
	if got := failed.Error(); !strings.Contains(got, "max_steps") {
		t.Errorf("failure reads %q; it should name the setting that caused it", got)
	}
}

// A negative limit is a typo, not a request to stop before starting.
func TestNegativeMaxStepsMeansUnlimited(t *testing.T) {
	a := steppingAgent(t, looper(t, 2))
	a.SetMaxSteps(-1)
	if a.maxSteps != 0 {
		t.Fatalf("maxSteps = %d after SetMaxSteps(-1), want 0", a.maxSteps)
	}
	if _, _, failed := collect(t, a, "hello"); failed != nil {
		t.Errorf("the turn failed: %v", failed)
	}
}

// Sub-agents run the same loop, so the backstop has to reach them too — an
// unbounded child would defeat a bounded parent.
func TestSubagentInheritsMaxSteps(t *testing.T) {
	a := withSubagents(t)
	a.SetMaxSteps(7)

	child := &Agent{
		tools:    a.tools.Without("task"),
		maxSteps: a.maxSteps,
	}
	if child.maxSteps != 7 {
		t.Errorf("child maxSteps = %d, want 7", child.maxSteps)
	}
}
