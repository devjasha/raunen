package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"raunen/internal/fileset"
)

// Searching is a tool rather than something the model shells out for.
//
// It could already do this: `bash` has grep and rg on its allowlist, and models
// reach for them readily. Three things are wrong with that.
//
// It is not portable. `rg` is not installed everywhere, BSD and GNU grep
// disagree on flags, and the model cannot tell which it is talking to until a
// command fails. Every session spent tokens rediscovering that.
//
// It is not gated. `bash` is mutating unless a command matches an allowlist, so
// a search with a pipe or a redirect in it needs approval in accept mode and is
// refused outright in plan mode — which is the mode where investigation is the
// only thing left to do.
//
// And it is not bounded. `grep -r` walks node_modules and .git, then hands back
// a megabyte of minified JavaScript that gets truncated to the first 30 KB. The
// answer is in there somewhere.
//
// So: two tools that always exist, are always read-only, search what git thinks
// the project is, and return results sized to be read rather than truncated.

const (
	// maxMatches bounds one search. A query matching more than this is one that
	// needs narrowing, and saying so is more useful than a wall of results the
	// model will only skim.
	maxMatches = 200
	// maxPerFile stops a single generated file from filling the whole result.
	maxPerFile = 20
	// maxLineBytes truncates a very long line. A minified bundle is one line of
	// half a megabyte, and printing it says nothing about why it matched.
	maxLineBytes = 400
	// maxScanBytes skips files larger than this. Past it a file is a binary, a
	// bundle or a fixture, and none of those are what a search is looking for.
	maxScanBytes = 2 << 20
)

// addSearch registers the search tools against a root directory.
func addSearch(r *Registry, root string, truncate func(string) string) {
	r.Add(Tool{
		Name: "grep",
		Description: "Search the project for a regular expression, returning " +
			"file:line: and the matching line. Skips anything git ignores. Use " +
			"this rather than grep or rg through bash.",
		Params: obj(map[string]any{
			"pattern": str("Regular expression, RE2 syntax."),
			"path":    str("Limit to this directory or file."),
			"glob":    str("Limit to paths matching this glob, e.g. *.go"),
			"ignore_case": map[string]any{
				"type": "boolean", "description": "Match case-insensitively.",
			},
			"files_only": map[string]any{
				"type":        "boolean",
				"description": "Return only the file names. Much cheaper.",
			},
			"context": map[string]any{
				"type":        "integer",
				"description": "Lines of context around each match, 0-5.",
			},
		}, "pattern"),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Pattern    string `json:"pattern"`
				Path       string `json:"path"`
				Glob       string `json:"glob"`
				IgnoreCase bool   `json:"ignore_case"`
				FilesOnly  bool   `json:"files_only"`
				Context    int    `json:"context"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Pattern) == "" {
				return "", fmt.Errorf("pattern is required")
			}
			re, err := compile(a.Pattern, a.IgnoreCase)
			if err != nil {
				return "", err
			}
			files, err := candidates(ctx, root, a.Path, a.Glob)
			if err != nil {
				return "", err
			}
			return truncate(grepFiles(root, files, re, a.FilesOnly, clampContext(a.Context))), nil
		},
	})

	r.Add(Tool{
		Name: "glob",
		Description: "Find the project's files by name, e.g. **/*_test.go. " +
			"Skips anything git ignores.",
		Params: obj(map[string]any{
			"pattern": str("Glob, e.g. *.go or internal/**/*.json. ** spans directories."),
			"path":    str("Limit to this directory."),
		}, "pattern"),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Pattern) == "" {
				return "", fmt.Errorf("pattern is required")
			}
			files, err := candidates(ctx, root, a.Path, a.Pattern)
			if err != nil {
				return "", err
			}
			if len(files) == 0 {
				return fmt.Sprintf("no files match %q", a.Pattern), nil
			}
			var sb strings.Builder
			for i, f := range files {
				if i >= maxMatches {
					fmt.Fprintf(&sb, "... and %d more\n", len(files)-i)
					break
				}
				sb.WriteString(f + "\n")
			}
			return truncate(sb.String()), nil
		},
	})
}

// clampContext bounds the context lines. Five either side is already fifteen
// lines a match, and a model asking for fifty has misunderstood the tool.
func clampContext(n int) int {
	return min(max(n, 0), 5)
}

// compile builds the pattern, reporting a bad regexp in terms the model can act
// on rather than as a raw parser error.
func compile(pattern string, fold bool) (*regexp.Regexp, error) {
	if fold {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern %q: %w — this is RE2 syntax, "+
			"which has no backreferences or lookaround", pattern, err)
	}
	return re, nil
}

// candidates lists the project files to search, narrowed by an optional
// subdirectory and an optional glob.
func candidates(ctx context.Context, root, sub, glob string) ([]string, error) {
	files, _ := fileset.List(ctx, root, 0)

	// A path that names one file is a search of that file, which is worth
	// supporting: it is how a model checks one thing it already has in hand.
	if sub != "" {
		clean := path.Clean(filepath.ToSlash(sub))
		if info, err := os.Stat(filepath.Join(root, clean)); err == nil && !info.IsDir() {
			files = []string{clean}
		} else {
			prefix := clean + "/"
			var kept []string
			for _, f := range files {
				if strings.HasPrefix(f, prefix) {
					kept = append(kept, f)
				}
			}
			if len(kept) == 0 {
				if err != nil {
					return nil, fmt.Errorf("no such path: %s", sub)
				}
				return nil, nil
			}
			files = kept
		}
	}

	if glob == "" {
		return files, nil
	}
	var kept []string
	for _, f := range files {
		ok, err := matchGlob(glob, f)
		if err != nil {
			return nil, err
		}
		if ok {
			kept = append(kept, f)
		}
	}
	return kept, nil
}

// matchGlob matches a path against a glob, supporting ** for "any number of
// directories" — which path.Match does not, and which is the form a model
// reaches for first.
//
// A pattern with no slash in it matches against the base name, so "*.go" finds
// every Go file in the tree rather than only those at the top. That is what
// anyone means by it, and requiring "**/*.go" to express it would be a trap.
func matchGlob(pattern, name string) (bool, error) {
	pattern = filepath.ToSlash(pattern)
	if !strings.Contains(pattern, "/") {
		ok, err := path.Match(pattern, path.Base(name))
		if err != nil {
			return false, fmt.Errorf("invalid glob %q: %w", pattern, err)
		}
		return ok, nil
	}
	if !strings.Contains(pattern, "**") {
		ok, err := path.Match(pattern, name)
		if err != nil {
			return false, fmt.Errorf("invalid glob %q: %w", pattern, err)
		}
		return ok, nil
	}
	re, err := globRegexp(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(name), nil
}

// globRegexp translates a glob containing ** into a regexp. Only the constructs
// path.Match knows are supported, plus **; character classes are passed through
// to path.Match's semantics by escaping everything else.
func globRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				// "a/**/b" should also match "a/b": the doubled star means
				// "any number of directories", and zero is a number. So the
				// separator that follows it is swallowed into the optional part.
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
				continue
			}
			// A single star stops at a separator, as in every other glob.
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("invalid glob %q: %w", pattern, err)
	}
	return re, nil
}

// grepFiles searches each file and formats what it found.
func grepFiles(root string, files []string, re *regexp.Regexp, namesOnly bool, around int) string {
	var (
		sb      strings.Builder
		total   int
		hitFile int
		capped  bool
	)

	for _, rel := range files {
		if total >= maxMatches {
			capped = true
			break
		}
		res := searchFile(filepath.Join(root, rel), re, namesOnly, around)
		if res.matches == 0 {
			continue
		}
		hitFile++
		if namesOnly {
			sb.WriteString(rel + "\n")
			continue
		}
		for _, l := range res.lines {
			sb.WriteString(rel + l)
		}
		total += res.matches
		if res.dropped > 0 {
			fmt.Fprintf(&sb, "%s: ... and %d more %s in this file\n",
				rel, res.dropped, plural(res.dropped, "match", "matches"))
		}
	}

	if hitFile == 0 {
		return "no matches"
	}
	if capped {
		fmt.Fprintf(&sb, "... stopped at %d matches — narrow the search with path or glob\n", maxMatches)
	}
	// A count at the end, because the model otherwise has to work out whether it
	// saw everything by counting lines itself. Context lines are not matches, so
	// they are deliberately not in this figure.
	if namesOnly {
		fmt.Fprintf(&sb, "[%d %s]\n", hitFile, plural(hitFile, "file", "files"))
	} else {
		fmt.Fprintf(&sb, "[%d %s in %d %s]\n",
			total, plural(total, "match", "matches"),
			hitFile, plural(hitFile, "file", "files"))
	}
	return sb.String()
}

// plural picks the right word for a count, so a result does not read
// "1 matches in 1 files".
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// result is what one file yielded: the formatted lines, how many of them were
// real matches rather than context, and how many matches were dropped for
// exceeding the per-file cap.
type result struct {
	lines   []string
	matches int
	dropped int
}

// searchFile returns the matches in one file.
func searchFile(abs string, re *regexp.Regexp, namesOnly bool, around int) result {
	var res result

	info, err := os.Stat(abs)
	if err != nil || info.IsDir() || info.Size() > maxScanBytes {
		return res
	}
	f, err := os.Open(abs)
	if err != nil {
		return res
	}
	defer f.Close()

	// Binary files match noise and print worse. Sniffing the first block is what
	// grep does, and it costs one read.
	var head [512]byte
	n, _ := f.Read(head[:])
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return res
	}
	if _, err := f.Seek(0, 0); err != nil {
		return res
	}

	var (
		lineNo  int
		recent  []string
		pending int
	)
	sc := bufio.NewScanner(f)
	// Long lines are the norm in generated files; the default 64 KB would stop
	// the scan and silently return nothing for the rest of the file.
	sc.Buffer(make([]byte, 0, 64<<10), maxScanBytes)

	for sc.Scan() {
		lineNo++
		line := sc.Text()

		if !re.MatchString(line) {
			// Trailing context from a previous match.
			if pending > 0 {
				res.lines = append(res.lines, fmt.Sprintf("-%d- %s\n", lineNo, clip(line)))
				pending--
			} else if around > 0 {
				recent = append(recent, fmt.Sprintf("-%d- %s\n", lineNo, clip(line)))
				if len(recent) > around {
					recent = recent[1:]
				}
			}
			continue
		}

		res.matches++
		if namesOnly {
			// One is enough to know the file is worth opening.
			return res
		}
		if res.matches > maxPerFile {
			res.dropped++
			res.matches--
			continue
		}
		res.lines = append(res.lines, recent...)
		recent = recent[:0]
		res.lines = append(res.lines, fmt.Sprintf(":%d: %s\n", lineNo, clip(line)))
		pending = around
	}
	return res
}

// clip shortens a line that is too long to be worth printing whole, keeping the
// cut on a rune boundary so the result stays valid UTF-8.
func clip(s string) string {
	if len(s) <= maxLineBytes {
		return s
	}
	cut := s[:maxLineBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + " …[line truncated]"
}
