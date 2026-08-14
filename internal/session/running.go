package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Running describes a live raunen instance. Instances announce themselves here
// because there is no reliable way to find them from the outside: tmux reports
// a pane's foreground process, which during a turn is whatever the agent
// happens to be running rather than raunen itself.
type Running struct {
	PID       int    `json:"pid"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Model     string `json:"model"`
	Root      string `json:"root"`
	// Pane is $TMUX_PANE, empty when not running under tmux.
	Pane    string    `json:"pane"`
	Started time.Time `json:"started"`
}

// RunningDir holds one file per live instance.
func RunningDir() string { return filepath.Join(filepath.Dir(Dir()), "running") }

func runningPath(pid int) string {
	return filepath.Join(RunningDir(), fmt.Sprintf("%d.json", pid))
}

// Register announces this process. The returned function removes the entry and
// should be deferred; a crash leaves the file behind, which ListRunning prunes
// when it finds the process gone.
func (s *Session) Register(model string) func() {
	r := Running{
		PID:       os.Getpid(),
		SessionID: s.ID,
		Title:     s.Title,
		Model:     model,
		Root:      s.Root,
		Pane:      os.Getenv("TMUX_PANE"),
		Started:   time.Now(),
	}
	if err := os.MkdirAll(RunningDir(), 0o755); err != nil {
		return func() {}
	}
	b, err := json.Marshal(r)
	if err != nil {
		return func() {}
	}
	if err := os.WriteFile(runningPath(r.PID), b, 0o644); err != nil {
		return func() {}
	}
	return func() { os.Remove(runningPath(r.PID)) }
}

// UpdateTitle refreshes the advertised title once the conversation has one, so
// the picker shows what a session is about rather than "untitled".
func (s *Session) UpdateTitle() {
	p := runningPath(os.Getpid())
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var r Running
	if json.Unmarshal(b, &r) != nil {
		return
	}
	if r.Title == s.Title && r.SessionID == s.ID {
		return
	}
	r.Title, r.SessionID = s.Title, s.ID
	if b, err = json.Marshal(r); err == nil {
		_ = os.WriteFile(p, b, 0o644)
	}
}

// alive reports whether a process still exists. Signal 0 performs the
// permission and existence checks without delivering anything.
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// ListRunning returns live instances, oldest first, pruning entries whose
// process has gone.
func ListRunning() ([]Running, error) {
	entries, err := os.ReadDir(RunningDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []Running
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(RunningDir(), e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var r Running
		if json.Unmarshal(b, &r) != nil {
			os.Remove(p)
			continue
		}
		if !alive(r.PID) {
			// The instance died without cleaning up after itself.
			os.Remove(p)
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out, nil
}
