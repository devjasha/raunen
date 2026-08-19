package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"raunen/internal/agent"
	"raunen/internal/companion"
	"raunen/internal/config"
	"raunen/internal/provider"
	"raunen/internal/session"
	"raunen/internal/tools"
	"raunen/internal/vcs"
)

// repoModel is a model rooted in a real one-commit repository, which is what
// the branch command needs to have anything to say.
func repoModel(t *testing.T) (Model, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "first")

	ag := agent.New(provider.New("http://localhost:1/v1", "", "m"),
		tools.Default(dir, 4096), "")
	m := New(&config.Config{}, ag, dir, "x/m", session.New(dir, "x/m"), companion.Load())
	m.width, m.height = 80, 40
	return m, dir
}

func transcript(m Model) string {
	return ansi.Strip(strings.Join(rowsOf(m), "\n"))
}

// The headline case: /branch <name> checks the branch out and the status bar
// follows, because the model reads its branch from the repository rather than
// from what it was told at startup.
func TestBranchCommandSwitches(t *testing.T) {
	m, dir := repoModel(t)
	if err := vcs.Checkout(dir, "feature", true); err != nil {
		t.Fatal(err)
	}
	m.branch = vcs.Branch(dir)

	ret, _ := m.command("/branch main")
	mm := ret.(Model)
	if mm.branch != "main" {
		t.Fatalf("branch = %q, want main", mm.branch)
	}
	if got := vcs.Branch(dir); got != "main" {
		t.Fatalf("repository is on %q, want main", got)
	}
	if !strings.Contains(transcript(mm), "switched to main") {
		t.Errorf("no confirmation in the transcript:\n%s", transcript(mm))
	}
}

// A switch the model cannot see is a switch it will get wrong, so it is told
// about it in the transcript it reads.
func TestBranchSwitchTellsTheModel(t *testing.T) {
	m, dir := repoModel(t)
	m.branch = vcs.Branch(dir)
	before := len(m.ag.Messages())

	ret, _ := m.command("/branch -b feature")
	mm := ret.(Model)
	msgs := mm.ag.Messages()
	if len(msgs) != before+1 {
		t.Fatalf("messages = %d, want one more than %d", len(msgs), before)
	}
	last := msgs[len(msgs)-1]
	if last.Role != provider.User {
		t.Errorf("role = %q, want a user message", last.Role)
	}
	if !strings.Contains(last.Content, "feature") {
		t.Errorf("the note does not name the new branch: %q", last.Content)
	}
}

// -b creates, as it does in git.
func TestBranchCommandCreates(t *testing.T) {
	m, dir := repoModel(t)
	m.branch = vcs.Branch(dir)

	ret, _ := m.command("/branch -b spike")
	mm := ret.(Model)
	if mm.branch != "spike" {
		t.Fatalf("branch = %q, want spike", mm.branch)
	}
	if !strings.Contains(transcript(mm), "created spike") {
		t.Errorf("no confirmation:\n%s", transcript(mm))
	}
}

// git's refusal is the useful part of a failed switch, so it is what gets
// shown — and the model's view of the world stays as it was.
func TestBranchCommandReportsFailure(t *testing.T) {
	m, dir := repoModel(t)
	m.branch = vcs.Branch(dir)
	before := len(m.ag.Messages())

	ret, _ := m.command("/branch nope")
	mm := ret.(Model)
	if mm.branch != "main" {
		t.Fatalf("branch = %q, want to stay on main", mm.branch)
	}
	if got := transcript(mm); !strings.Contains(got, "✗") {
		t.Errorf("failure not reported:\n%s", got)
	}
	if len(mm.ag.Messages()) != before {
		t.Error("a failed switch should not tell the model anything")
	}
	_ = dir
}

// Outside a repository the command says so rather than shelling out to git.
func TestBranchOutsideRepository(t *testing.T) {
	m := testModel(t)
	m.branch = ""
	ret, _ := m.command("/branch")
	mm := ret.(Model)
	if mm.pick != nil {
		t.Error("the chooser should not open outside a repository")
	}
	if !strings.Contains(transcript(mm), "not a git repository") {
		t.Errorf("no explanation:\n%s", transcript(mm))
	}
}

// With no argument the command opens the chooser and goes to fetch the list.
func TestBranchCommandOpensPicker(t *testing.T) {
	m, dir := repoModel(t)
	m.branch = vcs.Branch(dir)

	ret, cmd := m.command("/branch")
	mm := ret.(Model)
	if mm.pick == nil || mm.pick.kind != pickBranch {
		t.Fatal("the branch chooser did not open")
	}
	if cmd == nil {
		t.Fatal("no command to fetch the branches")
	}
	msg, ok := cmd().(branchesMsg)
	if !ok {
		t.Fatalf("fetch returned %T, want branchesMsg", cmd())
	}
	if msg.err != nil {
		t.Fatalf("fetch: %v", msg.err)
	}
	if len(msg.branches) == 0 || msg.branches[0] != "main" {
		t.Fatalf("branches = %v, want main", msg.branches)
	}
}

// Switching to the branch already checked out is a no-op worth saying out loud
// rather than a git invocation.
func TestBranchAlreadyOnIt(t *testing.T) {
	m, dir := repoModel(t)
	m.branch = vcs.Branch(dir)
	ret, _ := m.command("/branch main")
	if got := transcript(ret.(Model)); !strings.Contains(got, "already on main") {
		t.Errorf("transcript = %q", got)
	}
}

// Mid-turn the agent is running tools against these files, so the switch waits
// rather than moving the working tree underneath it.
func TestBranchRefusedWhileBusy(t *testing.T) {
	m, dir := repoModel(t)
	m.branch = vcs.Branch(dir)
	begin(&m)

	ret, _ := m.command("/branch -b spike")
	mm := ret.(Model)
	if mm.branch != "main" {
		t.Fatalf("branch = %q, want to stay on main", mm.branch)
	}
	if got := vcs.Branch(dir); got != "main" {
		t.Fatalf("the repository moved to %q while busy", got)
	}
	if !strings.Contains(transcript(mm), "wait for the turn to finish") {
		t.Errorf("no explanation:\n%s", transcript(mm))
	}
}
