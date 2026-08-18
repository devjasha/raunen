package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// kinds is the shape of a diff as a string, which is what most of these tests
// actually care about: " " context, "+" added, "-" removed, "~" elided.
func kinds(lines []diffLine) string {
	var sb strings.Builder
	for _, l := range lines {
		switch l.kind {
		case lineContext:
			sb.WriteByte(' ')
		case lineAdd:
			sb.WriteByte('+')
		case lineDel:
			sb.WriteByte('-')
		case lineElide:
			sb.WriteByte('~')
		}
	}
	return sb.String()
}

func TestLineDiffKeepsCommonLinesAsContext(t *testing.T) {
	old := []string{"a", "b", "c"}
	new := []string{"a", "B", "c"}

	got := lineDiff(old, new)
	if want := " -+ "; kinds(got) != want {
		t.Fatalf("kinds = %q, want %q", kinds(got), want)
	}
	// A changed line reads as the removal then the addition.
	if got[1].text != "b" || got[2].text != "B" {
		t.Fatalf("expected -b then +B, got %q then %q", got[1].text, got[2].text)
	}
}

func TestLineDiffNumbersBothSides(t *testing.T) {
	got := lineDiff([]string{"a", "b"}, []string{"a", "x", "b"})
	if want := " + "; kinds(got) != want {
		t.Fatalf("kinds = %q, want %q", kinds(got), want)
	}
	// The inserted line has no position in the old file, and the context after
	// it has moved down by one on the new side.
	if got[1].oldNo != 0 || got[1].newNo != 2 {
		t.Fatalf("insert numbered old=%d new=%d, want old=0 new=2", got[1].oldNo, got[1].newNo)
	}
	if got[2].oldNo != 2 || got[2].newNo != 3 {
		t.Fatalf("trailing context old=%d new=%d, want old=2 new=3", got[2].oldNo, got[2].newNo)
	}
}

func TestLineDiffPureInsertAndDelete(t *testing.T) {
	if k := kinds(lineDiff(nil, []string{"a", "b"})); k != "++" {
		t.Fatalf("insert into empty = %q, want ++", k)
	}
	if k := kinds(lineDiff([]string{"a", "b"}, nil)); k != "--" {
		t.Fatalf("delete to empty = %q, want --", k)
	}
	if k := kinds(lineDiff([]string{"a"}, []string{"a"})); k != " " {
		t.Fatalf("identical = %q, want one context line", k)
	}
}

func TestFlatDiffFallbackForHugeInputs(t *testing.T) {
	// Past the guard the window must still describe the change rather than
	// building a table big enough to stall the frame.
	n := 600
	old := make([]string, n)
	new := make([]string, n)
	for i := range old {
		old[i] = fmt.Sprintf("old %d", i)
		new[i] = fmt.Sprintf("new %d", i)
	}
	if n*n <= maxDiffCells {
		t.Fatalf("test input too small to trip the guard")
	}
	got := lineDiff(old, new)
	if len(got) != 2*n {
		t.Fatalf("len = %d, want %d", len(got), 2*n)
	}
	if got[0].kind != lineDel || got[len(got)-1].kind != lineAdd {
		t.Fatalf("fallback should list removals then additions")
	}
}

func TestCollapseElidesRunsFarFromChanges(t *testing.T) {
	var lines []diffLine
	for i := 0; i < 20; i++ {
		lines = append(lines, diffLine{kind: lineContext, text: "ctx", oldNo: i + 1, newNo: i + 1})
	}
	lines = append(lines, diffLine{kind: lineAdd, text: "new", newNo: 21})

	got := collapse(lines, diffContext)
	// The two lines either side of the change survive; the other eighteen
	// become one marker that says how many were dropped.
	if want := "~  +"; kinds(got) != want {
		t.Fatalf("kinds = %q, want %q", kinds(got), want)
	}
	if got[0].n != 18 {
		t.Fatalf("elided %d lines, want 18", got[0].n)
	}
}

func TestCollapseKeepsEverythingWhenAllNearChanges(t *testing.T) {
	lines := []diffLine{
		{kind: lineContext, text: "a"},
		{kind: lineDel, text: "b"},
		{kind: lineAdd, text: "B"},
		{kind: lineContext, text: "c"},
	}
	if k := kinds(collapse(lines, diffContext)); k != " -+ " {
		t.Fatalf("kinds = %q, want %q", k, " -+ ")
	}
}

func TestCodeBlockRowsFitWidth(t *testing.T) {
	c := &codeBlock{
		path:     "internal/thing.go",
		label:    "+1 -1",
		indent:   "    ",
		numbered: true,
		lines: []diffLine{
			{kind: lineDel, text: strings.Repeat("x", 400), oldNo: 1},
			{kind: lineAdd, text: strings.Repeat("y", 400), newNo: 1},
		},
	}

	const width = 60
	rows := c.rows(width)
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for i, r := range rows {
		if w := ansi.StringWidth(r); w > width {
			t.Fatalf("row %d is %d cells wide, over the %d available:\n%s", i, w, width, r)
		}
		if !strings.HasPrefix(r, c.indent) {
			t.Fatalf("row %d is not indented under its tool call: %q", i, r)
		}
	}
}

func TestCodeBlockRowsRerenderAtNewWidth(t *testing.T) {
	// The whole reason the block is stored as data: a resize has to produce a
	// window that fits the new terminal, not the old one.
	c := &codeBlock{
		path:     "a.go",
		label:    "+1 -0",
		numbered: true,
		lines:    []diffLine{{kind: lineAdd, text: strings.Repeat("z", 200), newNo: 1}},
	}
	for _, w := range []int{40, 100, 30} {
		for _, r := range c.rows(w) {
			if got := ansi.StringWidth(r); got > w {
				t.Fatalf("at width %d a row came out %d wide", w, got)
			}
		}
	}
}

func TestCodeBlockCapsBodyAndSaysHowMuchIsLeft(t *testing.T) {
	var lines []diffLine
	for i := 0; i < maxBodyLines+10; i++ {
		lines = append(lines, diffLine{kind: lineAdd, text: fmt.Sprintf("line %d", i), newNo: i + 1})
	}
	c := &codeBlock{path: "big.go", label: "new", numbered: true, lines: lines}

	out := strings.Join(c.rows(80), "\n")
	if !strings.Contains(out, "more lines") {
		t.Fatalf("a truncated window must say what it left out:\n%s", out)
	}
}

func TestCodeLinesDropsTrailingNewline(t *testing.T) {
	// A trailing newline is not a line; counting it inflates every window's
	// addition count by one.
	if got := codeLines("a\nb\n"); len(got) != 2 {
		t.Fatalf("codeLines = %#v, want 2 lines", got)
	}
	if got := codeLines(""); got != nil {
		t.Fatalf("codeLines(\"\") = %#v, want nil", got)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCodeWindowNewFileIsAllAdditions(t *testing.T) {
	root := t.TempDir()
	args := mustJSON(t, map[string]string{"path": "new.go", "content": "package main\n\nfunc main() {}\n"})

	c := codeWindow(root, "write", args, "  ")
	if c == nil {
		t.Fatal("a write to a new file should draw a window")
	}
	for _, l := range c.lines {
		if l.kind != lineAdd {
			t.Fatalf("new file should be all additions, found %v (%q)", l.kind, l.text)
		}
	}
	if !strings.Contains(c.label, "new file") {
		t.Fatalf("label = %q, want it to mark the file as new", c.label)
	}
}

func TestCodeWindowWriteOverExistingFileDiffs(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := mustJSON(t, map[string]string{"path": "a.go", "content": "a\nB\nc\n"})

	c := codeWindow(root, "write", args, "  ")
	if c == nil {
		t.Fatal("expected a window")
	}
	if k := kinds(c.lines); k != " -+ " {
		t.Fatalf("kinds = %q, want %q — an overwrite should diff, not re-add the file", k, " -+ ")
	}
}

func TestCodeWindowEditNumbersFromPositionInFile(t *testing.T) {
	root := t.TempDir()
	body := "one\ntwo\nthree\nfour\nfive\n"
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	args := mustJSON(t, map[string]string{"path": "a.go", "old": "four", "new": "FOUR"})

	c := codeWindow(root, "edit", args, "  ")
	if c == nil {
		t.Fatal("expected a window")
	}
	if !c.numbered {
		t.Fatal("an edit located in the file should be numbered")
	}
	// The change is on the fourth line, so that is the number it must carry —
	// numbering the snippet from one would point at the wrong place.
	var del *diffLine
	for i := range c.lines {
		if c.lines[i].kind == lineDel {
			del = &c.lines[i]
			break
		}
	}
	if del == nil {
		t.Fatal("no removed line")
	}
	if del.oldNo != 4 {
		t.Fatalf("removed line numbered %d, want 4", del.oldNo)
	}
}

func TestCodeWindowEditWithMissingOldTextIsUnnumbered(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := mustJSON(t, map[string]string{"path": "a.go", "old": "nowhere", "new": "x"})

	c := codeWindow(root, "edit", args, "  ")
	if c == nil {
		t.Fatal("the intended change is still worth showing")
	}
	if c.numbered {
		t.Fatal("nothing was located, so there are no real line numbers to show")
	}
}

func TestCodeWindowIgnoresToolsWithNothingToShow(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"read", "bash", "list", "task"} {
		if c := codeWindow(root, name, `{"path":"a.go"}`, ""); c != nil {
			t.Fatalf("%s should not draw a code window", name)
		}
	}
}

func TestCodeWindowSurvivesBadArguments(t *testing.T) {
	root := t.TempDir()
	// A model mid-stream can produce half a JSON object; the transcript must
	// not panic on it.
	for _, args := range []string{"", "{", `{"path":""}`, `{"path":123}`, "null"} {
		if c := codeWindow(root, "write", args, ""); c != nil {
			t.Fatalf("expected no window for args %q", args)
		}
	}
}

func TestCodeWindowNoChangeDrawsNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := mustJSON(t, map[string]string{"path": "a.go", "content": "same\n"})
	if c := codeWindow(root, "write", args, ""); c != nil {
		t.Fatal("a write that changes nothing should not draw a window")
	}
}

// A code window carries no text of its own, which is exactly how a blank line
// is spotted elsewhere in the transcript. It must not be mistaken for one, or
// the spacing around it collapses and a reply after it loses its gap.
func TestCodeEntryIsNotABlankLine(t *testing.T) {
	e := entry{kind: kindWork, code: &codeBlock{
		path:  "a.go",
		lines: []diffLine{{kind: lineAdd, text: "x", newNo: 1}},
	}}
	if e.blankLine() {
		t.Fatal("a code window counted as blank space")
	}
	if (entry{}).blankLine() != true {
		t.Fatal("an genuinely empty entry should still count as blank")
	}
}

// lastKind decides the spacing between registers, so it has to see the window
// as the working-out it is rather than looking straight through it.
func TestLastKindSeesACodeWindow(t *testing.T) {
	m := testModel(t)
	m.push(entry{kind: kindWork, code: &codeBlock{
		path:  "a.go",
		lines: []diffLine{{kind: lineAdd, text: "x", newNo: 1}},
	}})
	if got := m.lastKind(); got != kindWork {
		t.Fatalf("lastKind = %v, want kindWork", got)
	}
}
