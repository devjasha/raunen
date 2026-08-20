// Package fileset lists the files that make up a project.
//
// "The project" is what git says it is: everything tracked, plus everything
// untracked that is not ignored. That is the right list by definition, and it
// honours .gitignore for free — otherwise a parser's worth of work to
// approximate badly. Outside a repository it falls back to walking the tree
// minus the directories that are never the answer.
//
// It exists as its own package because two very different callers need the same
// list: @ completion in the UI, and the search tools the model calls. They had
// no business sharing a package, and duplicating the walk would have meant the
// two disagreeing about what counts as a file the moment one of them was fixed.
package fileset

import (
	"context"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
)

// SkipDirs are directories never worth listing. They are where the bulk of a
// tree usually is, and none of it is what anyone means by "the project".
//
// git already excludes most of these through .gitignore; this catches the ones
// a repository forgot to ignore, and carries the fallback walk on its own.
var SkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "target": true,
	"dist": true, "build": true, ".next": true, ".venv": true,
	"__pycache__": true, ".cache": true, ".idea": true, "venv": true,
}

// Max is the default ceiling on how many paths are returned. A tree larger than
// this is one where an exhaustive list was never the right tool, and the cap
// keeps a stray scan of a home directory from costing hundreds of megabytes.
const Max = 20000

// List returns the project's files as slash-separated paths relative to root,
// in the order git reports them, capped at max.
//
// The second return says whether git answered, which separates "not a
// repository" from "a repository with nothing in it" — the caller usually does
// not care, but the fallback does.
func List(ctx context.Context, root string, max int) ([]string, bool) {
	if max <= 0 {
		max = Max
	}
	if files, ok := gitFiles(ctx, root, max); ok {
		return files, true
	}
	return walkFiles(root, max), false
}

// gitFiles asks git for the files it knows about.
func gitFiles(ctx context.Context, root string, max int) ([]string, bool) {
	cmd := exec.CommandContext(ctx, "git", "ls-files",
		"--cached", "--others", "--exclude-standard", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}

	var files []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		if len(files) >= max {
			break
		}
		if Skipped(p) {
			continue
		}
		files = append(files, p)
	}
	return files, true
}

// walkFiles is the fallback outside a repository: the same tree, minus the
// directories that are never the answer.
func walkFiles(root string, max int) []string {
	var files []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory is not a reason to abandon the rest.
			return nil
		}
		if len(files) >= max {
			return fs.SkipAll
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if SkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files
}

// Skipped reports whether a path lies under a directory not worth listing. git
// honours .gitignore but knows nothing about which directories are noise.
func Skipped(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if SkipDirs[part] {
			return true
		}
	}
	return false
}
