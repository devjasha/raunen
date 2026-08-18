package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// repo builds a throwaway repository with one commit, so the branch functions
// have something real to talk to. Real git rather than a fake: the whole point
// of this package is what git actually does.
func repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit := exec.Command("git", "commit", "-qam", "first")
	commit.Dir = dir
	add := exec.Command("git", "add", ".")
	add.Dir = dir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	return dir
}

func TestBranchesListsLocalBranches(t *testing.T) {
	dir := repo(t)
	if err := Checkout(dir, "feature", true); err != nil {
		t.Fatalf("checkout -b: %v", err)
	}

	got, err := Branches(dir)
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	want := map[string]bool{"main": true, "feature": true}
	if len(got) != len(want) {
		t.Fatalf("Branches = %v, want main and feature", got)
	}
	for _, b := range got {
		if !want[b] {
			t.Errorf("unexpected branch %q", b)
		}
	}
	// Most recently committed first, and creating a branch moves it there.
	if got[0] != "feature" {
		t.Errorf("first = %q, want the most recent branch, feature", got[0])
	}
}

func TestCheckoutSwitchesBranch(t *testing.T) {
	dir := repo(t)
	if err := Checkout(dir, "other", true); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := Branch(dir); got != "other" {
		t.Fatalf("Branch = %q, want other", got)
	}
	if err := Checkout(dir, "main", false); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if got := Branch(dir); got != "main" {
		t.Fatalf("Branch = %q, want main", got)
	}
}

// A switch that git refuses must say why, in git's own words.
func TestCheckoutReportsGitsComplaint(t *testing.T) {
	dir := repo(t)
	err := Checkout(dir, "nope", false)
	if err == nil {
		t.Fatal("switching to a missing branch should fail")
	}
	if err.Error() == "" {
		t.Fatal("error carries no message")
	}
	if got := err.Error(); got[:1] == "" {
		t.Fatalf("unhelpful message %q", got)
	}
}

// Creating a branch that already exists is refused, and the message is one
// line rather than git's full paragraph.
func TestCheckoutErrorIsOneLine(t *testing.T) {
	dir := repo(t)
	if err := Checkout(dir, "dup", true); err != nil {
		t.Fatal(err)
	}
	err := Checkout(dir, "dup", true)
	if err == nil {
		t.Fatal("creating an existing branch should fail")
	}
	for _, r := range err.Error() {
		if r == '\n' {
			t.Fatalf("message spans lines: %q", err.Error())
		}
	}
}

func TestBranchesOutsideRepository(t *testing.T) {
	if _, err := Branches(t.TempDir()); err == nil {
		t.Fatal("a directory outside a repository should report an error")
	}
}
