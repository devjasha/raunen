package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tree writes a set of files and returns the directory holding them.
func tree(t *testing.T, paths ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func indexOf(paths ...string) *fileIndex {
	return &fileIndex{paths: withDirs(paths)}
}

// A folder has to be offerable, and nothing walks them separately: they are
// derived from the files underneath.
func TestWithDirsDerivesFolders(t *testing.T) {
	got := withDirs([]string{"main.go", "internal/ui/ui.go", "internal/ui/files.go"})

	want := []string{"internal/", "internal/ui/", "internal/ui/files.go", "internal/ui/ui.go", "main.go"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("withDirs =\n%v\nwant\n%v", got, want)
	}
}

// A bare @ is a way to look around, so it lists the top of the tree rather
// than every path in it — and folders come first, since opening one is the
// next thing you do with it.
func TestChildrenListsOneLevel(t *testing.T) {
	idx := indexOf("main.go", "README.md", "internal/ui/ui.go", "internal/agent/agent.go")

	if got, want := idx.children(""), []string{"internal/", "README.md", "main.go"}; !equal(got, want) {
		t.Errorf("children(\"\") = %v, want %v", got, want)
	}
	if got, want := idx.children("internal/"), []string{"internal/agent/", "internal/ui/"}; !equal(got, want) {
		t.Errorf("children(internal/) = %v, want %v", got, want)
	}
}

// Typing part of a name should find the file, not the forty files that happen
// to sit in a directory of that name.
func TestSearchRanksNamesOverPaths(t *testing.T) {
	idx := indexOf("internal/ui/ui.go", "internal/ui/picker.go", "internal/ui/welcome.go")

	got := idx.search("ui.go")
	if len(got) == 0 || got[0] != "internal/ui/ui.go" {
		t.Fatalf("search(ui.go) = %v, want the file itself first", got)
	}

	// A shallow match beats a deep one of the same kind.
	idx = indexOf("main.go", "internal/deep/nested/main.go")
	if got := idx.search("main"); got[0] != "main.go" {
		t.Errorf("search(main) = %v, want the shallow file first", got)
	}
}

// An abbreviation should still find a long path, the way the model chooser
// works — but only after everything that matched properly.
func TestSearchFallsBackToSubsequence(t *testing.T) {
	idx := indexOf("internal/companion/companion.go", "notes.md")

	got := idx.search("icg")
	if len(got) != 1 || got[0] != "internal/companion/companion.go" {
		t.Errorf("search(icg) = %v, want the subsequence match", got)
	}
}

// The heavy directories are where most of a tree is and none of what anyone
// means by "this file".
func TestScanSkipsNoise(t *testing.T) {
	root := tree(t, "main.go", "node_modules/left-pad/index.js", "internal/ui/ui.go")

	for _, p := range buildIndex(root).paths {
		if strings.Contains(p, "node_modules") {
			t.Errorf("indexed %q, want node_modules skipped", p)
		}
	}
}

// In a repository the file list is git's, which is what makes .gitignore work
// without reimplementing it.
func TestScanHonoursGitignore(t *testing.T) {
	root := tree(t, "main.go", "secret.env", "build/out.bin", ".gitignore")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"),
		[]byte("secret.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", root, "init").Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}

	var paths []string
	paths = append(paths, buildIndex(root).paths...)
	for _, p := range paths {
		if p == "secret.env" {
			t.Error("indexed an ignored file")
		}
	}
	if !contains(paths, "main.go") {
		t.Errorf("indexed %v, want main.go among them", paths)
	}
	// Untracked but not ignored still counts: a file written a moment ago is
	// exactly the one you want to point at.
	if !contains(paths, ".gitignore") {
		t.Errorf("indexed %v, want untracked files included", paths)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
