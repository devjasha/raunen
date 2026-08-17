package agent

import (
	"errors"
	"strings"
	"testing"

	"raunen/internal/provider"
)

func ladder() []Candidate {
	return []Candidate{
		{Ref: "ollama/small", Context: 4096},
		{Ref: "ollama/medium", Context: 16384},
		{Ref: "openrouter/big"}, // no declared window
	}
}

func drain(ch chan Event) []Event {
	close(ch)
	var out []Event
	for e := range ch {
		out = append(out, e)
	}
	return out
}

func TestEscalateIsOffUnlessEnabled(t *testing.T) {
	a := &Agent{ref: "ollama/small", contextTokens: 4096, fallbacks: ladder()}
	out := make(chan Event, 4)
	if a.escalate(forFailure, "because", out) {
		t.Error("escalated with auto-switch off")
	}
	if got := drain(out); len(got) != 0 {
		t.Errorf("emitted %d events with auto-switch off", len(got))
	}
}

func TestEscalateClimbsAndReports(t *testing.T) {
	a := &Agent{ref: "ollama/small", contextTokens: 4096, autoSwitch: true, fallbacks: ladder()}
	out := make(chan Event, 8)

	if !a.escalate(forFailure, "too tight", out) {
		t.Fatal("did not escalate")
	}
	if a.ref != "ollama/medium" || a.contextTokens != 16384 {
		t.Fatalf("moved to %s (%d), want ollama/medium (16384)", a.ref, a.contextTokens)
	}

	// The undeclared window is allowed: it was put in the ladder deliberately.
	if !a.escalate(forFailure, "still tight", out) {
		t.Fatal("did not escalate to the undeclared candidate")
	}
	if a.ref != "openrouter/big" {
		t.Fatalf("moved to %s, want openrouter/big", a.ref)
	}

	// Ladder exhausted.
	if a.escalate(forFailure, "nowhere left", out) {
		t.Error("escalated past the end of the ladder")
	}

	events := drain(out)
	if len(events) != 2 {
		t.Fatalf("emitted %d switches, want 2", len(events))
	}
	first, ok := events[0].(Switched)
	if !ok {
		t.Fatalf("event 0 is %T, want Switched", events[0])
	}
	if first.From != "ollama/small" || first.To != "ollama/medium" || first.Reason != "too tight" {
		t.Errorf("switch = %+v, want small→medium with the reason carried", first)
	}
}

// When the move is for room, a model that is no roomier would hit the same
// ceiling again, so a smaller or equal window is skipped. This does not apply to
// a move forced by a refusal — see TestEscalateIgnoresSizeWhenRefused.
func TestEscalateSkipsNonUpgrades(t *testing.T) {
	a := &Agent{
		ref: "ollama/medium", contextTokens: 16384, autoSwitch: true,
		fallbacks: []Candidate{
			{Ref: "ollama/small", Context: 4096},
			{Ref: "ollama/same", Context: 16384},
			{Ref: "ollama/large", Context: 65536},
		},
	}
	out := make(chan Event, 4)
	if !a.escalate(forRoom, "tight", out) {
		t.Fatal("did not escalate")
	}
	if a.ref != "ollama/large" {
		t.Errorf("moved to %s, want ollama/large — smaller and equal windows must be skipped", a.ref)
	}
	drain(out)
}

// Escalation is for when trimming cannot help: what it may never drop already
// fills the window.
func TestNeedsMoreRoom(t *testing.T) {
	a := &Agent{contextTokens: 1024}
	a.messages = []provider.Message{
		{Role: provider.System, Content: "sys"},
		{Role: provider.User, Content: "short question"},
	}
	if _, tight := a.needsMoreRoom(); tight {
		t.Error("reported tight with a nearly empty conversation")
	}

	// A big tool result inside the current turn cannot be trimmed away.
	a.messages = append(a.messages,
		provider.Message{Role: provider.Assistant, ToolCalls: []provider.ToolCall{
			{ID: "c", Function: provider.Function{Name: "read", Arguments: "{}"}},
		}},
		provider.Message{Role: provider.ToolRole, ToolCallID: "c", Content: strings.Repeat("x", 3200)},
	)
	reason, tight := a.needsMoreRoom()
	if !tight {
		t.Fatal("did not report tight when the current turn alone fills the window")
	}
	if !strings.Contains(reason, "window") {
		t.Errorf("reason = %q, want it to explain the window", reason)
	}
}

// An unknown window means there is nothing to measure against, so escalation
// must not fire on a guess.
func TestNeedsMoreRoomSilentWithoutAContext(t *testing.T) {
	a := &Agent{contextTokens: 0}
	a.messages = []provider.Message{
		{Role: provider.System, Content: strings.Repeat("x", 100000)},
	}
	if _, tight := a.needsMoreRoom(); tight {
		t.Error("reported tight with no declared context")
	}
}

// Dropping earlier tool results makes the model forget what it found and repeat
// the work, so a roomier model must be preferred over trimming. This pins the
// order: escalate while anything is left on the ladder, trim only after.
func TestEscalationIsPreferredOverTrimming(t *testing.T) {
	a := &Agent{
		ref: "ollama/small", contextTokens: 1000, autoSwitch: true,
		fallbacks: []Candidate{{Ref: "cloud/big", Context: 100_000}},
	}
	a.messages = []provider.Message{
		{Role: provider.System, Content: "sys"},
		{Role: provider.User, Content: "find the bug"},
	}
	// Enough accumulated work to overflow the small window.
	for i := 0; i < 6; i++ {
		a.messages = append(a.messages,
			provider.Message{Role: provider.Assistant, ToolCalls: []provider.ToolCall{
				{ID: "c", Function: provider.Function{Name: "read", Arguments: "{}"}},
			}},
			provider.Message{Role: provider.ToolRole, ToolCallID: "c", Content: strings.Repeat("x", 400)},
		)
	}
	before := len(a.messages)

	if !a.wouldTrim() {
		t.Fatal("the conversation should not fit the small window")
	}

	out := make(chan Event, 8)
	// The loop escalates while it would otherwise trim.
	for a.wouldTrim() {
		if !a.escalate(forFailure, "context full", out) {
			break
		}
	}
	a.trim(out)
	close(out)

	if a.ref != "cloud/big" {
		t.Errorf("model = %s, want cloud/big — it should grow before dropping", a.ref)
	}
	if len(a.messages) != before {
		t.Errorf("messages = %d, want %d kept: nothing should be dropped once it fits",
			len(a.messages), before)
	}
	for ev := range out {
		if _, ok := ev.(Trimmed); ok {
			t.Error("trimmed despite a roomier model being available")
		}
	}
}

// With nowhere to grow, trimming is still the fallback — a turn that cannot be
// sent is worse than one that lost some history.
func TestTrimmingRemainsTheLastResort(t *testing.T) {
	a := &Agent{ref: "ollama/small", contextTokens: 1000, autoSwitch: true}
	a.messages = []provider.Message{
		{Role: provider.System, Content: "sys"},
		{Role: provider.User, Content: "q"},
	}
	for i := 0; i < 6; i++ {
		a.messages = append(a.messages,
			provider.Message{Role: provider.User, Content: strings.Repeat("x", 400)})
	}

	out := make(chan Event, 8)
	for a.wouldTrim() {
		if !a.escalate(forFailure, "context full", out) {
			break
		}
	}
	a.trim(out)
	close(out)

	var trimmed bool
	for ev := range out {
		if _, ok := ev.(Trimmed); ok {
			trimmed = true
		}
	}
	if !trimmed {
		t.Error("nothing was trimmed and there was nowhere to escalate")
	}
}

// A refusal makes size irrelevant. Insisting on a larger window here left a
// whole ladder unused: a model with a 1048576 window was refused for want of
// credits, and every free rung at 1000000 was skipped for being "too small",
// so the turn failed with somewhere perfectly good to go.
func TestEscalateIgnoresSizeWhenRefused(t *testing.T) {
	a := &Agent{
		ref: "openrouter/paid", contextTokens: 1_048_576, autoSwitch: true,
		fallbacks: []Candidate{{Ref: "openrouter/free", Context: 1_000_000}},
	}

	out := make(chan Event, 4)
	if !a.escalate(forFailure, "needs credits", out) {
		t.Fatal("did not escalate to a slightly smaller model after a refusal")
	}
	if a.ref != "openrouter/free" {
		t.Errorf("model = %s, want openrouter/free", a.ref)
	}
	drain(out)

	// The same ladder is correctly refused when the problem really is room.
	b := &Agent{
		ref: "openrouter/paid", contextTokens: 1_048_576, autoSwitch: true,
		fallbacks: []Candidate{{Ref: "openrouter/free", Context: 1_000_000}},
	}
	out2 := make(chan Event, 4)
	if b.escalate(forRoom, "context full", out2) {
		t.Error("escalated to a smaller window when the problem was room")
	}
	drain(out2)
}

// The endpoint's message has to survive intact: it is the only actionable part,
// and the body contains ": " itself, which a naive split mangled into a fragment
// of the middle of the JSON.
func TestEndpointMessageExtraction(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "json body with colons inside it",
			err: errors.New(`rate limited: https://openrouter.ai/api/v1 returned 429 Too Many Requests: ` +
				`{"error":{"message":"free-models-per-day. Add 10 credits to unlock 1000 free model requests per day","code":429,` +
				`"metadata":{"headers":{"X-RateLimit-Limit":"50"}}}}`),
			want: "free-models-per-day. Add 10 credits to unlock 1000 free model requests per day",
		},
		{
			name: "plain text",
			err:  errors.New("endpoint unavailable: http://localhost:11434/v1: connection refused"),
			want: "connection refused",
		},
		{
			name: "json that will not parse falls back rather than leaking a fragment",
			err:  errors.New("model unusable: https://x/v1 returned 400 Bad Request: {truncated"),
			want: "model unusable: https://x/v1 returned 400 Bad Request",
		},
	}
	for _, c := range cases {
		if got := endpointMessage(c.err); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// A shared allowance has to be recognised from the refusal, since that is what
// decides whether the provider's other models are worth trying.
func TestSharedQuotaDetection(t *testing.T) {
	shared := []string{
		`{"error":{"message":"free-models-per-day. Add 10 credits","metadata":{"limit_source":"openrouter_free_tier_daily"}}}`,
		`rate limited: daily limit reached`,
		`rate limited: 50 requests per day exceeded`,
	}
	for _, m := range shared {
		if !sharedQuota(errors.New(m)) {
			t.Errorf("not detected as a shared allowance: %s", m)
		}
	}
	// A plain throughput limit is this model's own and must not rest the
	// provider, which would take working models out with it.
	perModel := []string{
		"rate limited: too many requests, slow down",
		"rate limited: 20 requests per minute",
	}
	for _, m := range perModel {
		if sharedQuota(errors.New(m)) {
			t.Errorf("wrongly treated as a shared allowance: %s", m)
		}
	}
}
