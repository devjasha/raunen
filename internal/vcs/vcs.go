// Package vcs reports repository state for the status bar, and makes the small
// changes to it that the UI offers directly.
package vcs

import (
	"context"
	"errors"
	"fmt"
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

// Branches lists the local branches of dir, most recently used first, followed
// by branches that exist only on a remote. Those are given by their short name
// — "fix-login" rather than "origin/fix-login" — because that is the name git
// checks out as a new tracking branch, and the name the user would type.
//
// Unlike Branch this reports its error: it answers a request the user made, so
// an empty list needs explaining rather than hiding.
func Branches(dir string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Ordered by last commit date rather than alphabetically: the branch you
	// want next is nearly always one you touched recently.
	local, err := run(ctx, dir, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	out := strings.Fields(local)

	seen := make(map[string]bool, len(out))
	for _, b := range out {
		seen[b] = true
	}

	remote, err := run(ctx, dir, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname:short)", "refs/remotes")
	if err != nil {
		// A repository with no remotes is ordinary; local branches are enough.
		return out, nil
	}
	for _, r := range strings.Fields(remote) {
		// "origin/HEAD" is a pointer, not a branch anyone checks out.
		_, short, ok := strings.Cut(r, "/")
		if !ok || short == "HEAD" || seen[short] {
			continue
		}
		seen[short] = true
		out = append(out, short)
	}
	return out, nil
}

// Checkout switches dir to branch, creating it from the current HEAD when
// create is set. The returned error carries git's own first line, which says
// far more about a refused switch than anything this package could invent.
func Checkout(dir, branch string, create bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	args := []string{"checkout"}
	if create {
		args = append(args, "-b")
	}
	args = append(args, branch)

	if _, err := run(ctx, dir, args...); err != nil {
		return err
	}
	return nil
}

// run executes git in dir and returns its stdout, turning a non-zero exit into
// an error carrying the first line git wrote to stderr.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err == nil {
		return string(out), nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := firstLine(string(ee.Stderr)); msg != "" {
			return "", errors.New(msg)
		}
	}
	return "", fmt.Errorf("git %s: %w", args[0], err)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	// git prefixes its complaints; the prefix is noise in a one-line report.
	return strings.TrimPrefix(strings.TrimSpace(s), "error: ")
}
