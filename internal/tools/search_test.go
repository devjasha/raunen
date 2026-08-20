package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// searchRepo builds a small tree to search. It is a git repository because that
// is what the tools list from, and because .gitignore being honoured is one of
// the things worth testing.
func searchRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	files := map[string]string{
		"main.go":                  "package main\n\nfunc main() {\n\tstart()\n}\n",
		"internal/app/app.go":      "package app\n\nfunc Start() error {\n\treturn nil\n}\n",
		"internal/app/app_test.go": "package app\n\nfunc TestStart(t *testing.T) {}\n",
		"internal/db/db.go":        "package db\n\nfunc Start() error {\n\treturn nil\n}\n",
		"README.md":                "# Title\n\nRun Start to begin.\n",
		".gitignore":               "ignored/\n*.log\n",
		"ignored/secret.go":        "package ignored\n\nfunc Start() {}\n",
		"debug.log":                "Start of log\n",
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v: %s", err, out)
		}
	}
	return root
}

// call runs a tool and fails the test if it errors.
func call(t *testing.T, root, name, args string) string {
	t.Helper()
	tool, ok := Default(root, 30<<10).Get(name)
	if !ok {
		t.Fatalf("no tool named %q", name)
	}
	out, err := tool.Run(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s(%s): %v", name, args, err)
	}
	return out
}

// TestGrepFindsMatches is the base case: the file, the line number and the line.
func TestGrepFindsMatches(t *testing.T) {
	root := searchRepo(t)
	out := call(t, root, "grep", `{"pattern":"func Start"}`)

	for _, want := range []string{"internal/app/app.go:3:", "internal/db/db.go:3:", "func Start"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

// TestGrepSkipsIgnoredFiles is the reason this is a tool rather than `grep -r`.
// A .gitignored directory is not part of the project and must never appear.
func TestGrepSkipsIgnoredFiles(t *testing.T) {
	root := searchRepo(t)
	out := call(t, root, "grep", `{"pattern":"func Start"}`)

	if strings.Contains(out, "ignored/") {
		t.Errorf("searched a gitignored directory:\n%s", out)
	}
	if strings.Contains(out, "debug.log") {
		t.Errorf("searched a gitignored file:\n%s", out)
	}
}

// TestGrepGlobNarrows checks the glob filter, and specifically that a pattern
// with no slash matches the base name anywhere in the tree — which is what
// anyone means by "*.go".
func TestGrepGlobNarrows(t *testing.T) {
	root := searchRepo(t)
	out := call(t, root, "grep", `{"pattern":"Start","glob":"*.md"}`)

	if !strings.Contains(out, "README.md") {
		t.Errorf("glob excluded the file it should have kept:\n%s", out)
	}
	if strings.Contains(out, ".go") {
		t.Errorf("glob did not exclude Go files:\n%s", out)
	}
}

// TestGrepPathNarrows limits the search to a subtree.
func TestGrepPathNarrows(t *testing.T) {
	root := searchRepo(t)
	out := call(t, root, "grep", `{"pattern":"func Start","path":"internal/db"}`)

	if !strings.Contains(out, "internal/db/db.go") {
		t.Errorf("did not search the requested directory:\n%s", out)
	}
	if strings.Contains(out, "internal/app") {
		t.Errorf("searched outside the requested directory:\n%s", out)
	}
}

// TestGrepPathCanBeOneFile supports checking a single file, which is how a model
// verifies something it already has in hand.
func TestGrepPathCanBeOneFile(t *testing.T) {
	root := searchRepo(t)
	out := call(t, root, "grep", `{"pattern":"start","path":"main.go"}`)

	if !strings.Contains(out, "main.go:") {
		t.Errorf("did not search the named file:\n%s", out)
	}
	if strings.Contains(out, "app.go") {
		t.Errorf("searched beyond the named file:\n%s", out)
	}
}

// TestGrepIgnoreCase covers the flag, since RE2's (?i) is not something every
// model reaches for.
func TestGrepIgnoreCase(t *testing.T) {
	root := searchRepo(t)
	out := call(t, root, "grep", `{"pattern":"START","ignore_case":true}`)
	if !strings.Contains(out, "func Start") {
		t.Errorf("case-insensitive search found nothing:\n%s", out)
	}
}

// TestGrepFilesOnly is the cheap mode: names, no lines, and a count of files
// rather than of matches.
func TestGrepFilesOnly(t *testing.T) {
	root := searchRepo(t)
	out := call(t, root, "grep", `{"pattern":"func Start","files_only":true}`)

	if strings.Contains(out, ":3:") {
		t.Errorf("files_only returned matching lines:\n%s", out)
	}
	if !strings.Contains(out, "internal/app/app.go") {
		t.Errorf("files_only lost a file that matched:\n%s", out)
	}
	if !strings.Contains(out, "files]") {
		t.Errorf("files_only did not report a file count:\n%s", out)
	}
}

// TestGrepContextLinesAreNotCounted pins the bug found while testing by hand:
// context lines were counted as matches, so one hit with two lines either side
// reported "5 matches".
func TestGrepContextLinesAreNotCounted(t *testing.T) {
	root := searchRepo(t)
	out := call(t, root, "grep", `{"pattern":"func Start","path":"internal/db","context":2}`)

	if !strings.Contains(out, "-2-") && !strings.Contains(out, "-4-") {
		t.Errorf("no context lines were shown:\n%s", out)
	}
	if !strings.Contains(out, "[1 match in 1 file]") {
		t.Errorf("context lines were counted as matches:\n%s", out)
	}
}

// TestGrepNoMatches has to say so plainly rather than returning nothing, which
// reads as a broken tool.
func TestGrepNoMatches(t *testing.T) {
	root := searchRepo(t)
	if out := call(t, root, "grep", `{"pattern":"zzzNotHere"}`); !strings.Contains(out, "no matches") {
		t.Errorf("output is %q, want it to say there were no matches", out)
	}
}

// TestGrepBadPatternExplains keeps a regexp error actionable: RE2 has no
// lookaround, and a model that tried one needs telling why.
func TestGrepBadPatternExplains(t *testing.T) {
	root := searchRepo(t)
	tool, _ := Default(root, 30<<10).Get("grep")
	_, err := tool.Run(context.Background(), json.RawMessage(`{"pattern":"(?=x)"}`))
	if err == nil {
		t.Fatal("invalid pattern was accepted")
	}
	if !strings.Contains(err.Error(), "RE2") {
		t.Errorf("error does not explain the syntax: %v", err)
	}
}

// TestGrepIsReadOnly is what makes searching work in plan mode, which is the
// mode where investigation is the only thing left to do.
func TestGrepIsReadOnly(t *testing.T) {
	reg := Default(t.TempDir(), 4096)
	for _, name := range []string{"grep", "glob"} {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("no tool named %q", name)
		}
		if !tool.IsReadOnly(json.RawMessage(`{}`)) {
			t.Errorf("%s is treated as mutating; it would be refused in plan mode", name)
		}
	}
}

// TestGlobMatchesByBaseName covers the case a model reaches for first.
func TestGlobMatchesByBaseName(t *testing.T) {
	root := searchRepo(t)
	out := call(t, root, "glob", `{"pattern":"*.go"}`)

	for _, want := range []string{"main.go", "internal/app/app.go", "internal/db/db.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("glob is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "README.md") {
		t.Errorf("glob matched a file it should not have:\n%s", out)
	}
}

// TestGlobDoubleStar is the syntax path.Match cannot do on its own.
func TestGlobDoubleStar(t *testing.T) {
	root := searchRepo(t)
	out := call(t, root, "glob", `{"pattern":"internal/**/*_test.go"}`)

	if !strings.Contains(out, "internal/app/app_test.go") {
		t.Errorf("** did not match across a directory:\n%s", out)
	}
	if strings.Contains(out, "db.go") {
		t.Errorf("** matched a file that is not a test:\n%s", out)
	}
}

// TestGlobDoubleStarMatchesZeroDirectories is the subtle half of **: "a/**/b"
// has to match "a/b" as well, since zero is a number of directories.
func TestGlobDoubleStarMatchesZeroDirectories(t *testing.T) {
	root := searchRepo(t)
	out := call(t, root, "glob", `{"pattern":"internal/**/app.go"}`)
	if !strings.Contains(out, "internal/app/app.go") {
		t.Errorf("** did not match one level down:\n%s", out)
	}
}

// TestGlobSkipsIgnored applies the same rule as grep: what git ignores is not
// part of the project.
func TestGlobSkipsIgnored(t *testing.T) {
	root := searchRepo(t)
	out := call(t, root, "glob", `{"pattern":"*.go"}`)
	if strings.Contains(out, "ignored/") {
		t.Errorf("glob listed a gitignored file:\n%s", out)
	}
}

// TestGlobNoMatchesNamesThePattern makes an empty result say what was searched
// for, so the model can see its own typo.
func TestGlobNoMatchesNamesThePattern(t *testing.T) {
	root := searchRepo(t)
	out := call(t, root, "glob", `{"pattern":"*.rs"}`)
	if !strings.Contains(out, "*.rs") {
		t.Errorf("output does not name the pattern that failed: %q", out)
	}
}

// TestSearchSkipsBinaryFiles keeps a compiled object out of the results, where
// it would match noise and print worse.
func TestSearchSkipsBinaryFiles(t *testing.T) {
	root := searchRepo(t)
	bin := filepath.Join(root, "blob.bin")
	if err := os.WriteFile(bin, []byte("Start\x00\x01\x02Start"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = root
	_ = cmd.Run()

	out := call(t, root, "grep", `{"pattern":"Start"}`)
	if strings.Contains(out, "blob.bin") {
		t.Errorf("searched a binary file:\n%s", out)
	}
}

// TestSearchWorksOutsideARepository covers the fallback walk, since not every
// directory someone runs the agent in is a git repository.
func TestSearchWorksOutsideARepository(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("func Start() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := call(t, root, "grep", `{"pattern":"func Start"}`); !strings.Contains(out, "a.go") {
		t.Errorf("found nothing outside a repository:\n%s", out)
	}
	if out := call(t, root, "glob", `{"pattern":"*.go"}`); !strings.Contains(out, "a.go") {
		t.Errorf("glob found nothing outside a repository:\n%s", out)
	}
}

// TestLongLinesAreClipped keeps a minified bundle from filling the result with
// one line that says nothing about why it matched.
func TestLongLinesAreClipped(t *testing.T) {
	root := t.TempDir()
	long := "needle" + strings.Repeat("x", maxLineBytes*2) + "\n"
	if err := os.WriteFile(filepath.Join(root, "bundle.js"), []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	out := call(t, root, "grep", `{"pattern":"needle"}`)
	if !strings.Contains(out, "line truncated") {
		t.Errorf("long line was not clipped:\n%.200s", out)
	}
	if len(out) > maxLineBytes*2 {
		t.Errorf("clipped output is still %d bytes", len(out))
	}
}
