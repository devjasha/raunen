package agent

import (
	"strings"
	"testing"
	"time"
)

// A rate-limited model has to be left alone for a while, and left alone longer
// each time it refuses — otherwise every turn pays the same tax.
func TestCooldownBacksOff(t *testing.T) {
	h := newHealth()
	const ref = "groq/llama"

	if ok, _ := h.Available(ref); !ok {
		t.Fatal("a fresh model was not available")
	}

	first := h.RateLimited(ref)
	if first != baseCooldown {
		t.Errorf("first cooldown = %s, want %s", first, baseCooldown)
	}
	ok, why := h.Available(ref)
	if ok {
		t.Error("a rate-limited model was still offered")
	}
	if !strings.Contains(why, "cooling down") {
		t.Errorf("reason = %q, want it to mention cooling down", why)
	}

	if second := h.RateLimited(ref); second <= first {
		t.Errorf("second cooldown = %s, want longer than %s", second, first)
	}

	// A model must never be written off for a whole session by a quota.
	for i := 0; i < 20; i++ {
		if got := h.RateLimited(ref); got > maxCooldown {
			t.Fatalf("cooldown grew to %s, past the %s cap", got, maxCooldown)
		}
	}
}

// One failure can be noise. Several in a row is the endpoint, and its other
// models are no more likely to answer.
func TestBreakerOpensOnRepeatedFailures(t *testing.T) {
	h := newHealth()

	for i := 1; i < breakerThreshold; i++ {
		if h.Unavailable("groq/model-a") {
			t.Fatalf("breaker opened after %d failures, want %d", i, breakerThreshold)
		}
	}
	if !h.Unavailable("groq/model-a") {
		t.Fatal("breaker did not open at the threshold")
	}

	// The whole endpoint goes, not just the model that failed.
	ok, why := h.Available("groq/model-b")
	if ok {
		t.Error("another model on a downed endpoint was still offered")
	}
	if !strings.Contains(why, "groq") {
		t.Errorf("reason = %q, want it to name the endpoint", why)
	}
	// Other endpoints are unaffected.
	if ok, _ := h.Available("cerebras/model-c"); !ok {
		t.Error("an unrelated endpoint was taken down with it")
	}
}

// A name that does not exist will not start existing. Retrying it is pure cost.
func TestLockoutIsPermanent(t *testing.T) {
	h := newHealth()
	h.LockOut("nvidia/typo", "the endpoint rejected it")

	ok, why := h.Available("nvidia/typo")
	if ok {
		t.Error("a locked-out model was still offered")
	}
	if !strings.Contains(why, "locked out") {
		t.Errorf("reason = %q, want it to say locked out", why)
	}

	// Unlike a cooldown, this does not expire — nothing here can clear it.
	h.coolUntil["nvidia/typo"] = time.Now().Add(-time.Hour)
	if ok, _ := h.Available("nvidia/typo"); ok {
		t.Error("a lockout expired like a cooldown")
	}
}

// A request that works disproves both a cooldown and a downed endpoint, so it
// has to clear them — otherwise a single blip suppresses a good model.
func TestSuccessClears(t *testing.T) {
	h := newHealth()
	h.RateLimited("groq/llama")
	h.Unavailable("groq/llama")
	h.Unavailable("groq/llama")

	h.Succeeded("groq/llama")

	if ok, why := h.Available("groq/llama"); !ok {
		t.Errorf("still held after a success: %s", why)
	}
	// The failure count reset too, so the next blip starts from scratch.
	if h.Unavailable("groq/llama") {
		t.Error("breaker opened on the first failure after a success")
	}
}

// The ladder must skip what is known to be failing, or resilience is only
// bookkeeping.
func TestEscalateSkipsUnhealthyRungs(t *testing.T) {
	a := &Agent{
		ref: "ollama/local", contextTokens: 4096, autoSwitch: true,
		fallbacks: []Candidate{
			{Ref: "groq/cooling", Context: 131072},
			{Ref: "nvidia/broken", Context: 131072},
			{Ref: "cerebras/good", Context: 131072},
		},
	}
	a.hp().RateLimited("groq/cooling")
	a.hp().LockOut("nvidia/broken", "rejected")

	out := make(chan Event, 4)
	if !a.escalate(forFailure, "out of room", out) {
		t.Fatal("did not escalate")
	}
	if a.ref != "cerebras/good" {
		t.Errorf("escalated to %s, want cerebras/good — the others are known bad", a.ref)
	}
	drain(out)
}

// What is being held back has to be reportable, or a ladder that looks short
// has no explanation.
func TestHeldReportsReasons(t *testing.T) {
	h := newHealth()
	h.RateLimited("groq/llama")
	h.LockOut("nvidia/typo", "rejected")

	notes := h.Held()
	if len(notes) != 2 {
		t.Fatalf("Held returned %d notes, want 2", len(notes))
	}
	seen := map[string]string{}
	for _, n := range notes {
		seen[n.Ref] = n.Reason
	}
	if !strings.Contains(seen["groq/llama"], "cooling") {
		t.Errorf("groq/llama reason = %q", seen["groq/llama"])
	}
	if !strings.Contains(seen["nvidia/typo"], "locked") {
		t.Errorf("nvidia/typo reason = %q", seen["nvidia/typo"])
	}
}
