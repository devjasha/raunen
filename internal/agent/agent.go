// Package agent implements the tool-use loop: prompt the model, run whatever
// tools it asks for, feed the results back, repeat until it stops asking.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"raunen/internal/provider"
	"raunen/internal/tools"
)

// Event is emitted as a turn progresses so a frontend can render it live.
// The UI layer decides how to display these; the loop stays presentation-free.
type Event interface{ event() }

// TextDelta is a fragment of assistant text as it streams in.
type TextDelta struct{ Text string }

// ReasoningDelta is a fragment of a reasoning model's thinking. It is shown
// live but never enters the transcript.
type ReasoningDelta struct{ Text string }

// ToolStart fires when the model has requested a tool and it is about to run.
// Depth is zero for the main conversation and one inside a sub-agent, so a
// frontend can nest the work without knowing how it was produced.
//
// Task identifies which sub-agent produced it, and is empty for the main
// conversation. Several sub-agents can be in flight at once, so depth alone no
// longer says where an event came from.
type ToolStart struct {
	Name  string
	Args  string
	Depth int
	Task  string
}

// ToolEnd carries the tool's result, or its error.
type ToolEnd struct {
	Name   string
	Result string
	Err    error
	Depth  int
	Task   string
}

// TaskStart fires when the model delegates to a sub-agent. ID names the child
// for the life of the task, so a frontend can follow several at once.
type TaskStart struct {
	ID          string
	Description string
}

// TaskEnd carries what the sub-agent reported back.
type TaskEnd struct {
	ID          string
	Description string
	Summary     string
	Steps       int
	Err         error
}

// TurnEnd fires once the model stops requesting tools.
type TurnEnd struct{ Text string }

// Usage reports token counts after each request to the model. Prompt is the
// size of the whole conversation, so it tracks context growth.
type Usage struct{ provider.Usage }

// Approval asks the frontend whether a state-changing tool may run. The loop
// blocks until something sends on Reply, so a frontend that receives one must
// always answer.
type Approval struct {
	Name  string
	Args  string
	Reply chan bool
}

// Rejected reports that a model was refused outright and will not be tried
// again this session.
type Rejected struct {
	Ref    string
	Reason string
}

// Retrying reports that a request is being sent again after the endpoint failed
// in a way that often clears by itself.
type Retrying struct {
	Attempt int
	After   time.Duration
	Reason  string
}

// Tripped reports that an endpoint has been taken out of rotation after
// repeated failures.
type Tripped struct {
	Provider string
	For      time.Duration
	// Reason distinguishes an endpoint that keeps failing from one whose
	// allowance has simply run out — the remedy is not the same.
	Reason string
}

// Trimmed reports that old exchanges were dropped to keep the request inside
// the model's context window.
type Trimmed struct{ Messages int }

// ModeChanged reports the mode the loop is running under, so a frontend can
// show it without tracking the agent's state itself.
type ModeChanged struct{ Mode Mode }

// Failed fires when the turn could not complete.
type Failed struct{ Err error }

func (TextDelta) event()      {}
func (ReasoningDelta) event() {}
func (ToolStart) event()      {}
func (ToolEnd) event()        {}
func (Usage) event()          {}
func (Approval) event()       {}
func (ModeChanged) event()    {}
func (Trimmed) event()        {}
func (Tripped) event()        {}
func (Rejected) event()       {}
func (Retrying) event()       {}
func (TaskStart) event()      {}
func (TaskEnd) event()        {}
func (TurnEnd) event()        {}
func (Failed) event()         {}

const DefaultSystem = `You are a terminal coding assistant. You have tools to read, write and edit
files and to run shell commands. Work in small concrete steps: inspect before
you change, and verify after. Prefer running a command to guessing at its
output. Keep replies short — the user is reading them in a terminal.`

// maxRetries is how many times a request is repeated after the endpoint fails
// in a way that tends to clear. Two is enough for a hiccup and short enough that
// a real outage is not waited out.
const maxRetries = 2

// retryDelay backs off a little between attempts, so a busy endpoint gets a
// moment rather than the same burst again.
func retryDelay(attempt int) time.Duration {
	return time.Duration(attempt) * 1500 * time.Millisecond
}

type Agent struct {
	client   *provider.Client
	tools    *tools.Registry
	messages []provider.Message
	system   string
	mode     Mode
	// contextTokens is the model's window, zero when it was not declared.
	contextTokens int
	// ref is the active "provider/model", tracked so escalation can report
	// what it moved away from.
	ref string
	// fallbacks is the escalation ladder; rung is how far up it we are.
	fallbacks  []Candidate
	rung       int
	autoSwitch bool
	// maxSteps optionally bounds the tool-calling steps in one turn. Zero, the
	// default, means unbounded; it exists only to stop a model that loops.
	maxSteps int
	// health remembers what has recently failed, so the same dead end is not
	// walked into every turn. Shared with sub-agents by pointer.
	health *health
	// attempt counts consecutive retries of the same request after a transient
	// failure. Reset by any success.
	attempt int
	// schemaTokens is what the tool definitions cost on every request. They are
	// not part of the message list but they are very much part of the prompt:
	// leaving them out understates a small model's usage by hundreds of tokens.
	schemaTokens int
	// out is the current turn's event channel. Delegated tasks run concurrently,
	// so several goroutines can write to it; Go channels are safe for that, and
	// the frontend sees one interleaved stream tagged by task.
	out chan<- Event
	// depth is zero for the main conversation and one inside a sub-agent.
	depth int
	// approving serialises approval prompts across the whole agent tree. Shared
	// with sub-agents by pointer, like health: the user has one keyboard, and
	// two questions at once would have an answer land on the wrong one.
	approving *sync.Mutex
	// parent is the agent this one was forked from to answer a turn beside it,
	// nil for the conversation itself. Merge hands the finished exchange back.
	parent *Agent
}

func New(c *provider.Client, r *tools.Registry, system string) *Agent {
	if system == "" {
		system = DefaultSystem
	}
	return &Agent{
		client:    c,
		tools:     r,
		health:    newHealth(),
		approving: &sync.Mutex{},
		system:    system,
		messages:  []provider.Message{{Role: provider.System, Content: system}},
	}
}

// SetRef records which "provider/model" is in use.
func (a *Agent) SetRef(ref string) { a.ref = ref }

// Ref reports the active "provider/model".
func (a *Agent) Ref() string { return a.ref }

// Context reports the active model's declared window, zero when unknown.
func (a *Agent) Context() int { return a.contextTokens }

// SetAutoSwitch enables escalation up the fallback ladder.
func (a *Agent) SetAutoSwitch(on bool) { a.autoSwitch = on }

// SetMaxSteps bounds the tool-calling steps in a single turn. Zero — the
// default — leaves a turn unbounded, which is what long tasks need; set it only
// as a backstop against a model that loops instead of finishing.
func (a *Agent) SetMaxSteps(n int) {
	if n < 0 {
		n = 0
	}
	a.maxSteps = n
}

// SetContext tells the agent how large the model's context window is, so it can
// keep requests inside it. Zero disables trimming.
func (a *Agent) SetContext(tokens int) { a.contextTokens = tokens }

// overhead is what every request costs before any messages: the tool schemas,
// measured once.
func (a *Agent) overhead() int {
	if a.tools == nil {
		return 0
	}
	if a.schemaTokens == 0 {
		b, err := json.Marshal(a.tools.Schemas())
		if err != nil {
			return 0
		}
		a.schemaTokens = len(b) / 4
	}
	return a.schemaTokens
}

// estimateTokens approximates the size of a request. Roughly four characters
// per token is close enough to decide what to drop, and it avoids depending on
// a tokenizer that would have to match whatever model is configured.
func estimateTokens(msgs []provider.Message) int {
	chars := 0
	for _, m := range msgs {
		chars += len(m.Content) + len(m.Role) + len(m.Name)
		for _, tc := range m.ToolCalls {
			chars += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
		// Per-message framing the API adds on top of the text.
		chars += 16
	}
	return chars / 4
}

// trim drops the oldest material until the request fits, reporting how many
// messages went. Without this the server silently truncates from the front
// instead — taking the system prompt and the user's question with it, after
// which the model answers something else entirely.
//
// Two things are never dropped: the system prompt, and the question currently
// being answered. Everything else goes oldest first — earlier exchanges before
// the current one, and within the current one, the earliest tool results. The
// newest tool result always survives, since it is usually what the model is
// about to reason about.
//
// Messages go in whole groups: an assistant message carrying tool calls and the
// results answering it have to travel together, or the request is rejected for
// referencing a call that is no longer there.
func (a *Agent) trim(out chan<- Event) {
	if a.contextTokens <= 0 {
		return
	}
	budget := a.trimBudget()
	if estimateTokens(a.messages) <= budget {
		return
	}
	dropped := 0

	// Earlier exchanges first: everything between the system prompt and the
	// question being answered.
	for lastUser(a.messages) > 1 && estimateTokens(a.messages) > budget {
		a.messages = append(a.messages[:1], a.messages[2:]...)
		dropped++
	}

	// Still over: drop the oldest tool groups from the current exchange,
	// keeping the question and the most recent group.
	for estimateTokens(a.messages) > budget {
		i := lastUser(a.messages) + 1
		if i >= len(a.messages) {
			break
		}
		end := i + 1
		for end < len(a.messages) && a.messages[end].Role == provider.ToolRole {
			end++
		}
		if end >= len(a.messages) {
			// This is the newest group; keeping it is more useful than fitting.
			break
		}
		a.messages = append(a.messages[:i], a.messages[end:]...)
		dropped += end - i
	}

	if dropped > 0 {
		// Only ever a last resort: the ladder had nowhere left to go.
		out <- Trimmed{Messages: dropped}
	}
}

// endpointMessage digs the server's own words out of an error, since those are
// what a user can act on.
//
// The wrapped text carries the classification, the URL and the status before
// the body, and the body itself is JSON. Both layers are peeled off when they
// are there, and the whole string is returned when they are not.
func endpointMessage(err error) string {
	msg := err.Error()
	// The body is appended after the status, and it contains ": " itself — so it
	// is found by locating where the JSON starts rather than by splitting on the
	// last colon, which returned a fragment of the middle of it.
	if i := strings.Index(msg, "{"); i >= 0 {
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(msg[i:]), &body) == nil && body.Error.Message != "" {
			return strings.TrimSpace(body.Error.Message)
		}
		// Unparseable JSON is worse than the wrapped text, so fall back to
		// everything before it.
		return strings.TrimSpace(strings.TrimSuffix(msg[:i], ": "))
	}
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		return strings.TrimSpace(msg[i+2:])
	}
	return strings.TrimSpace(msg)
}

// sharedQuota reports whether a refusal is against an allowance the whole
// provider draws on, rather than this model's own throughput.
//
// It matters because a per-day free-tier cap is shared: when it is gone, every
// free model behind that provider will refuse too, and walking a ladder of them
// spends eight requests discovering it.
func sharedQuota(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "daily") ||
		strings.Contains(m, "per-day") ||
		strings.Contains(m, "per day") ||
		strings.Contains(m, "free_tier")
}

// lastUser is the index of the most recent user message, or zero if there is
// none. Everything from there on is the exchange in progress.
func lastUser(msgs []provider.Message) int {
	for i := len(msgs) - 1; i > 0; i-- {
		if msgs[i].Role == provider.User {
			return i
		}
	}
	return 0
}

// Mode reports the current mode.
func (a *Agent) Mode() Mode { return a.mode }

// SetMode changes what the agent may do without asking. The system prompt is
// rewritten to match, so the model knows the rules rather than discovering
// them through refusals.
func (a *Agent) SetMode(m Mode) {
	a.mode = m
	a.messages[0].Content = a.system + m.guidance()
}

// Model reports the model this agent talks to.
func (a *Agent) Model() string { return a.client.Model }

// SetClient swaps the underlying model mid-session, keeping the transcript.
func (a *Agent) SetClient(c *provider.Client) { a.client = c }

// Reset clears the transcript, keeping the system prompt.
func (a *Agent) Reset() { a.messages = a.messages[:1] }

// Note records something that happened outside the conversation but changes
// what is true inside it — the working directory moving to another branch, say.
//
// It goes in as a user message rather than a system one: the transcript already
// carries exactly one system prompt, and endpoints differ on what a second one
// means, whereas every one of them reads a user turn.
func (a *Agent) Note(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	a.messages = append(a.messages, provider.Message{Role: provider.User, Content: text})
}

// approve asks the frontend to allow a call, returning false if the turn is
// cancelled while waiting.
func (a *Agent) approve(ctx context.Context, tc provider.ToolCall, out chan<- Event) bool {
	// One question at a time. Concurrent sub-agents can each reach a mutating
	// tool at the same moment, and two prompts racing for one y/n would have
	// the answer land on whichever asked last — approving something the user
	// never saw. The lock is shared with the caller, so the whole tree queues.
	a.approving.Lock()
	defer a.approving.Unlock()
	// Nothing is worth approving once the turn is cancelled.
	if ctx.Err() != nil {
		return false
	}

	reply := make(chan bool, 1)
	out <- Approval{Name: tc.Function.Name, Args: tc.Function.Arguments, Reply: reply}
	select {
	case ok := <-reply:
		return ok
	case <-ctx.Done():
		return false
	}
}

// Messages exposes the transcript for persistence.
func (a *Agent) Messages() []provider.Message { return a.messages }

// Fork returns an agent that answers one turn alongside this one.
//
// A second question asked while the first is still running cannot go to the
// same agent: a turn appends to a.messages as it goes and blocks on tool
// results, so two of them on one agent would interleave their tool calls into
// each other's transcripts and race on the slice itself.
//
// The fork takes a snapshot of the conversation so far — it can see everything
// said up to the moment it was asked — and answers into its own copy. Merge
// folds what it produced back in when it is done.
//
// Shared by pointer, not copied: health, so what one turn learns about a dead
// endpoint spares the other from finding out again; and the approval lock, so
// two overlapping turns reaching a mutating tool ask the one keyboard one
// question at a time rather than both at once.
func (a *Agent) Fork() *Agent {
	f := &Agent{
		client:        a.client,
		tools:         a.tools,
		system:        a.system,
		mode:          a.mode,
		contextTokens: a.contextTokens,
		ref:           a.ref,
		fallbacks:     a.fallbacks,
		autoSwitch:    a.autoSwitch,
		maxSteps:      a.maxSteps,
		health:        a.health,
		approving:     a.approving,
		parent:        a,
		messages:      append([]provider.Message(nil), a.messages...),
	}
	// The task tool is the one tool bound to an agent rather than to the
	// working directory: it emits the sub-agent's events on the channel of the
	// agent it was built for. Inheriting the registry by pointer would leave the
	// fork delegating onto the conversation's channel, which is nil because the
	// conversation never runs — a send that blocks forever, and one no
	// cancellation can reach. So the fork gets its own, built against itself.
	if a.tools.Has("task") {
		f.tools = a.tools.Without("task")
		f.EnableSubagents()
	}
	return f
}

// Merge folds a finished fork's exchange back into the conversation it came
// from, so the transcript — and the session saved from it — ends up holding
// every turn even though they were answered side by side.
//
// The exchange is taken from the fork's last user message rather than from a
// remembered offset, because the fork may have trimmed or compacted its copy
// while it worked. The question being answered is the one thing neither of
// those ever drops, which makes it the reliable place to cut.
//
// Must not be called until the fork's run has finished; the caller knows that
// because the event channel has closed.
func (a *Agent) Merge() {
	if a.parent == nil {
		return
	}
	i := lastUser(a.messages)
	if i < 1 {
		return
	}
	a.parent.messages = append(a.parent.messages, a.messages[i:]...)
	a.parent = nil
}

// Restore replaces the transcript with a saved one, keeping the current system
// prompt rather than the one the session was recorded with — the mode and
// prompt may have changed since.
func (a *Agent) Restore(msgs []provider.Message) {
	// A session saved with an empty tool-call argument list would be rejected
	// by any endpoint that speaks Anthropic's schema, on this turn and every
	// turn after it. Repair it on the way in rather than leaving the session
	// unusable.
	provider.NormalizeToolArgs(msgs)
	a.messages = a.messages[:1]
	for _, m := range msgs {
		if m.Role == provider.System {
			continue
		}
		a.messages = append(a.messages, m)
	}
}

// Run executes one user turn, emitting events on out and closing it when done.
// Cancelling ctx aborts the turn; the partial transcript is retained so the
// conversation stays coherent.
func (a *Agent) Run(ctx context.Context, input string, out chan<- Event) {
	a.run(ctx, input, out)
}

// run executes a turn until the model stops requesting tools or is cancelled.
// There is deliberately no step limit by default: a turn ends when the task is
// finished, and if the model runs low on context it escalates to the next model
// on the fallback ladder (see escalate) instead of being cut off. SetMaxSteps
// can impose one as a backstop against a model that loops.
func (a *Agent) run(ctx context.Context, input string, out chan<- Event) {
	defer close(out)
	a.out = out

	a.messages = append(a.messages, provider.Message{Role: provider.User, Content: input})
	schemas := a.tools.Schemas()
	// Each turn starts at the bottom of the ladder: a short question does not
	// deserve the expensive model just because an earlier one needed it.
	a.rung = 0

	for step := 0; ; step++ {
		// Only when the user asked for a backstop. Unlimited is the default, so
		// a long task is never cut off for being long.
		if a.maxSteps > 0 && step >= a.maxSteps {
			out <- Failed{Err: fmt.Errorf("stopped after %d steps (max_steps); the task may be unfinished", a.maxSteps)}
			return
		}
		// Grow before shrinking. Dropping earlier tool results makes the model
		// forget what it already found and investigate the same thing again,
		// which is worse than the request being large — so every rung of the
		// ladder is tried before anything is thrown away.
		for a.wouldTrim() {
			reason := fmt.Sprintf("context full at %d tokens", a.contextTokens)
			if why, tight := a.needsMoreRoom(); tight {
				reason = why
			}
			if !a.escalate(forRoom, reason, out) {
				break
			}
		}
		// Only reached if the ladder ran out, and a no-op if the request now
		// fits on a roomier model. Summarising is tried before anything is
		// dropped: the room is the same either way, and what it costs the model
		// to remember is not.
		a.reduce(ctx, out)

		msg, usage, err := a.client.Stream(ctx, a.messages, schemas, provider.Handler{
			Text:      func(s string) { out <- TextDelta{Text: s} },
			Reasoning: func(s string) { out <- ReasoningDelta{Text: s} },
		})
		if usage.Total > 0 {
			out <- Usage{usage}
		}
		if err != nil {
			// Record what this failure means before deciding what to do, so the
			// next turn does not have to learn it again.
			switch {
			case errors.Is(err, provider.ErrRateLimited):
				why := endpointMessage(err)
				out <- Rejected{Ref: a.ref, Reason: why}
				reason := "rate limited"
				if sharedQuota(err) {
					// The allowance belongs to the provider, so its other models
					// are equally out. Rest the whole endpoint instead of
					// learning that eight more times.
					a.hp().Exhaust(providerOf(a.ref))
					out <- Tripped{Provider: providerOf(a.ref), For: quotaCooldown,
						Reason: "its allowance is used up"}
					reason = "provider's allowance is used up"
				} else {
					wait := a.hp().RateLimited(a.ref)
					reason = fmt.Sprintf("rate limited, resting %s", wait.Round(time.Second))
				}
				if a.escalate(forFailure, reason, out) {
					continue
				}

			case errors.Is(err, provider.ErrNeedsCredits):
				// Locked out rather than cooled down: waiting does not buy
				// credit, so retrying this model later in the session would
				// fail exactly the same way.
				why := endpointMessage(err)
				a.hp().LockOut(a.ref, why)
				out <- Rejected{Ref: a.ref, Reason: why}
				if a.escalate(forFailure, "needs credits", out) {
					continue
				}
			case errors.Is(err, provider.ErrModelInvalid):
				// Report what the endpoint said before moving on. It is
				// usually specific and actionable — "only available through
				// the Batch API" — and a paraphrase throws that away.
				why := endpointMessage(err)
				a.hp().LockOut(a.ref, why)
				out <- Rejected{Ref: a.ref, Reason: why}
				if a.escalate(forFailure, "previous model rejected", out) {
					continue
				}
			case errors.Is(err, provider.ErrUnavailable):
				// A 500 or a dropped connection is usually the endpoint having a
				// moment rather than anything wrong with the request, and
				// sending it again is far more likely to work than moving to a
				// different model. Only after that does the ladder come into it.
				if a.attempt < maxRetries {
					a.attempt++
					wait := retryDelay(a.attempt)
					out <- Retrying{Attempt: a.attempt, After: wait, Reason: endpointMessage(err)}
					select {
					case <-time.After(wait):
					case <-ctx.Done():
						out <- Failed{Err: ctx.Err()}
						return
					}
					continue
				}
				a.attempt = 0
				if a.hp().Unavailable(a.ref) {
					out <- Tripped{Provider: providerOf(a.ref), For: breakerCooldown,
						Reason: "repeated failures"}
				}
				if a.escalate(forFailure, "endpoint unavailable", out) {
					continue
				}
			}
			// Keep the partial assistant message out of the transcript on
			// failure; a half-written turn confuses the next request. Report
			// the endpoint's own words rather than the wrapped chain, which
			// ends in raw JSON.
			out <- Failed{Err: fmt.Errorf("%s", endpointMessage(err))}
			return
		}
		a.attempt = 0
		a.hp().Succeeded(a.ref)
		a.messages = append(a.messages, msg)

		if len(msg.ToolCalls) == 0 {
			// "length" means the model hit the context ceiling mid-generation.
			// With a reasoning model that usually happens during thinking, so
			// nothing is produced at all — which looks like an empty reply
			// rather than the window being too small.
			if msg.Finish == "length" && strings.TrimSpace(msg.Content) == "" {
				// Cut off before writing anything. Retrying the same request on
				// a roomier model is exactly the case escalation exists for, and
				// nothing was emitted, so the retry cannot produce a seam.
				if a.escalate(forRoom, fmt.Sprintf("cut off after %d tokens of prompt", usage.Prompt), out) {
					a.messages = a.messages[:len(a.messages)-1]
					continue
				}
				out <- Failed{Err: fmt.Errorf(
					"ran out of context before answering (%d tokens of prompt). "+
						"Use /clear, or give the model a larger context", usage.Prompt)}
				return
			}
			out <- TurnEnd{Text: msg.Content}
			return
		}

		results := a.dispatchAll(ctx, msg.ToolCalls, out)
		for i, tc := range msg.ToolCalls {
			a.messages = append(a.messages, provider.Message{
				Role:       provider.ToolRole,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				// Results are appended in the order the model asked for them,
				// whatever order they finished in: a tool result that does not
				// follow its call is rejected by the API.
				Content: results[i],
			})
		}
		if ctx.Err() != nil {
			out <- Failed{Err: ctx.Err()}
			return
		}
	}
}

// dispatchAll runs a batch of tool calls and returns their results in the order
// asked for. Delegated tasks run concurrently with each other: a sub-agent
// spends most of its time waiting on a model, so three of them against a hosted
// endpoint finish in roughly the time of the slowest rather than the sum.
//
// Everything else stays sequential. Ordinary tools touch the working directory
// — two edits to one file, or a build racing a write, is a worse failure than
// any saving is worth — and they return quickly enough that there is nothing to
// win. Tasks are the exception because they are pure investigation: a sub-agent
// reports back and changes nothing the caller can see.
func (a *Agent) dispatchAll(ctx context.Context, calls []provider.ToolCall, out chan<- Event) []string {
	results := make([]string, len(calls))

	// Run the delegated ones first, in the background, then work through the
	// rest here. By the time the sequential calls are done the tasks have had
	// that long to run for free.
	var wg sync.WaitGroup
	for i, tc := range calls {
		if tc.Function.Name != "task" {
			continue
		}
		wg.Add(1)
		go func(i int, tc provider.ToolCall) {
			defer wg.Done()
			results[i], _ = a.dispatch(ctx, tc, out)
		}(i, tc)
	}
	for i, tc := range calls {
		if tc.Function.Name == "task" {
			continue
		}
		// The error is reported to the user and handed to the model as text, so
		// it can correct itself rather than stall.
		results[i], _ = a.dispatch(ctx, tc, out)
		if ctx.Err() != nil {
			break
		}
	}
	wg.Wait()
	return results
}

func (a *Agent) dispatch(ctx context.Context, tc provider.ToolCall, out chan<- Event) (string, error) {
	name := tc.Function.Name
	args := tc.Function.Arguments
	if args == "" {
		args = "{}"
	}

	out <- ToolStart{Name: name, Args: args, Depth: a.depth}

	t, ok := a.tools.Get(name)
	if !ok {
		err := fmt.Errorf("no such tool: %s", name)
		out <- ToolEnd{Name: name, Err: err, Depth: a.depth}
		return err.Error(), err
	}

	// Smaller local models sometimes emit malformed JSON. Report it back as a
	// tool result so the model can retry with valid arguments.
	if !json.Valid([]byte(args)) {
		err := fmt.Errorf("arguments were not valid JSON")
		out <- ToolEnd{Name: name, Err: err, Depth: a.depth}
		return err.Error(), err
	}

	// Mode gating happens here rather than inside the tools, so every tool is
	// covered by the same rule and a new tool cannot forget to check.
	if !t.IsReadOnly(json.RawMessage(args)) {
		switch a.mode {
		case ModePlan:
			err := fmt.Errorf("refused: %s changes state and the agent is in plan mode", name)
			out <- ToolEnd{Name: name, Err: err, Depth: a.depth}
			// Returned as a tool result so the model adapts instead of stalling.
			return "refused: plan mode is read-only. Propose the change instead of making it.", err
		case ModeAccept:
			if !a.approve(ctx, tc, out) {
				err := fmt.Errorf("declined by user")
				out <- ToolEnd{Name: name, Err: err, Depth: a.depth}
				return "the user declined this change. Ask what to do differently.", err
			}
		}
	}

	result, err := t.Run(ctx, json.RawMessage(args))
	if err != nil {
		out <- ToolEnd{Name: name, Err: err, Depth: a.depth}
		return "error: " + err.Error(), err
	}
	out <- ToolEnd{Name: name, Result: result}
	return result, nil
}
