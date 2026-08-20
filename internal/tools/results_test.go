package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// call runs a tool by name with the given arguments.
func runTool(t *testing.T, r *Registry, name string, a map[string]any) string {
	t.Helper()
	tool, ok := r.Get(name)
	if !ok {
		t.Fatalf("tool %q missing", name)
	}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Run(context.Background(), raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

// handleOf pulls the r-handle out of a bounded result.
func handleOf(t *testing.T, s string) string {
	t.Helper()
	i := strings.Index(s, "kept as ")
	if i < 0 {
		t.Fatalf("no handle in result: %.200s", s)
	}
	rest := s[i+len("kept as "):]
	j := strings.IndexByte(rest, ':')
	if j < 0 {
		t.Fatalf("malformed handle in result: %.200s", rest)
	}
	return rest[:j]
}

// bigFile writes a file of n numbered lines and returns its name.
func bigFile(t *testing.T, dir string, n int) string {
	t.Helper()
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "line %d: some filler text to make this worth storing\n", i)
	}
	name := "big.txt"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

// The whole point: a large result costs a page of context, not all of it.
func TestLargeResultIsPreviewedNotPasted(t *testing.T) {
	dir := t.TempDir()
	name := bigFile(t, dir, 4000)
	full, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}

	r := Default(dir, OutputBudget(8192))
	out := runTool(t, r, "read", map[string]any{"path": name})

	if len(out) >= len(full)/4 {
		t.Errorf("result is %d bytes of a %d byte file; it should be a preview", len(out), len(full))
	}
	if len(out) > PreviewBudget(OutputBudget(8192))+400 {
		t.Errorf("preview is %d bytes, over budget", len(out))
	}
	// The head has to actually be the head, or the preview is useless.
	if !strings.Contains(out, "line 1:") {
		t.Error("preview does not start at the beginning of the file")
	}
	if !strings.Contains(out, "more lines") {
		t.Errorf("no indication that more exists: %.200s", out)
	}
}

// A small result must pass through untouched: no handle, no ceremony.
func TestSmallResultIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Default(dir, OutputBudget(8192))
	out := runTool(t, r, "read", map[string]any{"path": "small.txt"})
	if strings.Contains(out, "kept as") {
		t.Errorf("small result was stored: %q", out)
	}
	if !strings.Contains(out, "1\talpha") || !strings.Contains(out, "2\tbeta") {
		t.Errorf("small result was altered: %q", out)
	}
}

// The interesting six lines of a huge log are findable without spending the
// whole log on context. This is the case the store exists for.
func TestSearchTheWholeStoredResult(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 3000; i++ {
		if i == 2500 {
			sb.WriteString("FATAL: the needle in the haystack\n")
			continue
		}
		fmt.Fprintf(&sb, "ordinary log line %d with padding to take up room\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "log.txt"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Default(dir, OutputBudget(8192))
	out := runTool(t, r, "read", map[string]any{"path": "log.txt"})
	if strings.Contains(out, "FATAL") {
		t.Fatal("the needle was inside the preview; the test proves nothing")
	}
	id := handleOf(t, out)

	got := runTool(t, r, "result", map[string]any{"id": id, "match": "FATAL"})
	if !strings.Contains(got, "the needle in the haystack") {
		t.Errorf("match did not find the needle: %q", got)
	}
	// It should say where, so the model can page around it.
	if !strings.Contains(got, "2500") {
		t.Errorf("match did not report the line number: %q", got)
	}
}

// Paging has to be continuable: each page says where the next one starts.
func TestPageThroughAStoredResult(t *testing.T) {
	dir := t.TempDir()
	name := bigFile(t, dir, 4000)
	r := Default(dir, OutputBudget(8192))
	id := handleOf(t, runTool(t, r, "read", map[string]any{"path": name}))

	page := runTool(t, r, "result", map[string]any{"id": id, "from": 1000, "lines": 5})
	for _, want := range []string{"line 1000:", "line 1004:"} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q: %q", want, page)
		}
	}
	if strings.Contains(page, "line 1005:") {
		t.Errorf("page ran past the requested count: %q", page)
	}
	if !strings.Contains(page, "continue from line 1005") {
		t.Errorf("page does not say how to continue: %q", page)
	}
}

// A page must never be able to undo the saving that storing it made.
func TestAPageIsBounded(t *testing.T) {
	dir := t.TempDir()
	name := bigFile(t, dir, 20000)
	budget := OutputBudget(8192)
	r := Default(dir, budget)
	id := handleOf(t, runTool(t, r, "read", map[string]any{"path": name}))

	// Ask for everything at once; the tool must refuse to hand it over.
	page := runTool(t, r, "result", map[string]any{"id": id, "from": 1, "lines": 20000})
	if len(page) > pageBytes(budget)+400 {
		t.Errorf("page is %d bytes, over the %d byte budget", len(page), pageBytes(budget))
	}
	if !strings.Contains(page, "more lines") {
		t.Errorf("bounded page does not say it was cut: %.200s", page)
	}
}

// A dropped or invented handle must tell the model to re-run the tool, not
// leave it guessing at other handles.
func TestUnknownHandleSaysWhatToDo(t *testing.T) {
	r := Default(t.TempDir(), OutputBudget(8192))
	tool, _ := r.Get("result")
	_, err := tool.Run(context.Background(), json.RawMessage(`{"id":"r999"}`))
	if err == nil {
		t.Fatal("unknown handle did not error")
	}
	if !strings.Contains(err.Error(), "again") {
		t.Errorf("error does not suggest re-running: %v", err)
	}
}

// An MCP server is a third party; "keep your output short" cannot be asked of
// one. Anything added to the registry is bounded by construction.
func TestToolsAddedLaterAreBounded(t *testing.T) {
	r := Default(t.TempDir(), OutputBudget(8192))
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&b, "record %d from someone else's server\n", i)
	}
	huge := b.String()
	r.Add(Tool{
		Name: "mcp_thing",
		Run: func(context.Context, json.RawMessage) (string, error) {
			return huge, nil
		},
	})
	out := runTool(t, r, "mcp_thing", nil)
	if len(out) >= len(huge)/4 {
		t.Errorf("unbounded tool result: %d bytes of %d", len(out), len(huge))
	}
	if !strings.Contains(out, "kept as") {
		t.Errorf("result was truncated rather than stored: %.200s", out)
	}
}

// Cloning for MCP and stripping task for a sub-agent must not lose the bound,
// and must not apply it twice.
func TestBoundSurvivesCloneAndWithout(t *testing.T) {
	dir := t.TempDir()
	name := bigFile(t, dir, 4000)
	r := Default(dir, OutputBudget(8192)).Clone().Without("edit")

	out := runTool(t, r, "read", map[string]any{"path": name})
	if !strings.Contains(out, "kept as") {
		t.Fatalf("bound lost through Clone/Without: %.200s", out)
	}
	// Double-wrapping would store the preview as a result of its own, so the
	// handle in hand would resolve to a document that is itself truncated.
	id := handleOf(t, out)
	page := runTool(t, r, "result", map[string]any{"id": id, "from": 3990})
	if !strings.Contains(page, "line 3990:") {
		t.Errorf("stored result was not the full original: %q", page)
	}
}

// The store is bounded, and delegated tasks write to it from several goroutines
// at once.
func TestStoreIsBoundedAndConcurrent(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.Put("bash", fmt.Sprintf("result %d\n", i))
		}(i)
	}
	wg.Wait()
	if got := s.Len(); got > storeMaxItems {
		t.Errorf("store holds %d results, over the %d cap", got, storeMaxItems)
	}
	if s.Len() == 0 {
		t.Error("store evicted everything")
	}
}

// An error is short and is not evidence to page through; it must reach the
// model as it is.
func TestErrorsAreNotStored(t *testing.T) {
	r := Default(t.TempDir(), OutputBudget(8192))
	tool, _ := r.Get("read")
	_, err := tool.Run(context.Background(), json.RawMessage(`{"path":"nope.txt"}`))
	if err == nil {
		t.Fatal("reading a missing file did not error")
	}
	if strings.Contains(err.Error(), "kept as") {
		t.Errorf("error was routed through the store: %v", err)
	}
}

// Bash output is bounded like everything else, and its exit marker survives:
// the model needs to know the command failed, and that lives at the end.
func TestBashOutputIsBounded(t *testing.T) {
	r := Default(t.TempDir(), OutputBudget(8192))
	out := runTool(t, r, "bash", map[string]any{
		"command": "for i in $(seq 1 5000); do echo \"line $i padding padding padding\"; done",
	})
	if !strings.Contains(out, "kept as") {
		t.Errorf("large bash result was not stored: %.200s", out)
	}
	id := handleOf(t, out)
	got := runTool(t, r, "result", map[string]any{"id": id, "match": "line 4999 "})
	if !strings.Contains(got, "line 4999") {
		t.Errorf("stored bash result is incomplete: %q", got)
	}
}

// Whether the command failed is the one thing the model must never have to page
// for, so the exit marker goes at the head, where bounding cannot reach it.
func TestExitStatusSurvivesBounding(t *testing.T) {
	r := Default(t.TempDir(), OutputBudget(8192))
	out := runTool(t, r, "bash", map[string]any{
		"command": "for i in $(seq 1 5000); do echo \"line $i padding padding\"; done; exit 3",
	})
	if !strings.Contains(out, "kept as") {
		t.Fatalf("result was not large enough to be stored: %.200s", out)
	}
	if !strings.Contains(out, "[exit:") {
		t.Errorf("exit status was cut off by bounding: %.300s", out)
	}
}
