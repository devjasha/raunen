// Package agent implements the tool-use loop: prompt the model, run whatever
// tools it asks for, feed the results back, repeat until it stops asking.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
type ToolStart struct {
	Name string
	Args string
}

// ToolEnd carries the tool's result, or its error.
type ToolEnd struct {
	Name   string
	Result string
	Err    error
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
}

func New(c *provider.Client, r *tools.Registry, system string) *Agent {
	if system == "" {
		system = DefaultSystem
	}
	return &Agent{
		client:   c,
		tools:    r,
		system:   system,
		messages: []provider.Message{{Role: provider.System, Content: system}},
	}
}

// SetContext tells the agent how large the model's context window is, so it can
// keep requests inside it. Zero disables trimming.
func (a *Agent) SetContext(tokens int) { a.contextTokens = tokens }

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
	budget := a.contextTokens * 6 / 10
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
	defer close(out)

	a.messages = append(a.messages, provider.Message{Role: provider.User, Content: input})
	schemas := a.tools.Schemas()

	for step := 0; step < maxSteps; step++ {
		a.trim(out)

		msg, usage, err := a.client.Stream(ctx, a.messages, schemas, provider.Handler{
			Text:      func(s string) { out <- TextDelta{Text: s} },
			Reasoning: func(s string) { out <- ReasoningDelta{Text: s} },
		})
		if usage.Total > 0 {
			out <- Usage{usage}
		}
		if err != nil {
			// Keep the partial assistant message out of the transcript on
			// failure; a half-written turn confuses the next request.
			out <- Failed{Err: err}
			return
		}
		a.messages = append(a.messages, msg)

		if len(msg.ToolCalls) == 0 {
			// "length" means the model hit the context ceiling mid-generation.
			// With a reasoning model that usually happens during thinking, so
			// nothing is produced at all — which looks like an empty reply
			// rather than the window being too small.
			if msg.Finish == "length" && strings.TrimSpace(msg.Content) == "" {
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
	out <- Failed{Err: fmt.Errorf("gave up after %d steps", maxSteps)}
}

func (a *Agent) dispatch(ctx context.Context, tc provider.ToolCall, out chan<- Event) (string, error) {
	name := tc.Function.Name
	args := tc.Function.Arguments
	if args == "" {
		args = "{}"
	}

	out <- ToolStart{Name: name, Args: args}

	t, ok := a.tools.Get(name)
	if !ok {
		err := fmt.Errorf("no such tool: %s", name)
		out <- ToolEnd{Name: name, Err: err}
		return err.Error(), err
	}

	// Smaller local models sometimes emit malformed JSON. Report it back as a
	// tool result so the model can retry with valid arguments.
	if !json.Valid([]byte(args)) {
		err := fmt.Errorf("arguments were not valid JSON")
		out <- ToolEnd{Name: name, Err: err}
		return err.Error(), err
	}

	// Mode gating happens here rather than inside the tools, so every tool is
	// covered by the same rule and a new tool cannot forget to check.
	if !t.IsReadOnly(json.RawMessage(args)) {
		switch a.mode {
		case ModePlan:
			err := fmt.Errorf("refused: %s changes state and the agent is in plan mode", name)
			out <- ToolEnd{Name: name, Err: err}
			// Returned as a tool result so the model adapts instead of stalling.
			return "refused: plan mode is read-only. Propose the change instead of making it.", err
		case ModeAccept:
			if !a.approve(ctx, tc, out) {
				err := fmt.Errorf("declined by user")
				out <- ToolEnd{Name: name, Err: err}
				return "the user declined this change. Ask what to do differently.", err
			}
		}
	}

	result, err := t.Run(ctx, json.RawMessage(args))
	if err != nil {
		out <- ToolEnd{Name: name, Err: err}
		return "error: " + err.Error(), err
	}
	out <- ToolEnd{Name: name, Result: result}
	return result, nil
}
