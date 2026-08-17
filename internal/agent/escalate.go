package agent

import (
	"fmt"

	"raunen/internal/provider"
)

// Candidate is a model the agent may escalate to.
type Candidate struct {
	Ref     string
	Client  *provider.Client
	Context int
}

// Switched reports that the agent moved to a different model mid-conversation.
type Switched struct {
	From, To string
	Reason   string
}

func (Switched) event() {}

// Held reports what the ladder is currently holding back, and why.
func (a *Agent) Held() []Note {
	return a.hp().Held()
}

// Ladder reports the escalation ladder, for showing what will happen when this
// model runs out of room.
func (a *Agent) Ladder() []Candidate { return a.fallbacks }

// SetFallbacks installs the escalation ladder. It is tried in order, so larger
// contexts belong later in the list.
func (a *Agent) SetFallbacks(c []Candidate) { a.fallbacks = c }

// nextCandidate returns the next unused rung of the ladder, skipping any whose
// context is known to be no larger than the current one — moving sideways would
// hit the same ceiling again.
//
// A candidate with no declared context is allowed through: the user put it in
// the ladder deliberately, and hosted models usually have room. Ordering is
// treated as the user's intent rather than second-guessed.
func (a *Agent) nextCandidate() (Candidate, bool) {
	for i, c := range a.fallbacks {
		if i < a.rung {
			continue
		}
		if c.Ref == a.ref {
			continue
		}
		// Skip what is known to be failing rather than rediscovering it.
		if ok, _ := a.hp().Available(c.Ref); !ok {
			continue
		}
		// A smaller window is pointless when the problem is room — but when the
		// problem is a rate limit, any model that answers is an improvement.
		if !a.rateLimited && c.Context > 0 && a.contextTokens > 0 && c.Context <= a.contextTokens {
			continue
		}
		return c, true
	}
	return Candidate{}, false
}

// escalate moves to the next model, reporting why. It returns false when the
// ladder is exhausted, which leaves the caller to fail in the ordinary way.
func (a *Agent) escalate(reason string, out chan<- Event) bool {
	if !a.autoSwitch {
		return false
	}
	c, ok := a.nextCandidate()
	if !ok {
		return false
	}

	a.rateLimited = reason == "rate limited"
	from := a.ref
	// Advance past this rung so a turn cannot loop between two models.
	for i, x := range a.fallbacks {
		if x.Ref == c.Ref {
			a.rung = i + 1
			break
		}
	}

	a.client = c.Client
	a.ref = c.Ref
	a.contextTokens = c.Context

	out <- Switched{From: from, To: c.Ref, Reason: reason}
	return true
}

// needsMoreRoom reports whether the next request is close enough to the ceiling
// that trimming alone will not save it.
//
// Trimming can only drop earlier exchanges and older tool results; it will never
// drop the system prompt or the question being answered. Once those alone fill
// most of the window, there is nothing left to cut and the model will be
// truncated mid-answer — which is the point of switching rather than retrying.
func (a *Agent) needsMoreRoom() (string, bool) {
	if a.contextTokens <= 0 {
		return "", false
	}
	essential := estimateTokens(a.essentials()) + a.overhead()
	if essential*10 >= a.contextTokens*7 {
		return fmt.Sprintf("the question and its results need %d tokens of a %d-token window",
			essential, a.contextTokens), true
	}
	return "", false
}

// essentials are the messages trimming is not allowed to drop: the system
// prompt, the question being answered, and everything since.
func (a *Agent) essentials() []provider.Message {
	u := lastUser(a.messages)
	if u == 0 {
		return a.messages
	}
	out := make([]provider.Message, 0, len(a.messages)-u+1)
	out = append(out, a.messages[0])
	out = append(out, a.messages[u:]...)
	return out
}
