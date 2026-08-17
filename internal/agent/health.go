package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// A ladder of free models is only useful if it remembers what just failed.
// Without that, a rate-limited model is retried on the next turn, fails again,
// and every turn pays the same tax. Three layers, because failures differ in
// what they imply:
//
//   - A cooldown on one model, for a rate limit. It will clear by itself, and
//     the wait grows each time so a model that keeps refusing is asked less
//     often rather than hammered.
//   - A circuit breaker on a whole endpoint, for connection failures and 5xx.
//     If a server is down, its other models are down too, and trying them in
//     turn just spends time discovering that.
//   - A lockout on one model, for the session, when the failure can never
//     clear: a name that does not exist, a request it cannot serve.
const (
	// baseCooldown is the first wait after a rate limit. It doubles per
	// consecutive refusal.
	baseCooldown = 30 * time.Second
	// maxCooldown caps the wait, so a model is never written off for a whole
	// session by a temporary quota.
	maxCooldown = 15 * time.Minute
	// breakerThreshold is how many endpoint failures open the circuit. One can
	// be noise; three in a row is the endpoint.
	breakerThreshold = 3
	// breakerCooldown is how long an endpoint is left alone once its circuit
	// opens.
	breakerCooldown = 2 * time.Minute
)

// health records what has recently failed and why.
//
// It is guarded by a mutex because a sub-agent runs on its own goroutine and
// shares the tracker with its caller: both walk the same ladder, and the point
// is that one learning something spares the other from rediscovering it.
type health struct {
	mu sync.Mutex
	// coolUntil and strikes are per model reference.
	coolUntil map[string]time.Time
	strikes   map[string]int
	// breakerUntil and failures are per provider.
	breakerUntil map[string]time.Time
	failures     map[string]int
	// locked is permanent for the session, with the reason kept for reporting.
	locked map[string]string
}

func newHealth() *health {
	return &health{
		coolUntil:    map[string]time.Time{},
		strikes:      map[string]int{},
		breakerUntil: map[string]time.Time{},
		failures:     map[string]int{},
		locked:       map[string]string{},
	}
}

// hp returns the tracker, creating it if an Agent was built without one. Only
// hand-constructed agents hit that path; New always provides one.
func (a *Agent) hp() *health {
	if a.health == nil {
		a.health = newHealth()
	}
	return a.health
}

// providerOf takes the provider from a "provider/model" reference.
func providerOf(ref string) string {
	name, _, _ := strings.Cut(ref, "/")
	return name
}

// Available reports whether a model is worth trying, and why not when it is not.
func (h *health) Available(ref string) (bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if why, ok := h.locked[ref]; ok {
		return false, "locked out: " + why
	}
	if until, ok := h.breakerUntil[providerOf(ref)]; ok && time.Now().Before(until) {
		return false, fmt.Sprintf("%s is down, retrying in %s",
			providerOf(ref), until.Sub(time.Now()).Round(time.Second))
	}
	if until, ok := h.coolUntil[ref]; ok && time.Now().Before(until) {
		return false, fmt.Sprintf("cooling down for %s",
			until.Sub(time.Now()).Round(time.Second))
	}
	return true, ""
}

// RateLimited puts a model on cooldown, backing off further each time it
// refuses in a row.
func (h *health) RateLimited(ref string) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.strikes[ref]++
	wait := baseCooldown << min(h.strikes[ref]-1, 8)
	if wait > maxCooldown {
		wait = maxCooldown
	}
	h.coolUntil[ref] = time.Now().Add(wait)
	return wait
}

// Unavailable counts an endpoint failure, opening the circuit once enough have
// happened in a row. It reports whether the circuit opened.
func (h *health) Unavailable(ref string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	name := providerOf(ref)
	h.failures[name]++
	if h.failures[name] < breakerThreshold {
		return false
	}
	h.breakerUntil[name] = time.Now().Add(breakerCooldown)
	h.failures[name] = 0
	return true
}

// LockOut writes a model off for the rest of the session.
func (h *health) LockOut(ref, why string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.locked[ref] = why
}

// Succeeded clears what a working request disproves: this model is not cooling,
// and its endpoint is not down.
func (h *health) Succeeded(ref string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.strikes, ref)
	delete(h.coolUntil, ref)
	delete(h.failures, providerOf(ref))
	delete(h.breakerUntil, providerOf(ref))
}

// Note is one line about something being held back, for reporting.
type Note struct {
	Ref    string
	Reason string
}

// Held lists what is currently unavailable, so /status can show why a ladder
// looks shorter than it is.
func (h *health) Held() []Note {
	h.mu.Lock()
	defer h.mu.Unlock()

	var out []Note
	now := time.Now()
	for ref, why := range h.locked {
		out = append(out, Note{Ref: ref, Reason: "locked out: " + why})
	}
	for ref, until := range h.coolUntil {
		if now.Before(until) {
			out = append(out, Note{Ref: ref,
				Reason: "cooling down for " + until.Sub(now).Round(time.Second).String()})
		}
	}
	for name, until := range h.breakerUntil {
		if now.Before(until) {
			out = append(out, Note{Ref: name,
				Reason: "endpoint down, retrying in " + until.Sub(now).Round(time.Second).String()})
		}
	}
	return out
}
