package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write creates a file and the directories leading to it.
func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// home points the home directory at dir, which is where the walk stops. Without
// it a test's temporary directory sits under /var or /tmp and the walk would
// climb past anything the test created.
func home(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
}

// TestNearestFileWins checks the ordering the prompt depends on: a rule in a
// sub-package exists to override the one at the top, so it has to come last.
func TestNearestFileWins(t *testing.T) {
	tmp := t.TempDir()
	home(t, tmp)
	root := filepath.Join(tmp, "project")
	inner := filepath.Join(root, "apps", "web")
	write(t, filepath.Join(root, Name), "root rule")
	write(t, filepath.Join(inner, Name), "inner rule")

	set := Load(inner, "")
	if len(set.Files) != 2 {
		t.Fatalf("loaded %d files, want 2: %+v", len(set.Files), set.Files)
	}
	if !strings.Contains(set.Files[0].Text, "root") {
		t.Errorf("first file is %q, want the root one", set.Files[0].Text)
	}
	if !strings.Contains(set.Files[1].Text, "inner") {
		t.Errorf("last file is %q, want the nearest one", set.Files[1].Text)
	}
}

// TestGlobalComesFirst keeps the global file outermost: it is the most general
// thing there is, so a project must be able to override it.
func TestGlobalComesFirst(t *testing.T) {
	tmp := t.TempDir()
	home(t, tmp)
	root := filepath.Join(tmp, "project")
	global := filepath.Join(tmp, "global", Name)
	write(t, global, "global rule")
	write(t, filepath.Join(root, Name), "project rule")

	set := Load(root, global)
	if len(set.Files) != 2 {
		t.Fatalf("loaded %d files, want 2", len(set.Files))
	}
	if !strings.Contains(set.Files[0].Text, "global") {
		t.Errorf("first file is %q, want the global one", set.Files[0].Text)
	}
}

// TestStopsAtHome is the boundary that keeps unrelated work out. A stray
// AGENTS.md in a home directory would otherwise attach itself to every project
// underneath it.
func TestStopsAtHome(t *testing.T) {
	tmp := t.TempDir()
	home(t, tmp)
	write(t, filepath.Join(tmp, Name), "should not be read")
	root := filepath.Join(tmp, "project")
	write(t, filepath.Join(root, Name), "project rule")

	set := Load(root, "")
	if len(set.Files) != 1 {
		t.Fatalf("loaded %d files, want only the project one: %+v", len(set.Files), set.Files)
	}
	if strings.Contains(set.Files[0].Text, "should not") {
		t.Error("read the file in the home directory")
	}
}

// TestNoFilesIsSilent confirms the common case costs nothing: a project without
// an AGENTS.md must add no framing to the prompt at all.
func TestNoFilesIsSilent(t *testing.T) {
	tmp := t.TempDir()
	home(t, tmp)
	root := filepath.Join(tmp, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	set := Load(root, "")
	if len(set.Files) != 0 {
		t.Fatalf("found %d files in an empty tree", len(set.Files))
	}
	if got := set.Prompt(root); got != "" {
		t.Errorf("prompt is %q, want empty", got)
	}
	if got := set.Summary(root); got != "" {
		t.Errorf("summary is %q, want empty", got)
	}
}

// TestEmptyFileIsSkipped treats a file someone meant to fill in as nothing to
// say, rather than as a heading with no content under it.
func TestEmptyFileIsSkipped(t *testing.T) {
	tmp := t.TempDir()
	home(t, tmp)
	root := filepath.Join(tmp, "project")
	write(t, filepath.Join(root, Name), "   \n\n  \n")

	if set := Load(root, ""); len(set.Files) != 0 {
		t.Fatalf("read an empty file: %+v", set.Files)
	}
}

// TestLargeFileIsTruncated bounds one file, since instructions that fill the
// window evict the conversation they were meant to inform.
func TestLargeFileIsTruncated(t *testing.T) {
	tmp := t.TempDir()
	home(t, tmp)
	root := filepath.Join(tmp, "project")
	big := strings.Repeat("a rule that goes on\n", MaxFileBytes/10)
	write(t, filepath.Join(root, Name), big)

	set := Load(root, "")
	if len(set.Files) != 1 {
		t.Fatalf("loaded %d files, want 1", len(set.Files))
	}
	f := set.Files[0]
	if !f.Truncated {
		t.Error("oversized file was not marked truncated")
	}
	if len(f.Text) > MaxFileBytes {
		t.Errorf("kept %d bytes, want at most %d", len(f.Text), MaxFileBytes)
	}
	if !strings.Contains(set.Prompt(root), "[truncated]") {
		t.Error("prompt does not say the file was cut")
	}
}

// TestTotalBudgetIsBounded checks the whole block is capped, not just each file,
// and that what did not fit is reported rather than silently missing.
func TestTotalBudgetIsBounded(t *testing.T) {
	tmp := t.TempDir()
	home(t, tmp)
	// Three nested directories, each with a full-size file: together they are
	// over the total budget even though none exceeds the per-file cap.
	dir := filepath.Join(tmp, "project")
	body := strings.Repeat("x", MaxFileBytes)
	for _, sub := range []string{"", "a", filepath.Join("a", "b")} {
		write(t, filepath.Join(dir, sub, Name), body)
	}
	deep := filepath.Join(dir, "a", "b")

	set := Load(deep, "")
	total := 0
	for _, f := range set.Files {
		total += len(f.Text)
	}
	if total > MaxTotalBytes {
		t.Errorf("assembled %d bytes, want at most %d", total, MaxTotalBytes)
	}
	if set.Dropped == 0 && len(set.Files) == 3 {
		t.Error("nothing was dropped or truncated despite exceeding the budget")
	}
}

// TestPromptLabelsPaths keeps each file attributed. The model has to know which
// directory a rule came from to judge how far it reaches.
func TestPromptLabelsPaths(t *testing.T) {
	tmp := t.TempDir()
	home(t, tmp)
	root := filepath.Join(tmp, "project")
	inner := filepath.Join(root, "apps", "web")
	write(t, filepath.Join(root, Name), "root rule")
	write(t, filepath.Join(inner, Name), "inner rule")

	prompt := Load(inner, "").Prompt(inner)
	for _, want := range []string{"root rule", "inner rule", Name, "more specific"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q:\n%s", want, prompt)
		}
	}
	// The nearest file is in the working directory itself, so it is labelled
	// bare. An ancestor is shown against the home directory rather than as a
	// run of "..", which says where it is rather than how to walk to it.
	if !strings.Contains(prompt, filepath.Join("~", "project", Name)) {
		t.Errorf("outer file is not labelled against the home directory:\n%s", prompt)
	}
}

// TestUnreadableFileIsSkipped keeps a permissions problem from stopping a turn:
// instructions improve a session, they are not a precondition for one.
func TestUnreadableFileIsSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	tmp := t.TempDir()
	home(t, tmp)
	root := filepath.Join(tmp, "project")
	path := filepath.Join(root, Name)
	write(t, path, "secret rule")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })

	if set := Load(root, ""); len(set.Files) != 0 {
		t.Fatalf("read a file it had no permission for: %+v", set.Files)
	}
}

// TestSummaryNamesFiles is what /status and startup print, so it has to say
// which files are in play rather than only how many.
func TestSummaryNamesFiles(t *testing.T) {
	tmp := t.TempDir()
	home(t, tmp)
	root := filepath.Join(tmp, "project")
	write(t, filepath.Join(root, Name), "rule")

	if got := Load(root, "").Summary(root); got != Name {
		t.Errorf("summary is %q, want %q", got, Name)
	}
}
