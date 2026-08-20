package acp

import (
	"encoding/json"
	"fmt"
	"strings"

	"raunen/internal/agent"
)

// Translating raunen's events into ACP's updates.
//
// raunen emits a richer stream than ACP models. Escalating to a roomier model,
// trimming old exchanges, resting a rate-limited endpoint, a sub-agent
// reporting back — none of these have an ACP update, because ACP describes what
// an agent is doing to your code rather than how it is managing a context
// window.
//
// The mapping is therefore lossy on purpose, and it is all in this one file so
// what is lost is countable rather than scattered. Events with no analogue
// become agent thought chunks, which is where an editor already puts things
// that are commentary rather than answer. Dropping them silently would leave a
// user watching a spinner with no idea their model had just been rate limited.

// toolKind classifies a tool for the editor, which uses it to pick an icon and
// to decide how loudly to announce a call. The names are ACP's.
func toolKind(name string) string {
	switch name {
	case "read", "list":
		return "read"
	case "write", "edit":
		return "edit"
	case "grep", "glob":
		return "search"
	case "bash":
		return "execute"
	case "task":
		return "think"
	}
	// MCP tools arrive named after their server and could be anything.
	return "other"
}

// toolTitle is the one line an editor shows for a call. It leads with what is
// being acted on, since "read" alone says nothing while "read main.go" says
// everything.
func toolTitle(name, args string) string {
	if t := targetOf(args); t != "" {
		return name + " " + t
	}
	return name
}

// targetOf digs the argument that identifies what a call acts on. The keys are
// tried in the order a tool is most likely to be about them, matching what the
// terminal shows and what a permission rule matches.
func targetOf(args string) string {
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "path", "pattern", "description"} {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// locations points the editor at the file a call touches, so it can follow
// along in the buffer. Only file tools have one; a shell command's working
// directory is not a location in the sense ACP means.
func locations(name, args string) []ToolCallLocation {
	switch name {
	case "read", "write", "edit":
	default:
		return nil
	}
	var m struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(args), &m) != nil || m.Path == "" {
		return nil
	}
	return []ToolCallLocation{{Path: m.Path}}
}

// callID names a tool call for the life of a turn.
//
// raunen's ToolStart carries no id — the terminal never needed one, since it
// renders calls in order. ACP does need one, because a tool_call and the
// tool_call_update that completes it are separate notifications and the editor
// pairs them by id. Sub-agents run concurrently, so the id includes the task
// that produced the event.
func callID(seq int, task string) string {
	if task != "" {
		return fmt.Sprintf("%s-%d", task, seq)
	}
	return fmt.Sprintf("call-%d", seq)
}

// forward translates one event into notifications and sends them.
//
// It returns the text of a finished turn, so the caller can report a stop
// reason without inspecting the stream a second time.
type forwarder struct {
	send func(any) error
	// open maps a tool name and task to the id announced for it, so the
	// completion can be matched to the announcement. Keyed by name because
	// ToolEnd carries no id either, and pairing by name is how the one-shot
	// JSON output already does it.
	open map[string][]string
	seq  int
}

func newForwarder(send func(any) error) *forwarder {
	return &forwarder{send: send, open: map[string][]string{}}
}

// thought emits a line of commentary. Used for everything raunen reports that
// ACP has no update for.
func (f *forwarder) thought(text string) {
	_ = f.send(ContentChunk{
		SessionUpdate: updateAgentThought,
		Content:       TextBlock(text),
	})
}

// event forwards one agent event.
func (f *forwarder) event(ev agent.Event) {
	switch e := ev.(type) {
	case agent.TextDelta:
		_ = f.send(ContentChunk{
			SessionUpdate: updateAgentMessage,
			Content:       TextBlock(e.Text),
		})

	case agent.ReasoningDelta:
		// A reasoning model's thinking is exactly what an agent thought chunk
		// is for. It is shown and never enters the transcript, in ACP as in the
		// terminal.
		_ = f.send(ContentChunk{
			SessionUpdate: updateAgentThought,
			Content:       TextBlock(e.Text),
		})

	case agent.ToolStart:
		f.seq++
		id := callID(f.seq, e.Task)
		f.open[e.Name] = append(f.open[e.Name], id)
		notice := ToolCallNotice{
			SessionUpdate: updateToolCall,
			ToolCallID:    id,
			Title:         toolTitle(e.Name, e.Args),
			Name:          e.Name,
			Kind:          toolKind(e.Name),
			Status:        statusInProgress,
			Locations:     locations(e.Name, e.Args),
		}
		if json.Valid([]byte(e.Args)) {
			notice.RawInput = json.RawMessage(e.Args)
		}
		_ = f.send(notice)

	case agent.ToolEnd:
		id, ok := f.take(e.Name)
		if !ok {
			// A completion with no announcement should not happen, but emitting
			// an update for an id the editor never saw would be worse than
			// dropping it.
			return
		}
		up := ToolCallUpdate{
			SessionUpdate: updateToolCallEnd,
			ToolCallID:    id,
			Status:        statusCompleted,
		}
		if e.Err != nil {
			up.Status = statusFailed
			up.Content = []ToolCallContent{{Type: "content", Content: TextBlock(e.Err.Error())}}
		} else if e.Result != "" {
			up.Content = []ToolCallContent{{Type: "content", Content: TextBlock(e.Result)}}
		}
		_ = f.send(up)

	// The rest have no ACP update. They are reported rather than dropped:
	// a user watching a spinner should not have to guess why it is slow.
	case agent.Switched:
		f.thought(fmt.Sprintf("[switched to %s — %s]", e.To, e.Reason))
	case agent.Trimmed:
		f.thought(fmt.Sprintf("[dropped %d earlier messages to fit the context]", e.Messages))
	case agent.Rejected:
		f.thought(fmt.Sprintf("[%s refused — %s]", e.Ref, e.Reason))
	case agent.Retrying:
		f.thought(fmt.Sprintf("[retrying after %s — %s]", e.After.Round(1e9), e.Reason))
	case agent.Tripped:
		f.thought(fmt.Sprintf("[%s rested for %s — %s]", e.Provider, e.For.Round(1e9), e.Reason))
	case agent.TaskStart:
		f.thought(fmt.Sprintf("[delegating: %s]", e.Description))
	case agent.TaskEnd:
		if e.Err != nil {
			f.thought(fmt.Sprintf("[sub-agent failed: %v]", e.Err))
		} else {
			f.thought(fmt.Sprintf("[sub-agent finished: %s]", e.Description))
		}
	}
}

// take pops the id announced for the next completion of this tool name.
func (f *forwarder) take(name string) (string, bool) {
	ids := f.open[name]
	if len(ids) == 0 {
		return "", false
	}
	id := ids[0]
	f.open[name] = ids[1:]
	return id, true
}
