package tools

import (
	"encoding/json"
	"testing"
)

// A wrong "yes" here writes to the user's disk, so the allowlist is tested
// from both directions.
func TestBashIsReadOnly(t *testing.T) {
	readOnly := []string{
		"ls -la",
		"cat main.go",
		"grep -rn TODO .",
		"rg --files",
		"find . -name '*.go'",
		"git status",
		"git log --oneline -20",
		"git diff HEAD~1",
		"ls -la | grep go | wc -l",
		"head -20 README.md; tail -5 README.md",
	}
	for _, c := range readOnly {
		if !bashIsReadOnly(args(c)) {
			t.Errorf("bashIsReadOnly(%q) = false, want true", c)
		}
	}

	mutating := []string{
		"rm -rf build",
		"echo hi > file.txt",     // redirection writes
		"cat $(cat cmd.txt)",     // substitution hides a command
		"ls `whoami`",            // backticks likewise
		"git commit -m x",        // not a read-only verb
		"git push",               // ditto
		"ls && rm file",          // second segment mutates
		"grep x . | tee out.txt", // tee writes
		"FOO=1 ls",               // env prefix, not reasoned about
		"/bin/ls",                // absolute path, not reasoned about
		"npm install",            // unrecognised
		"python3 script.py",      // unrecognised
		"",                       // nothing to check
		"mv a b",                 // unrecognised
	}
	for _, c := range mutating {
		if bashIsReadOnly(args(c)) {
			t.Errorf("bashIsReadOnly(%q) = true, want false", c)
		}
	}
}

func args(cmd string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}

func TestToolsAreClassified(t *testing.T) {
	r := Default(t.TempDir(), OutputBudget(4096))
	want := map[string]bool{
		"read": false, "list": false,
		"write": true, "edit": true, "bash": true,
	}
	for name, mutates := range want {
		tool, ok := r.Get(name)
		if !ok {
			t.Fatalf("tool %q missing", name)
		}
		if tool.Mutates != mutates {
			t.Errorf("%s.Mutates = %v, want %v", name, tool.Mutates, mutates)
		}
	}

	// A read-only bash call must come out read-only despite Mutates.
	bash, _ := r.Get("bash")
	if !bash.IsReadOnly(args("git status")) {
		t.Error("bash IsReadOnly(git status) = false, want true")
	}
	if bash.IsReadOnly(args("rm -rf /")) {
		t.Error("bash IsReadOnly(rm -rf /) = true, want false")
	}
}

func TestOutputBudget(t *testing.T) {
	// A result must never be able to outweigh the window it has to fit in.
	for _, ctx := range []int{2048, 4096, 8192, 32768, 262144} {
		if got := OutputBudget(ctx); got > 30<<10 || got < 2<<10 {
			t.Errorf("OutputBudget(%d) = %d, out of range", ctx, got)
		}
	}
	if OutputBudget(4096) >= 4096*4 {
		t.Error("budget at 4096 tokens is larger than the context it must fit in")
	}
	if OutputBudget(0) != 16<<10 {
		t.Errorf("OutputBudget(0) = %d, want 16KB default", OutputBudget(0))
	}
}
