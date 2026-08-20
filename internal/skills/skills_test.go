package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write creates a SKILL.md at dir/name/SKILL.md.
func write(t *testing.T, root, dir, body string) string {
	t.Helper()
	p := filepath.Join(root, dir, Name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestReadsFrontmatter is the base case: name, description and body, each where
// it belongs.
func TestReadsFrontmatter(t *testing.T) {
	root := t.TempDir()
	write(t, root, "skills/review", `---
name: review
description: Review checklist
---

Check for data races.
`)

	s := Load(root, "")
	sk, ok := s.Get("review")
	if !ok {
		t.Fatalf("skill not found; problems: %v", s.Problems)
	}
	if sk.Description != "Review checklist" {
		t.Errorf("description = %q", sk.Description)
	}
	if sk.Prompt != "Check for data races." {
		t.Errorf("prompt = %q, want the body without the frontmatter", sk.Prompt)
	}
}

// TestFrontmatterIsOptional keeps the simplest possible skill working: a
// markdown file in a named directory.
func TestFrontmatterIsOptional(t *testing.T) {
	root := t.TempDir()
	write(t, root, "skills/plain", "# Plain\n\nJust instructions.\n")

	sk, ok := Load(root, "").Get("plain")
	if !ok {
		t.Fatal("a skill without frontmatter was not found")
	}
	if sk.Name != "plain" {
		t.Errorf("name = %q, want the directory name", sk.Name)
	}
	// The description falls back to the first line, heading marker stripped.
	if sk.Description != "Plain" {
		t.Errorf("description = %q, want the first line", sk.Description)
	}
}

// TestUnknownFrontmatterKeysAreIgnored is what lets one file serve several
// tools: other agents put their own keys in there, and a skill carrying
// metadata we do not understand is still a perfectly good skill.
func TestUnknownFrontmatterKeysAreIgnored(t *testing.T) {
	root := t.TempDir()
	write(t, root, "skills/x", `---
name: x
description: Fine
allowed-tools: read, grep
model: some/model
license: MIT
---
Body.
`)

	s := Load(root, "")
	if len(s.Problems) > 0 {
		t.Errorf("unknown keys were reported as problems: %v", s.Problems)
	}
	sk, ok := s.Get("x")
	if !ok {
		t.Fatal("skill with extra keys was rejected")
	}
	if sk.Prompt != "Body." {
		t.Errorf("prompt = %q", sk.Prompt)
	}
}

// TestQuotedDescription covers the common case of a description containing a
// colon, which has to be quoted in YAML and unquoted here.
func TestQuotedDescription(t *testing.T) {
	root := t.TempDir()
	write(t, root, "skills/c", `---
name: c
description: "House style: imperative, lower case"
---
Body.
`)

	sk, _ := Load(root, "").Get("c")
	if sk.Description != "House style: imperative, lower case" {
		t.Errorf("description = %q, want the quotes stripped and the colon kept", sk.Description)
	}
}

// TestOtherAgentsDirectories is the ecosystem argument: a repository that has
// already written its skills down should work without being adapted.
func TestOtherAgentsDirectories(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		".claude/skills/fromclaude",
		".codex/skills/fromcodex",
		".agents/skills/fromagents",
		".opencode/skills/fromopencode",
		".raunen/skills/fromraunen",
	} {
		write(t, root, dir, "Body for "+filepath.Base(dir)+".\n")
	}

	s := Load(root, "")
	for _, want := range []string{"fromclaude", "fromcodex", "fromagents", "fromopencode", "fromraunen"} {
		if _, ok := s.Get(want); !ok {
			t.Errorf("did not find %q; found %v", want, s.Names())
		}
	}
}

// TestProjectBeatsUser: a repository saying how its own commits are written is
// more specific than a global preference, and the specific thing wins.
func TestProjectBeatsUser(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	write(t, user, "commit", "Global commit style.\n")
	write(t, root, "skills/commit", "Project commit style.\n")

	sk, ok := Load(root, user).Get("commit")
	if !ok {
		t.Fatal("skill not found")
	}
	if !strings.Contains(sk.Prompt, "Project") {
		t.Errorf("prompt = %q, want the project's version to win", sk.Prompt)
	}
}

// TestUserSkillsLoadOnTheirOwn covers a global skill with no project equivalent.
func TestUserSkillsLoadOnTheirOwn(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	write(t, user, "brief", "Answer briefly.\n")

	if _, ok := Load(root, user).Get("brief"); !ok {
		t.Error("a user skill was not found")
	}
}

// TestLookupIsCaseInsensitive because the name is typed mid-sentence, where a
// capital is a typing habit rather than a different skill.
func TestLookupIsCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	write(t, root, "skills/review", "Body.\n")

	for _, name := range []string{"review", "Review", "REVIEW"} {
		if _, ok := Load(root, "").Get(name); !ok {
			t.Errorf("%q did not resolve", name)
		}
	}
}

// TestEmptySkillIsReported treats a file someone meant to fill in as a problem
// rather than as a skill that silently contributes nothing.
func TestEmptySkillIsReported(t *testing.T) {
	root := t.TempDir()
	write(t, root, "skills/empty", `---
name: empty
description: nothing here
---
`)

	s := Load(root, "")
	if _, ok := s.Get("empty"); ok {
		t.Error("a skill with no body was loaded")
	}
	if len(s.Problems) == 0 {
		t.Error("an empty skill was skipped without being reported")
	}
}

// TestOversizedSkillIsTruncated bounds what one skill costs. It is charged to
// the context of every turn that names it.
func TestOversizedSkillIsTruncated(t *testing.T) {
	root := t.TempDir()
	write(t, root, "skills/big", strings.Repeat("a line of instruction\n", MaxBytes/10))

	sk, ok := Load(root, "").Get("big")
	if !ok {
		t.Fatal("oversized skill was dropped entirely")
	}
	if !sk.Truncated {
		t.Error("oversized skill was not marked truncated")
	}
	if len(sk.Prompt) > MaxBytes {
		t.Errorf("kept %d bytes, want at most %d", len(sk.Prompt), MaxBytes)
	}
}

// TestUnterminatedFrontmatterIsNotSwallowed: a file whose --- is never closed
// should keep its content as the body rather than losing all of it.
func TestUnterminatedFrontmatterIsNotSwallowed(t *testing.T) {
	root := t.TempDir()
	write(t, root, "skills/odd", "---\nname: odd\nThis was never closed.\n")

	sk, ok := Load(root, "").Get("odd")
	if !ok {
		t.Fatalf("skill was dropped; problems: %v", Load(root, "").Problems)
	}
	if !strings.Contains(sk.Prompt, "never closed") {
		t.Errorf("prompt = %q, want the content kept", sk.Prompt)
	}
}

// TestNoSkillsIsSilent keeps the common case free of noise.
func TestNoSkillsIsSilent(t *testing.T) {
	s := Load(t.TempDir(), "")
	if s.Len() != 0 {
		t.Errorf("found %d skills in an empty tree", s.Len())
	}
	if len(s.Problems) != 0 {
		t.Errorf("reported problems for an empty tree: %v", s.Problems)
	}
}

// TestDeepNestingIsBounded stops a stray skills directory in a large tree from
// turning discovery into a full walk.
func TestDeepNestingIsBounded(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join("skills", "a", "b", "c", "d", "e", "f")
	write(t, root, deep, "Too deep.\n")

	if s := Load(root, ""); s.Len() != 0 {
		t.Errorf("walked past the depth limit and found %v", s.Names())
	}
}

// TestNamesAreStable so a list offered while typing does not reshuffle between
// one look and the next.
func TestNamesAreStable(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"c", "a", "b"} {
		write(t, root, "skills/"+n, "Body.\n")
	}

	first := Load(root, "").Names()
	for i := 0; i < 10; i++ {
		if got := Load(root, "").Names(); strings.Join(got, ",") != strings.Join(first, ",") {
			t.Fatalf("order changed: %v then %v", first, got)
		}
	}
	if strings.Join(first, ",") != "a,b,c" {
		t.Errorf("names = %v, want sorted", first)
	}
}

// TestNilSetIsUsable, since a caller that skipped discovery should not have to
// guard every call.
func TestNilSetIsUsable(t *testing.T) {
	var s *Set
	if _, ok := s.Get("x"); ok {
		t.Error("nil set returned a skill")
	}
	if s.Len() != 0 || s.Names() != nil {
		t.Error("nil set is not empty")
	}
}
