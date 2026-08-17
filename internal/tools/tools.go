// Package tools defines the actions the agent can take and the schemas it
// advertises to the model.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"raunen/internal/provider"
)

// Tool is one callable action.
type Tool struct {
	Name        string
	Description string
	// Params is a JSON Schema object describing the arguments.
	Params map[string]any
	Run    func(ctx context.Context, args json.RawMessage) (string, error)
	// Mutates reports whether the tool can change state. It drives what plan
	// and accept modes gate on.
	Mutates bool
	// ReadOnly can clear Mutates for a particular call. Only bash needs it:
	// the tool can do anything, but most of what a model runs while
	// investigating is harmless.
	ReadOnly func(args json.RawMessage) bool
}

// IsReadOnly reports whether this specific invocation leaves state alone.
func (t Tool) IsReadOnly(args json.RawMessage) bool {
	if !t.Mutates {
		return true
	}
	return t.ReadOnly != nil && t.ReadOnly(args)
}

// Registry holds the tools available to a session.
type Registry struct {
	tools map[string]Tool
	order []string
}

func (r *Registry) Add(t Tool) {
	if r.tools == nil {
		r.tools = map[string]Tool{}
	}
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
}

// Names returns the tools in the order they were added. Callers that only read
// the registry should use this rather than reaching for the unexported fields.
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Clone returns an independent copy with the same tools, in the same order.
// Used to fold in a second source of tools without disturbing the original —
// MCP servers register into a copy so the built-in set stays intact.
func (r *Registry) Clone() *Registry {
	out := &Registry{}
	for _, n := range r.order {
		out.Add(r.tools[n])
	}
	return out
}

// Has reports whether a tool by this name already exists.
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// Without returns a copy of the registry with a tool removed. It is what stops
// a sub-agent from delegating further: the child simply does not have the tool.
func (r *Registry) Without(name string) *Registry {
	out := &Registry{}
	for _, n := range r.order {
		if n == name {
			continue
		}
		out.Add(r.tools[n])
	}
	return out
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Schemas renders the registry in the shape the API expects.
func (r *Registry) Schemas() []provider.ToolSchema {
	out := make([]provider.ToolSchema, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		var s provider.ToolSchema
		s.Type = "function"
		s.Function.Name = t.Name
		s.Function.Description = t.Description
		s.Function.Parameters = t.Params
		out = append(out, s)
	}
	return out
}

// obj is a small helper for writing JSON Schema without deep map literals.
func obj(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// OutputBudget returns how many bytes a single tool result may contribute,
// given the model's context window in tokens.
//
// This has to scale with the window. A fixed 30KB cap — roughly 8000 tokens —
// is larger than the whole context of a model served at 4096, so one read of an
// ordinary file would evict the system prompt and the user's question, and the
// model would start answering something else entirely.
//
// Roughly one byte per token of context works out to about a quarter of the
// window per result, which leaves room for the conversation around it.
func OutputBudget(contextTokens int) int {
	if contextTokens <= 0 {
		// Unknown window: assume something modest rather than something large.
		return 16 << 10
	}
	return min(30<<10, max(2<<10, contextTokens))
}

// Default returns the standard toolset, rooted at the given working directory.
// maxOutput caps how much any one result may return; see OutputBudget.
func Default(root string, maxOutput int) *Registry {
	r := &Registry{}

	// truncate cleans a result, then keeps it from crowding out the
	// conversation. Cleaning comes first so the budget is spent on content
	// rather than on colour codes and progress bars, which also means the
	// useful end of a long log is likelier to survive.
	truncate := func(s string) string {
		s = Clean(s)
		if len(s) <= maxOutput {
			return s
		}
		return s[:maxOutput] + fmt.Sprintf(
			"\n... [truncated, %d bytes total — read a specific part if you need more]", len(s))
	}

	// resolve keeps relative paths anchored to the session root. An empty path
	// is rejected rather than silently resolving to the root directory, which
	// only produces a confusing "is a directory" error further down.
	resolve := func(p string) (string, error) {
		if strings.TrimSpace(p) == "" {
			return "", fmt.Errorf("path is required")
		}
		if filepath.IsAbs(p) {
			return p, nil
		}
		return filepath.Join(root, p), nil
	}

	r.Add(Tool{
		Name:        "bash",
		Description: "Run a shell command and return its combined stdout and stderr.",
		Mutates:     true,
		ReadOnly:    bashIsReadOnly,
		Params: obj(map[string]any{
			"command": str("The shell command to run."),
			"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds (default 120)."},
		}, "command"),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Command) == "" {
				return "", fmt.Errorf("command is required")
			}
			if a.Timeout <= 0 {
				a.Timeout = 120
			}
			ctx, cancel := context.WithTimeout(ctx, time.Duration(a.Timeout)*time.Second)
			defer cancel()

			cmd := exec.CommandContext(ctx, "bash", "-c", a.Command)
			cmd.Dir = root
			// Killing the shell is not enough. Its children inherit the output
			// pipe, so reading it blocks on an EOF that never comes while they
			// live — cancelling a `npx install` left the turn hanging until the
			// install finished on its own.
			//
			// So the command gets its own process group and the whole group is
			// signalled, and WaitDelay bounds the wait in case something still
			// clings to the pipe.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error {
				if cmd.Process == nil {
					return nil
				}
				// Negative pid means the process group.
				return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			cmd.WaitDelay = 2 * time.Second

			out, err := cmd.CombinedOutput()
			if ctx.Err() == context.Canceled {
				// Cancelled by the user; whatever was captured is not worth
				// handing to the model as a result.
				return "", ctx.Err()
			}
			if ctx.Err() == context.DeadlineExceeded {
				return truncate(string(out)) + "\n[timed out]", nil
			}
			if err != nil {
				// A non-zero exit is information for the model, not a failure
				// of the tool itself.
				return truncate(fmt.Sprintf("%s\n[exit: %v]", out, err)), nil
			}
			if len(out) == 0 {
				return "[no output]", nil
			}
			return truncate(string(out)), nil
		},
	})

	r.Add(Tool{
		Name:        "read",
		Description: "Read a file from disk. Returns its contents with line numbers.",
		Params: obj(map[string]any{
			"path": str("Path to the file, absolute or relative to the working directory."),
		}, "path"),
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			p, err := resolve(a.Path)
			if err != nil {
				return "", err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			lines := strings.Split(string(b), "\n")
			// A trailing newline yields a final empty element that is not a
			// real line; leaving it in makes models miscount the file.
			if n := len(lines); n > 0 && lines[n-1] == "" {
				lines = lines[:n-1]
			}
			if len(lines) == 0 {
				return "[empty file]", nil
			}
			var sb strings.Builder
			for i, line := range lines {
				fmt.Fprintf(&sb, "%d\t%s\n", i+1, line)
			}
			return truncate(sb.String()), nil
		},
	})

	r.Add(Tool{
		Name:        "write",
		Description: "Write content to a file, creating or overwriting it.",
		Mutates:     true,
		Params: obj(map[string]any{
			"path":    str("Path to the file."),
			"content": str("Full content to write."),
		}, "path", "content"),
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			p, err := resolve(a.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(p, []byte(a.Content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path), nil
		},
	})

	r.Add(Tool{
		Name:    "edit",
		Mutates: true,
		Description: "Replace an exact string in a file. The old string must appear " +
			"exactly once; include surrounding context to make it unique.",
		Params: obj(map[string]any{
			"path": str("Path to the file."),
			"old":  str("Exact text to replace."),
			"new":  str("Replacement text."),
		}, "path", "old", "new"),
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Path string `json:"path"`
				Old  string `json:"old"`
				New  string `json:"new"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			p, err := resolve(a.Path)
			if err != nil {
				return "", err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			s := string(b)
			switch strings.Count(s, a.Old) {
			case 0:
				return "", fmt.Errorf("old string not found in %s", a.Path)
			case 1:
			default:
				return "", fmt.Errorf("old string appears multiple times in %s; add more context", a.Path)
			}
			if err := os.WriteFile(p, []byte(strings.Replace(s, a.Old, a.New, 1)), 0o644); err != nil {
				return "", err
			}
			return "edited " + a.Path, nil
		},
	})

	r.Add(Tool{
		Name:        "list",
		Description: "List files and directories at a path.",
		Params: obj(map[string]any{
			"path": str("Directory to list. Defaults to the working directory."),
		}),
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(raw, &a)
			if a.Path == "" {
				a.Path = "."
			}
			p, err := resolve(a.Path)
			if err != nil {
				return "", err
			}
			entries, err := os.ReadDir(p)
			if err != nil {
				return "", err
			}
			var sb strings.Builder
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() {
					name += "/"
				}
				sb.WriteString(name + "\n")
			}
			if sb.Len() == 0 {
				return "[empty]", nil
			}
			return truncate(sb.String()), nil
		},
	})

	return r
}

// readOnlyCommands are shell commands that only inspect. The list is an
// allowlist rather than a denylist of dangerous commands: there is no way to
// enumerate every way a shell can change something, so anything unrecognised
// counts as mutating. That makes plan mode usable for investigation without
// pretending the check is airtight.
var readOnlyCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true,
	"find": true, "fd": true, "tree": true, "file": true, "stat": true,
	"pwd": true, "which": true, "type": true, "echo": true, "printf": true,
	"date": true, "env": true, "uname": true, "whoami": true, "id": true,
	"sort": true, "uniq": true, "cut": true, "tr": true, "diff": true,
	"basename": true, "dirname": true, "realpath": true, "readlink": true,
	"du": true, "df": true, "jq": true, "column": true, "nl": true,
}

// readOnlyGitSubcommands are the git verbs that only report.
var readOnlyGitSubcommands = map[string]bool{
	"status": true, "log": true, "diff": true, "show": true, "branch": true,
	"remote": true, "config": true, "blame": true, "describe": true,
	"rev-parse": true, "ls-files": true, "shortlog": true, "tag": true,
}

// bashIsReadOnly reports whether a command only inspects. It is deliberately
// strict: redirection, command substitution and anything unrecognised all
// count as mutating, because a wrong "yes" here writes to the user's disk.
func bashIsReadOnly(raw json.RawMessage) bool {
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return false
	}
	cmd := strings.TrimSpace(a.Command)
	if cmd == "" {
		return false
	}
	// Redirection writes files, and substitution hides a second command.
	if strings.ContainsAny(cmd, ">`") || strings.Contains(cmd, "$(") {
		return false
	}
	// Every segment of a pipeline or list has to pass on its own.
	for _, part := range splitShellSegments(cmd) {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			return false
		}
		name := fields[0]
		// Reject env-var prefixes and absolute paths rather than reasoning
		// about them.
		if strings.ContainsAny(name, "=/") {
			return false
		}
		if name == "git" {
			if len(fields) < 2 || !readOnlyGitSubcommands[fields[1]] {
				return false
			}
			continue
		}
		if !readOnlyCommands[name] {
			return false
		}
	}
	return true
}

// splitShellSegments breaks a command on the separators that chain commands.
func splitShellSegments(cmd string) []string {
	var out []string
	cur := strings.Builder{}
	for i := 0; i < len(cmd); i++ {
		switch {
		case cmd[i] == '|', cmd[i] == ';', cmd[i] == '&', cmd[i] == '\n':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(cmd[i])
		}
	}
	out = append(out, cur.String())
	return out
}
