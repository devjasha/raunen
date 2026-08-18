package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"raunen/internal/provider"
)

// Compaction is what trimming should have been.
//
// Trimming drops the oldest messages whole, which is cheap and loses everything
// in them: the model forgets the file it already read, the approach it already
// ruled out, and the bug it already found, then spends the room it just gained
// discovering them again. It is the right answer only when there is no time to
// do better.
//
// Compacting spends one model call to write down what those messages
// established — paths, symbols, decisions, what is left to do — and puts the
// summary in their place. The conversation gets shorter without getting
// stupider, which is the only reason to shorten it at all.
//
// The recent tail is never summarised. Whatever is being worked on right now is
// worth more in full than in précis, and a summary of the last two minutes is
// the one thing the model could have written for itself.

// compactSystem instructs the summarising pass. It is deliberately not the
// agent's own system prompt: this call has no tools, no mode and nothing to do
// but write, and telling it that it is a coding assistant invites it to try.
const compactSystem = `You are summarising a coding session so that it can carry on in a smaller context window.

Write for the assistant that will continue the work, not for a reader looking back. Your summary replaces the conversation it describes: whatever you leave out is gone.

Use these headings, and drop any that has nothing under it:

Goal — what the user asked for, in their words, including anything they corrected, refused or ruled out.
Found — what was established about the codebase: file paths, symbol names, line numbers, how the pieces fit. A path is worth more than a sentence describing one.
Changed — every file written or edited, and what the edit was.
Decided — choices made and the reasons for them, so they are not reopened.
Open — what is unfinished, what failed, what to do next.

Keep names, paths, commands and error text exactly as they appear; they cannot be recovered once the messages are gone. Prefer a specific detail to a general statement. Do not introduce the summary, do not address the user, and do not offer to help.`

// compactAsk closes the material being summarised. It comes after the
// transcript rather than before it, because the last thing in the prompt is the
// thing a small model is most likely to actually do.
const compactAsk = `Summarise the session above, under the headings you were given.`

// summaryFrame labels the summary in the conversation it rejoins, so the model
// treats it as a record of what happened rather than as something the user just
// said. The message goes in as a user turn because that is the one role every
// OpenAI-compatible endpoint accepts anywhere in a conversation — an assistant
// message would be words the assistant never said, and a second system prompt
// is rejected outright by several of them.
const summaryFrame = "[The earlier part of this conversation was compacted to save context. " +
	"This is the record of it — treat it as your own memory of what happened.]\n\n"

// minCompact is how many older messages make a compaction worth a model call.
// Below this the summary would cost more room than the messages it replaces.
const minCompact = 4

// toolResultCap bounds one tool result inside the material being summarised. A
// single file read can be larger than the window we are trying to fit into, and
// what matters for a summary is that the file was read and what was in it, not
// the last four hundred lines of it.
const toolResultCap = 2000

// ErrNothingToCompact reports that there was nothing worth summarising. It is a
// normal outcome rather than a failure: a frontend should say so and carry on.
var ErrNothingToCompact = errors.New("nothing to compact")

// Compacted reports that older messages were replaced by a summary of them.
type Compacted struct {
	// Replaced is how many messages the summary stands in for; Kept is how many
	// were left verbatim after it.
	Replaced, Kept int
	// Before and After are token estimates of the whole conversation, which is
	// the number the user actually cares about.
	Before, After int
	Summary       string
	// Auto is set when the loop asked for this to fit a request, rather than
	// the user asking for it.
	Auto bool
}

func (Compacted) event() {}

// Compact replaces everything before the recent tail with a written summary of
// it, and reports what moved. focus is an optional instruction from the user
// about what to preserve; it is empty for an automatic compaction.
//
// The conversation is left untouched if anything goes wrong, so a failed
// compaction costs a model call and nothing else.
func (a *Agent) Compact(ctx context.Context, focus string) (Compacted, error) {
	return a.compact(ctx, focus, false)
}

// compact does the work for both callers. auto distinguishes the loop clawing
// back just enough room to send a request from the user asking for room on
// purpose, which is the difference between how much of the tail is kept.
func (a *Agent) compact(ctx context.Context, focus string, auto bool) (Compacted, error) {
	cut := a.compactPoint(auto)
	older := a.messages[1:cut]
	if len(older) < minCompact {
		return Compacted{}, fmt.Errorf("%w — the conversation is still short", ErrNothingToCompact)
	}
	before := estimateTokens(a.messages)

	ask := compactAsk
	if focus = strings.TrimSpace(focus); focus != "" {
		// The user's instruction goes last and is named as theirs, so a model
		// that follows only the final line still follows the right one.
		ask += "\n\nThe user asked you to pay particular attention to: " + focus
	}
	req := []provider.Message{
		{Role: provider.System, Content: compactSystem},
		{Role: provider.User, Content: a.transcriptOf(older) + "\n\n" + ask},
	}

	// No tools are offered. The summariser has nothing to do with them, and an
	// endpoint that sees tool schemas will sooner or later answer with a call
	// instead of with prose.
	msg, _, err := a.client.Stream(ctx, req, nil, provider.Handler{})
	if err != nil {
		return Compacted{}, fmt.Errorf("could not summarise the conversation: %w", err)
	}
	summary := strings.TrimSpace(msg.Content)
	if summary == "" {
		return Compacted{}, errors.New("the model returned an empty summary, so nothing was compacted")
	}

	// A summary that costs more than what it replaces is not a compaction. It
	// happens whenever the older material is already short — a model asked to
	// summarise five lines writes six — and applying it would make the next
	// request larger rather than smaller, which is the opposite of the point.
	note := provider.Message{Role: provider.User, Content: summaryFrame + summary}
	if estimateTokens([]provider.Message{note}) >= estimateTokens(older) {
		return Compacted{}, fmt.Errorf(
			"%w — the summary came out no smaller than the %d messages it would replace",
			ErrNothingToCompact, len(older))
	}

	// Copied before the transcript is rebuilt, since the rebuild writes over
	// the very entries the tail is sliced from.
	kept := append([]provider.Message(nil), a.messages[cut:]...)
	a.messages = append(append(a.messages[:1], note), kept...)

	return Compacted{
		Replaced: len(older),
		Kept:     len(kept),
		Before:   before,
		After:    estimateTokens(a.messages),
		Summary:  summary,
		Auto:     auto,
	}, nil
}

// RunCompact performs a compaction as a turn of its own, emitting events on out
// and closing it when done — so a frontend can drive it with the machinery it
// already has for a conversation turn, including cancellation.
func (a *Agent) RunCompact(ctx context.Context, focus string, out chan<- Event) {
	defer close(out)
	c, err := a.Compact(ctx, focus)
	if err != nil {
		// CompactFailed rather than Failed: nothing was being answered, so
		// nothing failed to be answered. The conversation is exactly as it was
		// and the next question will work.
		out <- CompactFailed{Err: err}
		return
	}
	out <- c
}

// compactPoint is where the tail kept verbatim begins.
//
// It always lands on a user message. Cutting anywhere else would leave a tool
// result at the front of the conversation referring to a call that is no longer
// in it, which every endpoint rejects — the same invariant trim keeps, for the
// same reason.
func (a *Agent) compactPoint(auto bool) int {
	cut := lastUser(a.messages)
	if cut < 1 {
		// No exchange to speak of: there is nothing here worth summarising.
		return len(a.messages)
	}
	// The last exchange is kept whatever it costs; earlier ones join it while
	// there is room. Walking back rather than forward is what keeps the most
	// recent work in full when the tail alone is already large.
	keep := a.keepBudget(auto)
	for i := cut - 1; i > 1; i-- {
		if a.messages[i].Role != provider.User {
			continue
		}
		if estimateTokens(a.messages[i:]) > keep {
			break
		}
		cut = i
	}
	return cut
}

// keepBudget is how much of the window the verbatim tail may occupy. The rest
// is for the summary, the system prompt and the reply.
//
// The two callers want different answers. An automatic compaction only has to
// claw back enough room to send the request in front of it, so it keeps three
// tenths — keep less and the model loses the thread of what it was doing a
// minute ago, which is what compaction exists to prevent. Someone typing
// /compact is asking for room on purpose, usually before starting something
// large, and a compaction that hands back a tenth of the window is not worth
// the wait: by hand the tail is a tenth, which is the last exchange or two.
func (a *Agent) keepBudget(auto bool) int {
	share := 1
	if auto {
		share = 3
	}
	if b := a.trimBudget(); b > 0 {
		return b * share / 10
	}
	// The window was never declared. Compaction is still worth doing — the
	// user asked for it, or the endpoint is about to truncate silently — so
	// fall back to a fixed slice rather than refusing.
	return 1500 * share
}

// summaryBudget bounds the material sent to the summariser. It has to fit in
// the same window everything else does.
func (a *Agent) summaryBudget() int {
	if b := a.trimBudget(); b > 0 {
		return b
	}
	return 16000
}

// transcriptOf renders messages as plain text for the summariser.
//
// Flattened rather than replayed as a message list, for two reasons. A request
// carrying tool calls without the schemas that declare them is rejected by some
// endpoints and quietly mangled by others. And as text it can be bounded: a
// tool result is truncated to its useful head, and the oldest material is
// dropped if the whole thing still will not fit — losing the beginning of a
// summary is better than failing to write one.
func (a *Agent) transcriptOf(msgs []provider.Message) string {
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case provider.User:
			lines = append(lines, "USER: "+m.Content)
		case provider.Assistant:
			if c := strings.TrimSpace(m.Content); c != "" {
				lines = append(lines, "ASSISTANT: "+c)
			}
			for _, tc := range m.ToolCalls {
				lines = append(lines, fmt.Sprintf("ASSISTANT CALLED %s(%s)",
					tc.Function.Name, clip(tc.Function.Arguments, 400)))
			}
		case provider.ToolRole:
			lines = append(lines, "RESULT: "+clip(m.Content, toolResultCap))
		}
	}

	// Trimmed from the front, which is the oldest material and the part the
	// conversation has already moved furthest away from.
	budget := a.summaryBudget()
	for len(lines) > 1 && estimateTokens([]provider.Message{
		{Role: provider.User, Content: strings.Join(lines, "\n\n")},
	}) > budget {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n\n")
}

// clip shortens a string to n characters, saying how much went. The count
// matters: "and 40 more lines" tells the summariser the file was long, which is
// itself worth recording.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	rest := len(s) - n
	return s[:n] + fmt.Sprintf("\n… (%d more characters, dropped when summarising)", rest)
}

// reduce brings the request inside the window: summarise what can be
// summarised, and only throw away what is left over.
//
// Order matters. Compaction costs a model call and keeps the knowledge;
// trimming is free and loses it. Trying the cheap one first would mean paying
// to summarise messages that had already been deleted.
func (a *Agent) reduce(ctx context.Context, out chan<- Event) {
	if a.shouldCompact() {
		c, err := a.compact(ctx, "", true)
		switch {
		case err == nil:
			out <- c
		case errors.Is(err, ErrNothingToCompact):
			// Nothing worth summarising; trimming below is the only option.
		case ctx.Err() != nil:
			// The turn was cancelled. Say nothing; the cancellation is already
			// on its way to the frontend.
		default:
			out <- CompactFailed{Err: err}
		}
	}
	a.trim(out)
}

// CompactFailed reports that an automatic compaction did not happen, so the
// conversation is about to be trimmed instead. It is not a failed turn — the
// turn carries on — which is why it is not a Failed.
type CompactFailed struct{ Err error }

func (CompactFailed) event() {}

// shouldCompact decides whether the loop should summarise before it trims.
func (a *Agent) shouldCompact() bool {
	if !a.wouldTrim() {
		return false
	}
	// Sub-agents are not compacted. One exists to spend a fresh window and hand
	// back a sentence; it is bounded to a few steps, and a model call to
	// summarise a context that is about to be thrown away entirely would cost
	// more than the memory is worth.
	if a.depth > 0 {
		return false
	}
	cut := a.compactPoint(true)
	if cut-1 < minCompact {
		return false
	}
	// Worth the call only if the material is actually large. A handful of short
	// messages summarises to about its own size, and the request would still
	// not fit afterwards.
	return estimateTokens(a.messages[1:cut]) > a.trimBudget()/4
}
