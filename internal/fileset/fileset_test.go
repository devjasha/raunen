package fileset

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func write(t *testing.T, root, name, body string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestListHonoursGitignore is the whole reason git does the listing: what a
// repository ignores is not part of the project, and reimplementing .gitignore
// is a parser's worth of work to approximate badly.
func TestListHonoursGitignore(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".gitignore", "ignored/\n*.log\n")
	write(t, root, "keep.go", "package a\n")
	write(t, root, "ignored/drop.go", "package b\n")
	write(t, root, "noisy.log", "x\n")

	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v: %s", err, out)
	}

	files, ok := List(context.Background(), root, 0)
	if !ok {
		t.Fatal("git did not answer in a repository")
	}
	if !slices.Contains(files, "keep.go") {
		t.Errorf("dropped a tracked file: %v", files)
	}
	for _, drop := range []string{"ignored/drop.go", "noisy.log"} {
		if slices.Contains(files, drop) {
			t.Errorf("listed the ignored %q: %v", drop, files)
		}
	}
}

// TestListFallsBackOutsideARepository covers the walk, and reports that git did
// not answer — which is what separates "no repository" from "an empty one".
func TestListFallsBackOutsideARepository(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package a\n")
	write(t, root, "sub/b.go", "package b\n")

	files, ok := List(context.Background(), root, 0)
	if ok {
		t.Error("reported a git listing outside a repository")
	}
	for _, want := range []string{"a.go", "sub/b.go"} {
		if !slices.Contains(files, want) {
			t.Errorf("walk missed %q: %v", want, files)
		}
	}
}

// TestWalkSkipsHeavyDirectories keeps the fallback from descending into the
// places where the bulk of a tree usually is.
func TestWalkSkipsHeavyDirectories(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package a\n")
	write(t, root, "node_modules/pkg/index.js", "x\n")
	write(t, root, ".git/config", "x\n")

	files, _ := List(context.Background(), root, 0)
	for _, f := range files {
		if Skipped(f) {
			t.Errorf("walked into a directory it should skip: %q", f)
		}
	}
	if !slices.Contains(files, "a.go") {
		t.Errorf("skipping cost a real file: %v", files)
	}
}

// TestMaxCaps bounds a stray scan of something enormous.
func TestMaxCaps(t *testing.T) {
	root := t.TempDir()
	for i := range 20 {
		write(t, root, filepath.Join("d", string(rune('a'+i))+".go"), "x\n")
	}
	if files, _ := List(context.Background(), root, 5); len(files) > 5 {
		t.Errorf("returned %d files despite a cap of 5", len(files))
	}
}

// TestSkipped is the shared rule, used on both the git list and the walk: git
// honours .gitignore but knows nothing about which directories are noise.
func TestSkipped(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"internal/app/app.go", false},
		{"node_modules/x/y.js", true},
		{"a/vendor/b.go", true},
		{".git/config", true},
		{"vendored.go", false},
	} {
		if got := Skipped(tc.path); got != tc.want {
			t.Errorf("Skipped(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
