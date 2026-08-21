package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePNG(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Writing a path into the message attaches it, with no command needed. The text
// is left exactly as typed: the sentence around the path is what says what to
// do with the picture.
func TestImagePathInAMessageIsAttached(t *testing.T) {
	m := testModel(t)
	writePNG(t, m.root, "shot.png")

	imgs := m.detectImagePaths("take a look at shot.png and tell me why")
	if len(imgs) != 1 {
		t.Fatalf("got %d images, want the one named in the message", len(imgs))
	}
	if imgs[0].Name != "shot.png" || imgs[0].MIME != "image/png" {
		t.Errorf("attached %+v", imgs[0])
	}
}

// Prose puts punctuation against a path. "look at shot.png," is the same file.
func TestImagePathSurvivesPunctuation(t *testing.T) {
	m := testModel(t)
	writePNG(t, m.root, "shot.png")

	for _, line := range []string{"see shot.png,", "(shot.png)", "`shot.png`", "why is shot.png wrong?"} {
		if got := m.detectImagePaths(line); len(got) != 1 {
			t.Errorf("detectImagePaths(%q) found %d, want 1", line, len(got))
		}
	}
}

// The same picture mentioned twice is one attachment, not two: the second copy
// costs tokens and adds nothing.
func TestRepeatedPathAttachesOnce(t *testing.T) {
	m := testModel(t)
	writePNG(t, m.root, "shot.png")

	if got := m.detectImagePaths("shot.png versus shot.png"); len(got) != 1 {
		t.Errorf("got %d images, want one", len(got))
	}
}

// A path that is not there, or is not an image, must not attach anything — and
// must not stop the message being sent.
func TestOnlyRealImagesAreAttached(t *testing.T) {
	m := testModel(t)
	if err := os.WriteFile(filepath.Join(m.root, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"look at missing.png", "read main.go", "what about png files"} {
		if got := m.detectImagePaths(line); len(got) != 0 {
			t.Errorf("detectImagePaths(%q) attached %d, want none", line, len(got))
		}
	}
}

func TestImageCommandStagesForTheNextMessage(t *testing.T) {
	m := testModel(t)
	writePNG(t, m.root, "diagram.png")

	if _, cmd := m.command("/image diagram.png"); cmd != nil {
		t.Error("attaching should not need a command to run")
	}
	if len(m.attached) != 1 {
		t.Fatalf("staged %d images, want 1", len(m.attached))
	}

	// Taken by the next message, and by that one only: an image left staged
	// would silently ride along with every question after it.
	if got := m.takeAttached(); len(got) != 1 {
		t.Errorf("takeAttached gave %d", len(got))
	}
	if len(m.attached) != 0 {
		t.Errorf("%d images still staged after sending", len(m.attached))
	}
	if got := m.takeAttached(); got != nil {
		t.Errorf("takeAttached gave %v on an empty set, want nil", got)
	}
}

// A path with spaces is what a screenshot is actually called on a Mac.
func TestImageCommandTakesPathsWithSpaces(t *testing.T) {
	m := testModel(t)
	writePNG(t, m.root, "Screenshot 2024 at 10.00.png")

	m.command("/image Screenshot 2024 at 10.00.png")
	if len(m.attached) != 1 {
		t.Fatalf("staged %d, want the file with spaces in its name", len(m.attached))
	}
}

func TestImageCommandReportsABadPath(t *testing.T) {
	m := testModel(t)
	m.command("/image nope.png")
	if len(m.attached) != 0 {
		t.Errorf("a failed load should stage nothing")
	}
	if !strings.Contains(lastLine(&m), "✗") {
		t.Errorf("last line = %q, want the failure said out loud", lastLine(&m))
	}
}

func TestImagesClearDropsThem(t *testing.T) {
	m := testModel(t)
	writePNG(t, m.root, "a.png")
	m.command("/image a.png")
	m.command("/images clear")
	if len(m.attached) != 0 {
		t.Errorf("%d images survived /images clear", len(m.attached))
	}
}

// Staged attachments are named above the input, for the same reason a pending
// reply is: what the next message will carry should be visible while typing it.
func TestStagedImagesAreShownAboveTheInput(t *testing.T) {
	m := testModel(t)
	writePNG(t, m.root, "mock.png")
	m.command("/image mock.png")

	if !strings.Contains(m.View().Content, "mock.png") {
		t.Error("a staged image should be named above the input")
	}
}

func lastLine(m *Model) string {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(m.entries[i].text); t != "" {
			return t
		}
	}
	return ""
}

// Dragging a file onto a terminal inserts its path as a bracketed paste, in
// whatever shape that terminal favours. All of these are one dropped image.
func TestDroppedPathShapesAreRecognised(t *testing.T) {
	m := testModel(t)
	writePNG(t, m.root, "shot.png")
	writePNG(t, m.root, "a b.png")
	abs := filepath.Join(m.root, "shot.png")
	spaced := filepath.Join(m.root, "a b.png")

	for _, tc := range []struct{ name, paste string }{
		{"bare absolute path", abs},
		{"relative path", "shot.png"},
		{"trailing space, as iTerm sends", abs + " "},
		{"single quoted", "'" + abs + "'"},
		{"double quoted", `"` + abs + `"`},
		{"backslash-escaped space", strings.ReplaceAll(spaced, " ", `\ `)},
		{"quoted with a space", `"` + spaced + `"`},
		{"file URL", "file://" + abs},
		{"percent-encoded file URL", "file://" + strings.ReplaceAll(spaced, " ", "%20")},
	} {
		mm := testModel(t)
		mm.root = m.root
		if !mm.dropped(tc.paste) {
			t.Errorf("%s: %q was not taken as a drop", tc.name, tc.paste)
			continue
		}
		if len(mm.attached) != 1 {
			t.Errorf("%s: staged %d images, want 1", tc.name, len(mm.attached))
		}
	}
}

// Several files dragged at once arrive space-separated.
func TestDroppingSeveralFilesStagesThemAll(t *testing.T) {
	m := testModel(t)
	a := writePNG(t, m.root, "one.png")
	b := writePNG(t, m.root, "two.png")

	if !m.dropped(a + " " + b) {
		t.Fatal("two paths should be taken as a drop")
	}
	if len(m.attached) != 2 {
		t.Errorf("staged %d, want both", len(m.attached))
	}
}

// The important half: pasting text must never be hijacked. Anything that is
// not purely paths to images that exist has to reach the input untouched.
func TestOrdinaryPastesAreNotTreatedAsDrops(t *testing.T) {
	m := testModel(t)
	writePNG(t, m.root, "shot.png")

	for _, tc := range []struct{ name, paste string }{
		{"prose", "here is a sentence"},
		{"prose mentioning a real image", "have a look at shot.png please"},
		{"a path that does not exist", "/tmp/definitely-not-here.png"},
		{"a non-image file", "main.go"},
		{"an image path inside prose", "why does shot.png look wrong"},
		{"multiline text", "line one\nline two"},
		{"a stack trace", "at foo.png:12\n  at bar"},
		{"empty", ""},
		{"an unterminated quote", `"shot.png`},
		{"a url that is not a file", "https://example.com/a.png"},
	} {
		mm := testModel(t)
		mm.root = m.root
		if mm.dropped(tc.paste) {
			t.Errorf("%s: %q was wrongly swallowed as a drop", tc.name, tc.paste)
		}
		if len(mm.attached) != 0 {
			t.Errorf("%s: staged %d images, want none", tc.name, len(mm.attached))
		}
	}
}

// A mixed drop is not a drop: one of the two is not an image, so the whole
// paste is text and belongs in the input.
func TestMixedDropIsLeftAsText(t *testing.T) {
	m := testModel(t)
	img := writePNG(t, m.root, "shot.png")
	other := filepath.Join(m.root, "notes.txt")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m.dropped(img + " " + other) {
		t.Error("a drop containing a non-image should be left as text")
	}
}

// A dropped file that exists but cannot be read as an image counts as handled —
// the failure is reported, and the raw path must not then appear in the input.
func TestDroppedUnreadableImageIsReportedNotTyped(t *testing.T) {
	m := testModel(t)
	p := filepath.Join(m.root, "broken.png")
	if err := os.WriteFile(p, []byte("this is not a png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !m.dropped(p) {
		t.Error("a dropped image file should be handled even when it fails to load")
	}
	if len(m.attached) != 0 {
		t.Errorf("staged %d, want none", len(m.attached))
	}
	if !strings.Contains(lastLine(&m), "✗") {
		t.Errorf("last line = %q, want the failure said out loud", lastLine(&m))
	}
}
