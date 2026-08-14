// Package vcs reports repository state for the status bar.
package vcs

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Branch returns the current git branch for dir, or "" when dir is not in a
// repository. A detached HEAD reports the short commit instead.
//
// Errors are deliberately swallowed: this only feeds a status bar, and a
// missing git or an unusual repository state should never surface as an error.
func Branch(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(out))
	if b != "HEAD" {
		return b
	}

	// Detached HEAD: fall back to the short commit hash.
	cmd = exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD")
	cmd.Dir = dir
	if out, err = cmd.Output(); err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
