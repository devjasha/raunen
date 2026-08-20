package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"raunen/internal/agent"
	"raunen/internal/session"
)

// A one-shot run is the scripted face of the agent, and a script needs two
// things the interactive UI does not: an exit status that means something, and
// output it can parse without guessing.
//
// Text mode stays exactly as it was — the answer on stdout, everything else on
// stderr — because that is what makes `raunen 'question' | pbcopy` work. JSON
// mode is for the other case, where a program wants to know which tools ran and
// whether the model actually finished.

// interrupted is the exit status for a run stopped by a signal. 128+SIGINT is
// the shell convention, and a script that distinguishes "the user pressed
// ctrl+c" from "the model failed" needs it to be that rather than 1.
const interrupted = 130

// runResult is the JSON document a one-shot run prints with --json.
//
// The field names are fixed: something parses this. Anything added later must
// be additive, since a consumer reading .output should not break because a new
// key appeared beside it.
type runResult struct {
	// Output is the assistant's final answer, as markdown.
	Output string `json:"output"`
	// Error is why the run failed, absent when it did not.
	Error string `json:"error,omitempty"`
	// ExitCode mirrors the process's status, so a consumer that already has the
	// document does not need to have kept the status separately.
	ExitCode int `json:"exit_code"`
	// Model is the "provider/model" that answered. It can differ from the one
	// asked for: escalation moves up the ladder when the context fills.
	Model string `json:"model"`
	// SessionID identifies the saved conversation, empty when --no-save.
	SessionID string `json:"session_id"`
	// Steps counts the tool-calling rounds the turn took.
	Steps int `json:"steps"`
	// ToolCalls lists what ran, in order.
	ToolCalls []toolCall `json:"tool_calls"`
	// Usage is the token accounting for the whole turn.
	Usage usage `json:"usage"`
}

// toolCall is one tool the model ran.
type toolCall struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Error is the failure, absent on success. The message itself, not a
	// classification: it is usually specific enough to act on.
	Error string `json:"error,omitempty"`
}

// usage totals the tokens the turn cost. Summed across requests rather than
// taken from the last one, because escalation and tool loops mean a turn is
// many requests and only the total is the bill.
type usage struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
	Total      int `json:"total"`
}

// oneShotOpts are the flags that only apply to a scripted run.
type oneShotOpts struct {
	// json switches stdout from prose to a single result document.
	json bool
	// save writes the conversation so --continue can pick it up.
	save bool
}

// oneShot runs a single turn and exits.
//
// The session is saved on the way out, which it was not before: a one-shot turn
// and an interactive one produce the same kind of conversation, and `raunen
// 'question'` followed by `raunen --continue` losing the question was a bug
// rather than a policy.
func oneShot(ag *agent.Agent, sess *session.Session, prompt string, opts oneShotOpts) error {
	// A run stopped by ctrl+c should end the turn rather than leave a half
	// written answer and a stranded model request. The context is cancelled and
	// the loop unwinds through the same path a failure takes.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	events := make(chan agent.Event, 64)
	go ag.Run(ctx, prompt, events)

	// Empty rather than nil, so the field marshals as [] and a consumer can
	// iterate it without checking for null first.
	res := runResult{Model: ag.Ref(), ToolCalls: []toolCall{}}
	if opts.save {
		res.SessionID = sess.ID
	}

	// In JSON mode nothing may reach stdout until the document does, so the
	// answer is collected rather than streamed. In text mode it streams, which
	// is what makes the output feel live in a pipe.
	var answer strings.Builder
	var failure error

	for ev := range events {
		switch e := ev.(type) {
		case agent.TextDelta:
			if opts.json {
				answer.WriteString(e.Text)
			} else {
				fmt.Print(e.Text)
			}
		case agent.ReasoningDelta:
			// Thinking goes to stderr so stdout stays clean for pipes.
			fmt.Fprint(os.Stderr, dim(e.Text))
		case agent.Usage:
			res.Usage.Prompt += e.Prompt
			res.Usage.Completion += e.Completion
			res.Usage.Total += e.Total
			// Token accounting is the first thing worth seeing when replies go
			// wrong, so it is available on stderr behind RAUNEN_DEBUG.
			if debug {
				fmt.Fprintf(os.Stderr, dim("\n[usage] prompt=%d completion=%d total=%d\n"),
					e.Prompt, e.Completion, e.Total)
			}
		case agent.Switched:
			// The model that answers is not always the one asked for, and a
			// result reporting the model it was launched with would be a lie.
			res.Model = e.To
			fmt.Fprintf(os.Stderr, dim("[switched to %s — %s]\n"), e.To, e.Reason)
		case agent.Trimmed:
			fmt.Fprintf(os.Stderr, dim("[dropped %d earlier messages to fit the context]\n"), e.Messages)
		case agent.ToolStart:
			res.Steps++
			res.ToolCalls = append(res.ToolCalls, toolCall{Name: e.Name, Status: "running"})
			fmt.Fprintf(os.Stderr, "⏺ %s\n", e.Name)
		case agent.ToolEnd:
			markDone(res.ToolCalls, e)
			if e.Err != nil {
				fmt.Fprintf(os.Stderr, "  ↳ %v\n", e.Err)
			}
		case agent.TurnEnd:
			// TurnEnd carries the whole reply, which is what a turn that
			// produced no deltas — an answer assembled from a tool result, say
			// — still has to report.
			if opts.json && answer.Len() == 0 {
				answer.WriteString(e.Text)
			}
			if !opts.json {
				fmt.Println()
			}
		case agent.Failed:
			failure = e.Err
		}
	}

	res.Output = strings.TrimSpace(answer.String())

	// Saved before anything is reported, so a failed turn is still resumable:
	// what the model did before it ran out of context is usually most of the
	// work, and throwing it away because the last request failed would be the
	// expensive kind of tidy.
	if opts.save {
		sess.Messages = ag.Messages()
		if err := sess.Save(); err != nil {
			fmt.Fprintln(os.Stderr, "raunen: could not save session:", err)
		}
	}

	switch {
	case failure != nil && errors.Is(ctx.Err(), context.Canceled):
		// Cancelled by a signal. The turn did not fail on its own terms, and a
		// script needs to tell the two apart.
		res.ExitCode = interrupted
		res.Error = "interrupted"
	case failure != nil:
		res.ExitCode = 1
		res.Error = failure.Error()
	}

	if opts.json {
		return writeResult(res)
	}
	if failure != nil {
		fmt.Println()
		return exitError{code: res.ExitCode, err: failure}
	}
	return nil
}

// markDone records a tool's outcome against its most recent running entry.
//
// Matching on the last running call of that name rather than on position:
// sub-agents run concurrently, so their ToolEnds interleave, and pairing by
// index would attribute one task's failure to another's call.
func markDone(calls []toolCall, e agent.ToolEnd) {
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Name != e.Name || calls[i].Status != "running" {
			continue
		}
		if e.Err != nil {
			calls[i].Status = "error"
			calls[i].Error = e.Err.Error()
		} else {
			calls[i].Status = "success"
		}
		return
	}
}

// writeResult prints the document and returns the status the process should
// exit with.
//
// The document is printed even when the run failed. A consumer parsing stdout
// should not have to handle "sometimes there is JSON and sometimes there is
// not" — the error is a field, which is the whole reason for the mode.
func writeResult(res runResult) error {
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	if res.ExitCode != 0 {
		return exitError{code: res.ExitCode, quiet: true,
			err: fmt.Errorf("%s", res.Error)}
	}
	return nil
}

// exitError carries a status code out of a run, and optionally asks that the
// message not be printed again — in JSON mode it is already in the document,
// and repeating it on stderr would be noise.
type exitError struct {
	code  int
	quiet bool
	err   error
}

func (e exitError) Error() string { return e.err.Error() }
func (e exitError) Unwrap() error { return e.err }
