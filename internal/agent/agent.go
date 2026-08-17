// Package agent implements the tool-use loop: prompt the model, run whatever
// tools it asks for, feed the results back, repeat until it stops asking.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
type ToolStart struct {
	Name  string
	Args  string
	Depth int
}

// ToolEnd carries the tool's result, or its error.
type ToolEnd struct {
	Name   string
	Result string
	Err    error
	Depth  int
}

// TaskStart fires when the model delegates to a sub-agent.
type TaskStart struct{ Description string }

// TaskEnd carries what the sub-agent reported back.
type TaskEnd struct {
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

// Tripped reports that an endpoint has been taken out of rotation after
// repeated failures.
type Tripped struct {
	Provider string
	For      time.Duration
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
func (TaskStart) event()      {}
func (TaskEnd) event()        {}
func (TurnEnd) event()        {}
func (Failed) event()         {}

const DefaultSystem = `You are a terminal coding assistant. You have tools to read, write and edit
files and to run shell commands. Work in small concrete steps: inspect before
you change, and verify after. Prefer running a command to guessing at its
output. Keep replies short — the user is reading them in a terminal.`

// maxSteps bounds a single turn so a confused model cannot loop forever.
const maxSteps = 40

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
	// rateLimited records that the last move was forced by a refusal rather
	// than by running out of room, which changes what counts as an upgrade.
	rateLimited bool
	// health remembers what has recently failed, so the same dead end is not
	// walked into every turn. Shared with sub-agents by pointer.
	health *health
	// schemaTokens is what the tool definitions cost on every request. They are
	// not part of the message list but they are very much part of the prompt:
	// leaving them out understates a small model's usage by hundreds of tokens.
	schemaTokens int
	// out is the current turn's event channel. A turn is single-threaded, so
	// tools that report progress of their own — the sub-agent tool — can reach
	// the frontend through it.
	out chan<- Event
	// depth is zero for the main conversation and one inside a sub-agent.
	depth int
}

func New(c *provider.Client, r *tools.Registry, system string) *Agent {
	if system == "" {
		system = DefaultSystem
	}
	return &Agent{
		client:   c,
		tools:    r,
		health:   newHealth(),
		system:   system,
		messages: []provider.Message{{Role: provider.System, Content: system}},
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
	// Leave room for the reply as well as the prompt.
	budget := a.contextTokens*6/10 - a.overhead()
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
		out <- Trimmed{Messages: dropped}
	}
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

// approve asks the frontend to allow a call, returning false if the turn is
// cancelled while waiting.
func (a *Agent) approve(ctx context.Context, tc provider.ToolCall, out chan<- Event) bool {
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

// Restore replaces the transcript with a saved one, keeping the current system
// prompt rather than the one the session was recorded with — the mode and
// prompt may have changed since.
func (a *Agent) Restore(msgs []provider.Message) {
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
	a.run(ctx, input, out, maxSteps)
}

// run is Run with an explicit step budget, so a sub-agent can be held to a
// tighter one than the conversation that spawned it.
func (a *Agent) run(ctx context.Context, input string, out chan<- Event, steps int) {
	defer close(out)
	a.out = out

	a.messages = append(a.messages, provider.Message{Role: provider.User, Content: input})
	schemas := a.tools.Schemas()
	// Each turn starts at the bottom of the ladder: a short question does not
	// deserve the expensive model just because an earlier one needed it.
	a.rung = 0

	for step := 0; step < steps; step++ {
		a.trim(out)

		// Move up the ladder before sending, when what cannot be trimmed away
		// already fills most of the window.
		if reason, tight := a.needsMoreRoom(); tight {
			a.escalate(reason, out)
		}

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
				wait := a.hp().RateLimited(a.ref)
				if a.escalate(fmt.Sprintf("rate limited, resting %s", wait.Round(time.Second)), out) {
					continue
				}
			case errors.Is(err, provider.ErrModelInvalid):
				a.hp().LockOut(a.ref, "the endpoint rejected it")
				if a.escalate("model rejected by the endpoint", out) {
					continue
				}
			case errors.Is(err, provider.ErrUnavailable):
				if a.hp().Unavailable(a.ref) {
					out <- Tripped{Provider: providerOf(a.ref), For: breakerCooldown}
				}
				if a.escalate("endpoint unavailable", out) {
					continue
				}
			}
			// Keep the partial assistant message out of the transcript on
			// failure; a half-written turn confuses the next request.
			out <- Failed{Err: err}
			return
		}
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
				if a.escalate(fmt.Sprintf("cut off after %d tokens of prompt", usage.Prompt), out) {
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

		for _, tc := range msg.ToolCalls {
			result, err := a.dispatch(ctx, tc, out)
			a.messages = append(a.messages, provider.Message{
				Role:       provider.ToolRole,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result,
			})
			// The error is reported to the user and handed to the model as
			// text, so it can correct itself rather than stall.
			_ = err

			if ctx.Err() != nil {
				out <- Failed{Err: ctx.Err()}
				return
			}
		}
	}
	out <- Failed{Err: fmt.Errorf("gave up after %d steps", steps)}
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
