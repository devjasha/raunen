package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Cancelling a command has to return promptly, and the interesting case is a
// command that spawns children: killing the shell does not kill its
// descendants, and they inherit the output pipe. Reading it then blocks on an
// EOF that never arrives, so pressing esc during an `npx install` left the turn
// hanging until the install finished on its own.
func TestCancelKillsTheProcessGroup(t *testing.T) {
	r := Default(t.TempDir(), 4096)
	bash, _ := r.Get("bash")

	cases := []struct{ name, cmd string }{
		{"plain command", "sleep 30"},
		{"child outlives the shell", "sleep 30 & wait"},
		{"nested children", "bash -c 'sleep 30' & sleep 30 & wait"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"command": c.cmd})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { time.Sleep(200 * time.Millisecond); cancel() }()

			done := make(chan error, 1)
			go func() { _, err := bash.Run(ctx, args); done <- err }()

			select {
			case err := <-done:
				// A cancelled command reports the cancellation rather than
				// handing the model whatever happened to be captured.
				if err == nil {
					t.Error("cancelled command returned no error")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("still blocked 5s after cancel")
			}
		})
	}
}

// A timeout has to release the same way, for the same reason.
func TestTimeoutKillsTheProcessGroup(t *testing.T) {
	r := Default(t.TempDir(), 4096)
	bash, _ := r.Get("bash")

	args, _ := json.Marshal(map[string]any{"command": "sleep 30 & wait", "timeout": 1})
	done := make(chan struct{})
	start := time.Now()
	go func() { bash.Run(context.Background(), args); close(done) }()

	select {
	case <-done:
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("returned after %s, want close to the 1s timeout", d.Round(time.Millisecond))
		}
	case <-time.After(8 * time.Second):
		t.Fatal("a timed-out command with children never returned")
	}
}
