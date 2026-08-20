package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"raunen/internal/agent"
	"raunen/internal/provider"
	"raunen/internal/session"
	"raunen/internal/tools"
)

// endpoint stands in for a model. It replies with whatever frames the test
// gives it, so a turn can be driven down a specific path — a tool call, an
// error, a plain answer — without a real provider.
type endpoint struct {
	*httptest.Server
	// frames are returned one call after another, so a turn that takes two
	// requests can be scripted as two entries.
	frames []string
	status int
	calls  int
}

func newEndpoint(t *testing.T, frames ...string) *endpoint {
	t.Helper()
	e := &endpoint{frames: frames}
	e.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e.status != 0 {
			w.WriteHeader(e.status)
			fmt.Fprint(w, `{"error":{"message":"the endpoint is on fire"}}`)
			return
		}
		i := min(e.calls, len(e.frames)-1)
		e.calls++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, e.frames[i])
	}))
	t.Cleanup(e.Close)
	return e
}

// textFrame is a model that answers in prose and stops.
func textFrame(reply string) string {
	frame, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"delta":         map[string]any{"content": reply},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18,
		},
	})
	return fmt.Sprintf("data: %s\n\ndata: [DONE]\n\n", frame)
}

// toolFrame is a model that calls one tool and waits for the result.
func toolFrame(name, args string) string {
	frame, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"delta": map[string]any{"tool_calls": []map[string]any{{
				"index": 0, "id": "c1", "type": "function",
				"function": map[string]any{"name": name, "arguments": args},
			}}},
			"finish_reason": "tool_calls",
		}},
	})
	return fmt.Sprintf("data: %s\n\ndata: [DONE]\n\n", frame)
}

// runOneShot drives a turn and captures what went to stdout.
func runOneShot(t *testing.T, e *endpoint, opts oneShotOpts, prompt string) (string, error) {
	t.Helper()
	root := t.TempDir()
	ag := agent.New(provider.New(e.URL+"/v1", "", "fake"),
		tools.Default(root, tools.OutputBudget(8192)), "")
	ag.SetRef("fake/fake")
	sess := session.New(root, "fake/fake")

	// Sessions are written to XDG_DATA_HOME; keep the test out of the real one.
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := oneShot(ag, sess, prompt, opts)
	w.Close()
	os.Stdout = old

	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String(), runErr
}

// decode parses the result document, failing the test if stdout was not valid
// JSON — which is the contract the whole mode rests on.
func decode(t *testing.T, out string) runResult {
	t.Helper()
	var res runResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out)
	}
	return res
}

// TestJSONReportsTheAnswer is the base case: the reply, the model and the
// tokens, all in one document.
func TestJSONReportsTheAnswer(t *testing.T) {
	e := newEndpoint(t, textFrame("Hello."))
	out, err := runOneShot(t, e, oneShotOpts{json: true, save: true}, "hi")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	res := decode(t, out)
	if res.Output != "Hello." {
		t.Errorf("output = %q, want %q", res.Output, "Hello.")
	}
	if res.Model != "fake/fake" {
		t.Errorf("model = %q", res.Model)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", res.ExitCode)
	}
	if res.Usage.Total != 18 {
		t.Errorf("usage.total = %d, want 18", res.Usage.Total)
	}
	if res.SessionID == "" {
		t.Error("session_id is empty on a saved run")
	}
}

// TestJSONToolCallsAreListed covers what a script actually wants to know: which
// tools ran, and whether they worked.
func TestJSONToolCallsAreListed(t *testing.T) {
	e := newEndpoint(t, toolFrame("list", `{"path":"."}`), textFrame("Done."))
	out, err := runOneShot(t, e, oneShotOpts{json: true}, "list the files")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	res := decode(t, out)
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v, want one entry", res.ToolCalls)
	}
	if res.ToolCalls[0].Name != "list" || res.ToolCalls[0].Status != "success" {
		t.Errorf("tool call = %+v, want list/success", res.ToolCalls[0])
	}
	if res.Steps != 1 {
		t.Errorf("steps = %d, want 1", res.Steps)
	}
}

// TestJSONRecordsAFailedTool keeps a tool error out of the run's own status: the
// model is told and usually recovers, so the turn has not failed.
func TestJSONRecordsAFailedTool(t *testing.T) {
	e := newEndpoint(t, toolFrame("read", `{"path":"no-such-file"}`), textFrame("It is missing."))
	out, err := runOneShot(t, e, oneShotOpts{json: true}, "read it")
	if err != nil {
		t.Fatalf("a failed tool should not fail the run: %v", err)
	}

	res := decode(t, out)
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].Status != "error" {
		t.Errorf("status = %q, want error", res.ToolCalls[0].Status)
	}
	if res.ToolCalls[0].Error == "" {
		t.Error("a failed tool call carries no error message")
	}
	if res.ExitCode != 0 {
		t.Errorf("exit_code = %d; a recovered tool error is not a failed run", res.ExitCode)
	}
}

// TestJSONOnFailureStillPrintsADocument is the reason the error is a field. A
// consumer parsing stdout should never have to handle "sometimes JSON".
func TestJSONOnFailureStillPrintsADocument(t *testing.T) {
	e := newEndpoint(t, textFrame("unused"))
	// 400 rather than 500: a 500 is retried with backoff, which would make this
	// test spend five seconds proving something about formatting.
	e.status = http.StatusBadRequest

	out, err := runOneShot(t, e, oneShotOpts{json: true}, "hi")
	if err == nil {
		t.Fatal("a failed run returned no error")
	}

	res := decode(t, out)
	if res.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", res.ExitCode)
	}
	if res.Error == "" {
		t.Error("error field is empty on a failed run")
	}
	// The endpoint's own words, not a paraphrase.
	if !strings.Contains(res.Error, "on fire") {
		t.Errorf("error = %q, want the endpoint's message", res.Error)
	}
}

// TestJSONExitCodeTravels checks the status reaches main, which is what makes
// `raunen --json ... || handle` work.
func TestJSONExitCodeTravels(t *testing.T) {
	e := newEndpoint(t, textFrame("unused"))
	e.status = http.StatusBadRequest

	_, err := runOneShot(t, e, oneShotOpts{json: true}, "hi")
	var ex exitError
	if !asExit(err, &ex) {
		t.Fatalf("error %v does not carry an exit code", err)
	}
	if ex.code != 1 {
		t.Errorf("exit code = %d, want 1", ex.code)
	}
	if !ex.quiet {
		t.Error("JSON mode should not print the error twice")
	}
}

// TestToolCallsIsNeverNull pins a detail a consumer depends on: `.tool_calls |
// length` must work on a turn that called nothing.
func TestToolCallsIsNeverNull(t *testing.T) {
	e := newEndpoint(t, textFrame("No tools needed."))
	out, _ := runOneShot(t, e, oneShotOpts{json: true}, "hi")

	if !strings.Contains(out, `"tool_calls": []`) {
		t.Errorf("tool_calls is not an empty array:\n%s", out)
	}
}

// TestNoSaveLeavesNoSession covers the flag, including that the document says so
// rather than naming a session that was never written.
func TestNoSaveLeavesNoSession(t *testing.T) {
	e := newEndpoint(t, textFrame("Hello."))
	out, err := runOneShot(t, e, oneShotOpts{json: true, save: false}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if res := decode(t, out); res.SessionID != "" {
		t.Errorf("session_id = %q, want empty with --no-save", res.SessionID)
	}
}

// TestTextModeStaysPlain is the compatibility guarantee. `raunen 'q' | pbcopy`
// has to keep working, so stdout carries the answer and nothing else.
func TestTextModeStaysPlain(t *testing.T) {
	e := newEndpoint(t, textFrame("Just the answer."))
	out, err := runOneShot(t, e, oneShotOpts{}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "Just the answer." {
		t.Errorf("stdout = %q, want the bare answer", out)
	}
	if strings.Contains(out, "{") {
		t.Errorf("text mode leaked JSON to stdout: %q", out)
	}
}

// TestOneShotSavesTheSession is a bug fix as much as a feature: a one-shot turn
// and an interactive one produce the same conversation, and `raunen 'q'` then
// `raunen --continue` used to lose the question.
func TestOneShotSavesTheSession(t *testing.T) {
	e := newEndpoint(t, textFrame("Hello."))
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)

	root := t.TempDir()
	ag := agent.New(provider.New(e.URL+"/v1", "", "fake"),
		tools.Default(root, tools.OutputBudget(8192)), "")
	ag.SetRef("fake/fake")
	sess := session.New(root, "fake/fake")

	if err := oneShot(ag, sess, "remember this", oneShotOpts{save: true}); err != nil {
		t.Fatal(err)
	}

	saved, err := session.Latest(root)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil {
		t.Fatal("a one-shot run saved no session; --continue would find nothing")
	}
	if len(saved.Messages) < 2 {
		t.Errorf("session holds %d messages, want the question and the answer", len(saved.Messages))
	}
}

// asExit is errors.As without the import, kept local so the test reads as one
// thing rather than as a type assertion in the middle of an assertion.
func asExit(err error, target *exitError) bool {
	if ex, ok := err.(exitError); ok {
		*target = ex
		return true
	}
	return false
}
