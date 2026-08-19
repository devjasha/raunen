package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"raunen/internal/provider"
	"raunen/internal/tools"
)

// dispatchAgent builds an agent whose only job is to run tool calls, with no
// model behind it: dispatchAll is the unit under test here.
func dispatchAgent(t *testing.T, reg *tools.Registry) *Agent {
	t.Helper()
	a := New(provider.New("http://localhost:1/v1", "", "m"), reg, "")
	return a
}

// drain consumes events in the background so dispatch never blocks writing to
// an unread channel, and returns them once the channel closes.
func drainAsync(out chan Event) func() []Event {
	var mu sync.Mutex
	var got []Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range out {
			mu.Lock()
			got = append(got, ev)
			mu.Unlock()
		}
	}()
	return func() []Event {
		close(out)
		<-done
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

func call(id, name, args string) provider.ToolCall {
	return provider.ToolCall{
		ID:       id,
		Function: provider.Function{Name: name, Arguments: args},
	}
}

// TestTasksRunConcurrently is the point of the feature. Three delegated tasks
// that each sleep must finish in about the time of one, not three — a sub-agent
// spends its time waiting on a model, so waiting in parallel is the whole win.
func TestTasksRunConcurrently(t *testing.T) {
	const n = 3
	const dwell = 150 * time.Millisecond

	reg := &tools.Registry{}
	var peak, running int64
	reg.Add(tools.Tool{
		Name:   "task",
		Params: map[string]any{"type": "object"},
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			cur := atomic.AddInt64(&running, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
					break
				}
			}
			time.Sleep(dwell)
			atomic.AddInt64(&running, -1)
			return "done", nil
		},
	})

	a := dispatchAgent(t, reg)
	out := make(chan Event, 128)
	collect := drainAsync(out)

	calls := make([]provider.ToolCall, n)
	for i := range calls {
		calls[i] = call(fmt.Sprint(i), "task", `{}`)
	}

	start := time.Now()
	results := a.dispatchAll(context.Background(), calls, out)
	elapsed := time.Since(start)
	collect()

	if got := atomic.LoadInt64(&peak); got < n {
		t.Errorf("peak concurrency %d, want %d — tasks did not overlap", got, n)
	}
	// Generous: the point is that it is nothing like the sequential n*dwell.
	if elapsed > dwell*2 {
		t.Errorf("took %v, want well under %v (sequential would be %v)",
			elapsed, dwell*2, dwell*n)
	}
	for i, r := range results {
		if r != "done" {
			t.Errorf("result %d is %q", i, r)
		}
	}
}

// TestResultsKeepCallOrder guards the thing most likely to break silently. The
// API rejects a tool result that does not line up with its call, so results
// must come back in the order asked for however they finish.
func TestResultsKeepCallOrder(t *testing.T) {
	reg := &tools.Registry{}
	reg.Add(tools.Tool{
		Name:   "task",
		Params: map[string]any{"type": "object"},
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Delay int    `json:"delay"`
				Tag   string `json:"tag"`
			}
			_ = json.Unmarshal(raw, &args)
			time.Sleep(time.Duration(args.Delay) * time.Millisecond)
			return args.Tag, nil
		},
	})

	a := dispatchAgent(t, reg)
	out := make(chan Event, 128)
	collect := drainAsync(out)

	// Deliberately finishing backwards: the last call returns first.
	calls := []provider.ToolCall{
		call("0", "task", `{"delay":120,"tag":"first"}`),
		call("1", "task", `{"delay":60,"tag":"second"}`),
		call("2", "task", `{"delay":0,"tag":"third"}`),
	}
	results := a.dispatchAll(context.Background(), calls, out)
	collect()

	want := []string{"first", "second", "third"}
	for i := range want {
		if results[i] != want[i] {
			t.Errorf("result %d is %q, want %q — results were reordered by completion",
				i, results[i], want[i])
		}
	}
}

// TestOrdinaryToolsStaySequential is the other half of the design: only tasks
// run in parallel. Two edits racing on one file is a worse failure than any
// saving is worth.
func TestOrdinaryToolsStaySequential(t *testing.T) {
	reg := &tools.Registry{}
	var peak, running int64
	reg.Add(tools.Tool{
		Name:   "write",
		Params: map[string]any{"type": "object"},
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			cur := atomic.AddInt64(&running, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt64(&running, -1)
			return "ok", nil
		},
	})

	a := dispatchAgent(t, reg)
	out := make(chan Event, 128)
	collect := drainAsync(out)

	calls := []provider.ToolCall{
		call("0", "write", `{}`),
		call("1", "write", `{}`),
		call("2", "write", `{}`),
	}
	a.dispatchAll(context.Background(), calls, out)
	collect()

	if got := atomic.LoadInt64(&peak); got != 1 {
		t.Errorf("peak concurrency %d, want 1 — ordinary tools must not overlap", got)
	}
}

// TestApprovalsAreSerialised covers the race that would be worst in practice:
// two children reaching a mutating tool at once, two prompts issued, and the
// user's single y approving something they were never shown.
func TestApprovalsAreSerialised(t *testing.T) {
	reg := &tools.Registry{}
	reg.Add(tools.Tool{
		Name:    "task",
		Params:  map[string]any{"type": "object"},
		Mutates: true,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			return "ok", nil
		},
	})

	a := dispatchAgent(t, reg)
	a.SetMode(ModeAccept)

	out := make(chan Event, 128)

	// Answer approvals one at a time, checking that a second is never in flight
	// while the first is unanswered.
	var open int64
	var overlapped atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range out {
			ap, ok := ev.(Approval)
			if !ok {
				continue
			}
			if atomic.AddInt64(&open, 1) > 1 {
				overlapped.Store(true)
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt64(&open, -1)
			ap.Reply <- true
		}
	}()

	calls := []provider.ToolCall{
		call("0", "task", `{}`),
		call("1", "task", `{}`),
		call("2", "task", `{}`),
	}
	a.dispatchAll(context.Background(), calls, out)
	close(out)
	<-done

	if overlapped.Load() {
		t.Error("two approval prompts were open at once; the user has one keyboard")
	}
}

// TestTaskEventsCarryTheirID checks that a frontend can tell concurrent
// sub-agents apart. Without an ID every event says only "depth 1", and three
// children collapse into one unreadable stream.
func TestTaskEventsCarryTheirID(t *testing.T) {
	a := withSubagents(t)
	// No model is reachable, so each child fails fast — the IDs are what matter.
	out := make(chan Event, 256)
	a.out = out

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := fmt.Sprintf(`{"description":"task %d","prompt":"do %d"}`, i, i)
			_, _ = a.runTask(context.Background(), json.RawMessage(args))
		}(i)
	}
	wg.Wait()
	close(out)

	starts := map[string]string{}
	ends := map[string]bool{}
	for ev := range out {
		switch e := ev.(type) {
		case TaskStart:
			if e.ID == "" {
				t.Error("TaskStart has no ID")
			}
			if prev, dup := starts[e.ID]; dup {
				t.Errorf("ID %q reused: %q and %q", e.ID, prev, e.Description)
			}
			starts[e.ID] = e.Description
		case TaskEnd:
			if e.ID == "" {
				t.Error("TaskEnd has no ID")
			}
			ends[e.ID] = true
		}
	}

	if len(starts) != 3 {
		t.Errorf("got %d distinct task IDs, want 3", len(starts))
	}
	for id := range starts {
		if !ends[id] {
			t.Errorf("task %q started but never ended", id)
		}
	}
	// Descriptions must not have been shuffled between children.
	for id, desc := range starts {
		if !strings.HasPrefix(desc, "task ") {
			t.Errorf("task %q has description %q", id, desc)
		}
	}
}

// A turn asked while another is running gets a fork. It has to see everything
// said up to that moment — it is joining a conversation, not starting one.
func TestForkSeesTheConversationSoFar(t *testing.T) {
	a := dispatchAgent(t, &tools.Registry{})
	a.Note("the parser is in parse.go")

	f := a.Fork()
	if len(f.Messages()) != len(a.Messages()) {
		t.Fatalf("the fork sees %d messages, the conversation has %d",
			len(f.Messages()), len(a.Messages()))
	}
	if f.Messages()[len(f.Messages())-1].Content != "the parser is in parse.go" {
		t.Error("the fork did not inherit what had been said")
	}
}

// The two must not share the message slice, or a tool result appended by one
// turn lands in the middle of the other's exchange.
func TestForkOwnsItsTranscript(t *testing.T) {
	a := dispatchAgent(t, &tools.Registry{})
	a.Note("first")
	before := len(a.Messages())

	f := a.Fork()
	f.Note("only the fork said this")

	if len(a.Messages()) != before {
		t.Errorf("the conversation grew to %d messages when the fork wrote to its own copy", len(a.Messages()))
	}
}

// Health and the approval lock are shared on purpose: what one turn learns
// about a dead endpoint should spare the other from finding out again, and the
// user has one keyboard.
func TestForkSharesHealthAndApprovals(t *testing.T) {
	a := dispatchAgent(t, &tools.Registry{})
	f := a.Fork()

	if f.health != a.health {
		t.Error("the fork has its own health, so it will walk into endpoints the caller already found dead")
	}
	if f.approving != a.approving {
		t.Error("the fork has its own approval lock, so two questions could reach one keyboard at once")
	}
}

// What a fork worked out has to reach the conversation, or the answer is on
// screen but missing from the transcript and from the session on disk.
func TestMergeFoldsTheExchangeBack(t *testing.T) {
	a := dispatchAgent(t, &tools.Registry{})
	a.Note("earlier discussion")
	before := len(a.Messages())

	f := a.Fork()
	f.messages = append(f.messages,
		provider.Message{Role: provider.User, Content: "the question"},
		provider.Message{Role: provider.Assistant, Content: "the answer"})
	f.Merge()

	got := a.Messages()
	if len(got) != before+2 {
		t.Fatalf("the conversation has %d messages after merging, want %d", len(got), before+2)
	}
	if got[len(got)-2].Content != "the question" || got[len(got)-1].Content != "the answer" {
		t.Errorf("the merged exchange is wrong: %q, %q",
			got[len(got)-2].Content, got[len(got)-1].Content)
	}
}

// A fork may trim or compact its own copy while it works. The question being
// answered is the one thing neither ever drops, which is why the exchange is
// cut from there rather than from a remembered offset.
func TestMergeSurvivesTheForkTrimmingItself(t *testing.T) {
	a := dispatchAgent(t, &tools.Registry{})
	for i := 0; i < 5; i++ {
		a.Note(fmt.Sprintf("earlier %d", i))
	}
	before := len(a.Messages())

	f := a.Fork()
	f.messages = append(f.messages, provider.Message{Role: provider.User, Content: "the question"})
	// Everything before the question goes, as trimming would take it.
	f.messages = append(f.messages[:1], f.messages[len(f.messages)-1:]...)
	f.messages = append(f.messages, provider.Message{Role: provider.Assistant, Content: "the answer"})
	f.Merge()

	got := a.Messages()
	if len(got) != before+2 {
		t.Fatalf("the conversation has %d messages, want %d — trimming lost the exchange", len(got), before+2)
	}
	if got[before].Content != "the question" {
		t.Errorf("the merge cut in the wrong place: %q", got[before].Content)
	}
}

// Merging twice would say everything the fork did a second time.
func TestMergeHappensOnce(t *testing.T) {
	a := dispatchAgent(t, &tools.Registry{})
	f := a.Fork()
	f.messages = append(f.messages, provider.Message{Role: provider.User, Content: "q"})
	f.Merge()
	after := len(a.Messages())
	f.Merge()

	if len(a.Messages()) != after {
		t.Error("merging a second time duplicated the exchange")
	}
}

// The conversation itself is not a fork, so merging it is a no-op rather than
// a doubling.
func TestMergingTheConversationDoesNothing(t *testing.T) {
	a := dispatchAgent(t, &tools.Registry{})
	a.Note("something")
	before := len(a.Messages())
	a.Merge()
	if len(a.Messages()) != before {
		t.Error("merging the conversation into itself changed it")
	}
}

// Two turns answered side by side must not touch each other's state. This is
// the failure the fork exists to prevent, and only the race detector sees it:
// without it, two turns on one agent append to the same slice from two
// goroutines and the corruption shows up much later as a rejected request.
//
// Run with -race for this to mean anything.
func TestConcurrentTurnsDoNotRace(t *testing.T) {
	reg := &tools.Registry{}
	reg.Add(tools.Tool{
		Name:   "look",
		Params: map[string]any{"type": "object"},
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			time.Sleep(5 * time.Millisecond)
			return "looked", nil
		},
	})
	a := dispatchAgent(t, reg)
	a.Note("shared history")

	const n = 4
	forks := make([]*Agent, n)
	var wg sync.WaitGroup
	for i := range forks {
		f := a.Fork()
		forks[i] = f
		wg.Add(1)
		go func(f *Agent, i int) {
			defer wg.Done()
			out := make(chan Event, 64)
			collect := drainAsync(out)
			// No model behind it, so the turn fails at the first request —
			// which is enough: the transcript is appended to either way, and
			// that is what the two would race on.
			f.messages = append(f.messages,
				provider.Message{Role: provider.User, Content: fmt.Sprintf("question %d", i)})
			f.dispatchAll(context.Background(), []provider.ToolCall{call("1", "look", `{}`)}, out)
			collect()
		}(f, i)
	}
	wg.Wait()

	// Merging is sequential — the frontend does it as each turn's channel
	// closes — and every exchange has to arrive.
	before := len(a.Messages())
	for _, f := range forks {
		f.Merge()
	}
	if got := len(a.Messages()); got != before+n {
		t.Errorf("the conversation has %d messages after merging %d turns, want %d",
			got, n, before+n)
	}
}
