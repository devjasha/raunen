package ui

import (
	"context"
	"path"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"raunen/internal/fileset"
)

// fileIndex is a snapshot of what is under the session root, kept so that an
// @ mention can be completed without walking the disk on every keystroke.
//
// Paths are relative to the root and directories carry a trailing slash, which
// is both how they are shown and what makes completing one continue into it
// rather than end there.
type fileIndex struct {
	paths []string
	at    time.Time
}

const (
	// maxIndexed bounds the snapshot. A tree large enough to pass this is one
	// where scrolling a completion list was never going to be how you find a
	// file anyway, and the cap keeps a stray scan of a home directory from
	// costing a hundred megabytes.
	maxIndexed = 20000
	// indexTTL is how long a snapshot is trusted. Files appear and disappear
	// while the agent works, so a mention started after this is worth a fresh
	// look; mid-word is not, or the list would shift under the cursor.
	indexTTL = 10 * time.Second
	// maxMatches caps what a query returns, so a broad one does not sort
	// thousands of paths to show six of them.
	maxMatches = 50
	// scanTimeout bounds the listing. It feeds a completion popup: late is the
	// same as never, and a partial list beats a stalled input.
	scanTimeout = 3 * time.Second
)

// filesMsg carries a finished scan back to the model.
type filesMsg struct{ index *fileIndex }

// scanFiles lists the tree in the background. It is a command rather than a
// call because the input must keep taking keystrokes while a large repository
// is being read.
func scanFiles(root string) tea.Cmd {
	return func() tea.Msg { return filesMsg{index: buildIndex(root)} }
}

// buildIndex lists the files under root and derives the directories from them.
//
// The listing itself belongs to fileset, which the search tools use too: what
// counts as part of the project is one question, and two answers to it would
// drift apart the moment either was fixed.
func buildIndex(root string) *fileIndex {
	ctx, cancel := context.WithTimeout(context.Background(), scanTimeout)
	defer cancel()
	files, _ := fileset.List(ctx, root, maxIndexed)
	return &fileIndex{paths: withDirs(files), at: time.Now()}
}

// withDirs adds the directories implied by the files, so that a folder can be
// mentioned as readily as a file. Listing them separately would mean a second
// walk; every directory that matters already appears as a prefix here.
func withDirs(files []string) []string {
	seen := make(map[string]bool, len(files))
	out := make([]string, 0, len(files)+len(files)/4)
	for _, f := range files {
		out = append(out, f)
		for dir := path.Dir(f); dir != "." && dir != "/"; dir = path.Dir(dir) {
			if seen[dir] {
				break
			}
			seen[dir] = true
			out = append(out, dir+"/")
		}
	}
	sort.Strings(out)
	return out
}

// stale reports whether the snapshot is old enough to be worth taking again.
func (f *fileIndex) stale() bool { return f == nil || time.Since(f.at) > indexTTL }

// children lists what sits directly inside dir, which is "" for the root.
// This is what makes a bare @ a way to look around rather than a query with no
// terms, and what makes completing a directory step into it.
func (f *fileIndex) children(dir string) []string {
	var dirs, files []string
	for _, p := range f.paths {
		rest, ok := strings.CutPrefix(p, dir)
		if !ok || rest == "" {
			continue
		}
		switch i := strings.IndexByte(rest, '/'); {
		case i < 0:
			files = append(files, p)
		case i == len(rest)-1:
			// A directory one level down: the slash is its own trailing one.
			dirs = append(dirs, p)
		}
		if len(dirs)+len(files) >= maxMatches {
			break
		}
	}
	// Directories first, since the next thing to do with one is to open it.
	return append(dirs, files...)
}

// search ranks the index against a query.
//
// What someone types is nearly always part of the file's own name, so a hit on
// the name outranks a hit anywhere in the path: typing "ui" should offer ui.go
// before every one of the forty files that happen to live in internal/ui.
// Subsequence matches come last, as the abbreviation case they are.
func (f *fileIndex) search(q string) []string {
	q = strings.ToLower(q)
	var name, inPath, fuzzy []string

	for _, p := range f.paths {
		lower := strings.ToLower(p)
		base := path.Base(strings.TrimSuffix(lower, "/"))
		switch {
		case strings.Contains(base, q):
			name = append(name, p)
		case strings.Contains(lower, q):
			inPath = append(inPath, p)
		case subsequence(lower, q):
			fuzzy = append(fuzzy, p)
		}
	}

	out := append(append(byDepth(name), byDepth(inPath)...), byDepth(fuzzy)...)
	if len(out) > maxMatches {
		out = out[:maxMatches]
	}
	return out
}

// byDepth puts the shallower path first among equals: a match at the top of the
// tree is more often the one meant than the same name buried six levels down.
func byDepth(paths []string) []string {
	sort.SliceStable(paths, func(i, j int) bool {
		di, dj := strings.Count(paths[i], "/"), strings.Count(paths[j], "/")
		if di != dj {
			return di < dj
		}
		return len(paths[i]) < len(paths[j])
	})
	return paths
}
