package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"raunen/internal/provider"
	"raunen/internal/tools"
)

// Sub-agents exist for context, not for speed. A local model runs one request
// at a time — two concurrent requests to the same Ollama measured slower than
// two sequential ones — so nothing here is parallel.
//
// What they buy is room. A sub-agent gets its own empty window, spends it
// reading whatever it needs, and hands back a short answer. The main
// conversation pays for the answer instead of for everything that produced it,
// which is the difference between finishing a question and running out of
// context halfway through it.
const subSystem = `You are a sub-agent handling one focused task for another agent.

Investigate with the tools you have and reply with the answer itself: the
findings, the file and line, the explanation. Your reply is all that is passed
back — the caller cannot see anything else you did, so do not refer to "the
above" or promise follow-up. Be brief and concrete. You cannot delegate.`

// subSteps bounds a sub-agent more tightly than a main turn. A child that
// wanders is worse than one that gives up: the caller is waiting, and every
// step it takes is spent from the same wall-clock budget.
const subSteps = 16

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

	a.out <- TaskStart{Description: args.Description}

	child := &Agent{
		client: a.client,
		// Without the task tool, so a sub-agent cannot spawn its own.
		tools:         a.tools.Without("task"),
		system:        subSystem,
		mode:          a.mode,
		contextTokens: a.contextTokens,
		ref:           a.ref,
		depth:         a.depth + 1,
		messages:      []provider.Message{{Role: provider.System, Content: subSystem + a.mode.guidance()}},
	}

	events := make(chan Event, 64)
	go child.run(ctx, args.Prompt, events, subSteps)

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
		a.out <- TaskEnd{Description: args.Description, Steps: steps, Err: failed}
		return "the sub-agent failed: " + failed.Error(), nil
	}
	if summary == "" {
		summary = "(the sub-agent returned nothing)"
	}

	a.out <- TaskEnd{Description: args.Description, Summary: summary, Steps: steps}
	return summary, nil
}
