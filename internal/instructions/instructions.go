// Package instructions finds the AGENTS.md files that apply to a directory and
// assembles them into a block for the system prompt.
//
// The problem it solves is that a project's conventions — how to run the tests,
// which directories are generated, the fact that one package is load-bearing —
// are not discoverable from the code in the time a model has to look. Without
// them the agent relearns the same things every session, badly, and the only
// place to write them down was the global "system" key, which applies to every
// project at once.
//
// AGENTS.md is the file other agents already read, so a repository that has one
// works here without being adapted to raunen specifically.
package instructions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Name is the file that carries project instructions.
const Name = "AGENTS.md"

const (
	// MaxFileBytes is how much of one file is read. A file past this size has
	// stopped being instructions and become documentation, and pasting a
	// hundred kilobytes of it into every request would evict the conversation
	// it was meant to inform.
	MaxFileBytes = 32 << 10
	// MaxTotalBytes bounds the whole assembled block, however many files it
	// came from. On an 8k local model even this is generous; the cap exists so
	// a deep tree of instruction files cannot silently consume the window.
	MaxTotalBytes = 64 << 10
)

// File is one instruction file that applied, kept with its path so the UI can
// report what was loaded and the model can be told where a rule came from.
type File struct {
	// Path is the file's location, absolute.
	Path string
	// Text is its contents, truncated to MaxFileBytes.
	Text string
	// Truncated reports that Text is shorter than the file on disk.
	Truncated bool
}

// Set is everything that applied to one directory, nearest last.
type Set struct {
	Files []File
	// Dropped counts files that were found but left out because the total
	// budget was already spent. Reported rather than hidden: instructions that
	// silently did not arrive look like a model ignoring them.
	Dropped int
}

// Load finds the instruction files that apply to root: the global one first,
// then every AGENTS.md from the top of the tree down to root itself.
//
// Order is outermost first so the nearest file is read last. Both end up in the
// prompt, and when they disagree the more specific one should be the thing the
// model saw most recently — a rule in a sub-package is there precisely to
// override the one at the top.
//
// A file that cannot be read is skipped rather than fatal. Instructions are an
// improvement to a turn, not a precondition for one, and refusing to start
// because a file has the wrong permissions would be a poor trade.
func Load(root, global string) Set {
	var set Set
	budget := MaxTotalBytes

	add := func(path string) {
		if budget <= 0 {
			set.Dropped++
			return
		}
		f, ok := read(path, min(MaxFileBytes, budget))
		if !ok {
			return
		}
		budget -= len(f.Text)
		set.Files = append(set.Files, f)
	}

	if global != "" {
		add(global)
	}
	for _, dir := range chain(root) {
		add(filepath.Join(dir, Name))
	}
	return set
}

// chain lists the directories to look in, from the outermost down to root.
//
// The walk stops at the home directory rather than continuing to the filesystem
// root. Everything above a project is shared by every project, and a stray
// AGENTS.md in a home directory or in /tmp would otherwise attach itself to
// unrelated work — the global file is the deliberate way to say something once.
// Home itself is excluded for the same reason: the config directory already
// holds the file that means "always".
func chain(root string) []string {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	stop, err := os.UserHomeDir()
	if err == nil {
		stop, _ = filepath.Abs(stop)
	}

	var dirs []string
	for dir := abs; ; {
		if stop != "" && dir == stop {
			break
		}
		dirs = append(dirs, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root, which only happens for a project
			// outside the home directory. Stop rather than loop.
			break
		}
		dir = parent
	}

	// Collected nearest first because that is the direction the walk goes;
	// reversed so the prompt reads general to specific.
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

// read returns the file's contents, capped at limit bytes. A missing file is
// the ordinary case and is not an error.
func read(path string, limit int) (File, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return File{}, false
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		// An empty file is a file someone meant to fill in. Nothing to say.
		return File{}, false
	}
	f := File{Path: path}
	if len(text) > limit {
		// Cut on a line boundary where there is one nearby, so the block does
		// not end mid-sentence and read as though the rule continues.
		cut := text[:limit]
		if i := strings.LastIndexByte(cut, '\n'); i > limit/2 {
			cut = cut[:i]
		}
		text = cut
		f.Truncated = true
	}
	f.Text = text
	return f, true
}

// Prompt renders the set as the block appended to the system prompt, empty when
// nothing applied.
//
// Each file is labelled with its path. The model needs to know which directory
// a rule came from to judge how far it reaches, and an unlabelled pile of
// markdown from three files reads as one contradictory document.
//
// The framing says these are the user's standing instructions but rank below
// what is being asked right now. Without that a project file saying "always run
// the full suite" turns a request to check one function into a ten-minute
// build.
func (s Set) Prompt(root string) string {
	if len(s.Files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Project instructions follow. They are standing conventions for this " +
		"working directory, and you should follow them unless the user's request " +
		"says otherwise — a direct request wins. Where two files disagree, the one " +
		"in the more specific directory wins.")
	for _, f := range s.Files {
		b.WriteString("\n\n--- " + display(f.Path, root) + " ---\n")
		b.WriteString(f.Text)
		if f.Truncated {
			b.WriteString("\n[truncated]")
		}
	}
	return b.String()
}

// display shortens a path for the label: relative to the working directory when
// it is inside it, and against the home directory otherwise, since an absolute
// path from a home directory is mostly noise.
func display(path, root string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return path
}

// Summary is a one-line report of what was loaded, for /status and startup.
func (s Set) Summary(root string) string {
	if len(s.Files) == 0 {
		return ""
	}
	names := make([]string, 0, len(s.Files))
	for _, f := range s.Files {
		names = append(names, display(f.Path, root))
	}
	out := strings.Join(names, ", ")
	if s.Dropped > 0 {
		out += fmt.Sprintf(" (%d more not read: over %d KB)", s.Dropped, MaxTotalBytes>>10)
	}
	return out
}
