// Package skills finds SKILL.md files and turns them into named prompts.
//
// A skill is a piece of instruction saved under a name: a review checklist, a
// house style, the way commit messages are written here. Too long to retype,
// too situational to put in the system prompt where it would be paid for on
// every turn of every session.
//
// raunen already had these as strings in skills.json. A string in JSON is a
// poor place to keep a page of prose — no line breaks worth the name, no way to
// version one file at a time, and nothing to share. SKILL.md is a directory
// with a markdown file in it, which is all three.
//
// It is also the format other agents use, and they look in known places for it.
// Reading those directories means a repository that already has skills works
// here without being adapted, and a skill written here is not wasted on
// whatever gets used next. That is the same argument AGENTS.md won, and it wins
// again for the same reason: agreeing with the ecosystem costs nothing and
// being different costs everything.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Name is the file that carries a skill.
const Name = "SKILL.md"

const (
	// MaxBytes is how much of one skill is read. Past this it has stopped being
	// an instruction and become a manual, and it is charged to the context of
	// every turn that names it.
	MaxBytes = 32 << 10
	// maxDepth bounds how far below a root to look. Skills live one directory
	// down — skills/review/SKILL.md — and walking an entire tree looking for
	// markdown would find every vendored example in the repository.
	maxDepth = 3
)

// projectRoots are the directories under a project that hold skills, in the
// order they are searched.
//
// The list includes other agents' directories on purpose. A repository that
// has .claude/skills/ has already written down how it wants to be worked on,
// and ignoring that to insist on a directory of our own would be a way of
// making the user do the same work twice.
var projectRoots = []string{
	"skills",
	".raunen/skills",
	".agents/skills",
	".claude/skills",
	".codex/skills",
	".opencode/skills",
}

// Skill is one discovered skill.
type Skill struct {
	// Name is what it is referenced by, taken from the frontmatter or from the
	// directory it lives in.
	Name string
	// Description is the line shown while choosing. It is never sent to the
	// model: it describes the skill to the person picking it.
	Description string
	// Prompt is the body, which is what the model is given.
	Prompt string
	// Path is where it came from, for reporting a duplicate or a parse failure.
	Path string
	// Truncated reports that the body was longer than MaxBytes.
	Truncated bool
}

// Set is what was found, keyed by lowercased name for lookup.
type Set struct {
	byName map[string]Skill
	// Problems are the files that could not be read or parsed. Reported rather
	// than swallowed: a skill that silently did not load looks like a skill the
	// model ignored.
	Problems []string
}

// Load finds every skill under the project root and the user's own directory.
//
// Project skills win over user skills of the same name. A repository saying how
// its own commits are written is more specific than a global preference, and
// the specific thing should win — the same rule AGENTS.md follows.
func Load(root, userDir string) *Set {
	s := &Set{byName: map[string]Skill{}}

	// User first, so a project skill of the same name overwrites it.
	if userDir != "" {
		s.scanRoot(userDir)
	}
	for _, rel := range projectRoots {
		s.scanRoot(filepath.Join(root, rel))
	}
	return s
}

// scanRoot walks one skills directory.
func (s *Set) scanRoot(dir string) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return
	}
	s.walk(dir, dir, 0)
}

// walk looks for SKILL.md, descending a bounded number of levels.
func (s *Set) walk(dir, root string, depth int) {
	if depth > maxDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		switch {
		case e.IsDir():
			// A dotted directory inside a skills root is housekeeping.
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			s.walk(filepath.Join(dir, e.Name()), root, depth+1)
		case e.Name() == Name:
			s.read(filepath.Join(dir, e.Name()))
		}
	}
}

// read parses one SKILL.md and files it under its name.
func (s *Set) read(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		s.Problems = append(s.Problems, fmt.Sprintf("%s: %v", path, err))
		return
	}

	sk := parse(string(b))
	sk.Path = path
	if sk.Name == "" {
		// No name in the frontmatter: the directory names it, which is what
		// makes frontmatter optional rather than required.
		sk.Name = filepath.Base(filepath.Dir(path))
	}
	if sk.Name == "" || strings.ContainsAny(sk.Name, " \t\n") {
		// A skill is referenced as one word in a prompt, so a name with a space
		// in it could never be typed.
		s.Problems = append(s.Problems, path+": unusable name "+sk.Name)
		return
	}
	if strings.TrimSpace(sk.Prompt) == "" {
		s.Problems = append(s.Problems, path+": no instructions under the frontmatter")
		return
	}
	if len(sk.Prompt) > MaxBytes {
		cut := sk.Prompt[:MaxBytes]
		if i := strings.LastIndexByte(cut, '\n'); i > MaxBytes/2 {
			cut = cut[:i]
		}
		sk.Prompt = cut
		sk.Truncated = true
	}
	s.byName[strings.ToLower(sk.Name)] = sk
}

// parse splits optional YAML frontmatter from the body.
//
// Only name and description are read. Other agents put their own keys in there,
// and a skill carrying metadata we do not understand is still a perfectly good
// skill — so unknown keys are ignored rather than rejected, which is what lets
// one file serve several tools.
//
// This is not a YAML parser and does not pretend to be. Frontmatter here is a
// handful of scalar keys; pulling in a parser to read two of them would be a
// dependency for the sake of it.
func parse(src string) Skill {
	var sk Skill
	rest := src

	if front, body, ok := splitFrontmatter(src); ok {
		rest = body
		for _, line := range strings.Split(front, "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			// A quoted scalar is the common case for a description containing
			// a colon, which is most of them.
			value = strings.Trim(value, `"'`)
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "name":
				sk.Name = value
			case "description":
				sk.Description = value
			}
		}
	}

	sk.Prompt = strings.TrimSpace(rest)
	if sk.Description == "" {
		sk.Description = firstLine(sk.Prompt)
	}
	return sk
}

// splitFrontmatter separates a leading --- block from the body.
func splitFrontmatter(src string) (front, body string, ok bool) {
	s := strings.TrimLeft(src, "\ufeff \t\r\n")
	if !strings.HasPrefix(s, "---") {
		return "", src, false
	}
	// Past the opening marker and its newline.
	rest := s[3:]
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	} else {
		return "", src, false
	}
	// The closing marker starts a line.
	for _, end := range []string{"\n---\n", "\n---\r\n"} {
		if i := strings.Index(rest, end); i >= 0 {
			return rest[:i], rest[i+len(end):], true
		}
	}
	if strings.HasPrefix(rest, "---\n") {
		// An empty frontmatter block.
		return "", rest[4:], true
	}
	// Unterminated: treat the whole thing as body rather than swallowing it.
	return "", src, false
}

// firstLine is the fallback description: a skill with no description still has
// to say something about itself, and its own opening words do it better than
// nothing. A markdown heading is stripped, since "# Review checklist" describes
// the skill but the hash does not.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line != "" {
			if len(line) > 72 {
				line = line[:69] + "…"
			}
			return line
		}
	}
	return ""
}

// Get looks a skill up by name, case-insensitively: the name is typed
// mid-sentence, where a capital at the start is a typing habit rather than a
// different skill.
func (s *Set) Get(name string) (Skill, bool) {
	if s == nil {
		return Skill{}, false
	}
	sk, ok := s.byName[strings.ToLower(name)]
	return sk, ok
}

// Names lists the discovered skills in a stable order, so a list offered while
// typing does not reshuffle between one look and the next.
func (s *Set) Names() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.byName))
	for _, sk := range s.byName {
		out = append(out, sk.Name)
	}
	sort.Strings(out)
	return out
}

// Len reports how many were found.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.byName)
}

// UserDir is where skills installed for the user live, beside the config.
func UserDir(configDir string) string {
	if configDir == "" {
		return ""
	}
	return filepath.Join(configDir, "skills")
}
