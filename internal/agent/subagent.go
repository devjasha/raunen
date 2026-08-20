package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"raunen/internal/provider"
	"raunen/internal/tools"
)

// Sub-agents exist for context first and speed second.
//
// Siblings delegated in the same turn run concurrently: a sub-agent spends
// almost all of its time waiting on a model, so three against a hosted endpoint
// finish in roughly the time of the slowest rather than the sum. Against a
// single local model there is nothing to win — one GPU serves one request at a
// time, and two concurrent requests to the same Ollama measured slower than two
// sequential ones — but nothing to lose either: they queue at the server
// instead of here.
//
// What they buy first is room. A sub-agent gets its own empty window, spends it
// reading whatever it needs, and hands back a short answer. The main
// conversation pays for the answer instead of for everything that produced it,
// which is the difference between finishing a question and running out of
// context halfway through it.
const subSystem = `You are a sub-agent handling one focused task for another agent.

Investigate with the tools you have and reply with the answer itself: the
findings, the file and line, the explanation. Your reply is all that is passed
back — the caller cannot see anything else you did, so do not refer to "the
above" or promise follow-up. Be brief and concrete. You cannot delegate.`

// taskSeq names sub-agents. Siblings are started from separate goroutines, so
// it is bumped atomically.
var taskSeq uint64

// EnableSubagents adds the task tool, letting the model delegate. The tool is
// built here rather than in the tools package because it needs to construct an
// agent, and tools cannot import agent without a cycle.
func (a *Agent) EnableSubagents() {
	a.tools.Add(tools.Tool{
		Name: "task",
		Description: "Delegate a self-contained piece of investigation to a sub-agent " +
			"with its own fresh context. It returns only its final answer, so use this " +
			"for work whose intermediate output you do not need — searching a codebase, " +
			"reading several files to answer one question. Give it everything it needs; " +
			"it cannot see this conversation.",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description": map[string]any{
					"type":        "string",
					"description": "A few words naming the task, for the user to see.",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "The full instruction for the sub-agent, self-contained.",
				},
			},
			"required": []string{"description", "prompt"},
		},
		// Delegation itself changes nothing; whatever the child does is gated
		// by the same rules, because it inherits the mode.
		Mutates: false,
		Run:     a.runTask,
	})
	// The tool changes the schemas, so the cached size is no longer right.
	a.schemaTokens = 0
}

func (a *Agent) runTask(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}
	if args.Description == "" {
		args.Description = "sub-task"
	}

	// Names this child for the life of the task, so a frontend can tell several
	// running at once apart. The counter is atomic because sibling tasks are
	// started from separate goroutines.
	id := fmt.Sprintf("t%d", atomic.AddUint64(&taskSeq, 1))

	a.out <- TaskStart{ID: id, Description: args.Description}

	child := &Agent{
		client: a.client,
		// Without the task tool, so a sub-agent cannot spawn its own.
		tools: a.tools.Without("task"),
		// Its own instructions, but the same project. A sub-agent edits the same
		// working directory as its caller, so a convention about how this
		// repository is built applies to it just as much — and it has no other
		// way to learn one, since it cannot see the conversation.
		system:        subSystem,
		project:       a.project,
		mode:          a.mode,
		contextTokens: a.contextTokens,
		// Shared, not copied: what the child learns about a failing endpoint
		// should spare the caller from finding out again.
		health:     a.health,
		fallbacks:  a.fallbacks,
		autoSwitch: a.autoSwitch,
		// The same backstop applies to the child: a looping sub-agent is the
		// case it was added for.
		maxSteps: a.maxSteps,
		ref:      a.ref,
		depth:    a.depth + 1,
	}
	child.messages = []provider.Message{{Role: provider.System, Content: child.prompt()}}

	events := make(chan Event, 64)
	go child.run(ctx, args.Prompt, events)

	// Forward the child's events to the frontend as they arrive, and keep its
	// closing text. Approvals travel through untouched, reply channel and all,
	// so a sub-agent's edits are approved in the same place as any other.
	var summary string
	var steps int
	var failed error
	for ev := range events {
		switch e := ev.(type) {
		case ToolStart:
			steps++
			// Stamped on the way through, so the frontend can route it to the
			// right panel without the child knowing it has siblings.
			e.Task = id
			a.out <- e
		case ToolEnd:
			e.Task = id
			a.out <- e
		case TurnEnd:
			summary = e.Text
		case Failed:
			failed = e.Err
		case TextDelta, ReasoningDelta:
			// The child's thinking and prose belong to the child. Only its
			// conclusion is passed on, which is the entire point.
		default:
			a.out <- ev
		}
	}

	summary = strings.TrimSpace(summary)
	if failed != nil && summary == "" {
		a.out <- TaskEnd{ID: id, Description: args.Description, Steps: steps, Err: failed}
		return "the sub-agent failed: " + failed.Error(), nil
	}
	if summary == "" {
		summary = "(the sub-agent returned nothing)"
	}

	a.out <- TaskEnd{ID: id, Description: args.Description, Summary: summary, Steps: steps}
	return summary, nil
}
