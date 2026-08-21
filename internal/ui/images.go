package ui

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"raunen/internal/attach"
	"raunen/internal/provider"
)

// imageMark prefixes an attachment wherever one is shown, so a picture reads as
// a picture in a transcript that is otherwise all text.
const imageMark = "▣"

// pastedMsg carries the result of reading the clipboard.
type pastedMsg struct {
	img provider.Image
	err error
}

// attachImage loads a path and holds it for the next message.
//
// Failures are reported and survived: a typo in a path should cost the line it
// was typed on, not the attachments already staged behind it.
func (m *Model) attachImage(path string) {
	img, err := attach.Load(expandPath(path, m.root))
	if err != nil {
		m.add(errStyle.Render("✗ " + err.Error()))
		return
	}
	m.stage(img)
}

// stage adds a loaded image to the pending set and says so.
func (m *Model) stage(img provider.Image) {
	m.attached = append(m.attached, img)
	m.add(dimStyle.Render(fmt.Sprintf("  %s attached %s  %s  (%d pending)",
		imageMark, img.Name, byteSize(len(img.Data)), len(m.attached))))
}

// takeAttached hands over the pending images and clears them. They go with one
// message only: an image that stayed staged would silently ride along with
// every question after it, which is both expensive and confusing.
func (m *Model) takeAttached() []provider.Image {
	if len(m.attached) == 0 {
		return nil
	}
	imgs := m.attached
	m.attached = nil
	return imgs
}

// detectImagePaths pulls image paths out of a typed message, the way fx does:
// writing "look at ./shot.png" attaches it, with no command needed.
//
// Only paths that exist and look like images are taken, and the text is left
// exactly as typed — the model still sees the sentence it was written in, which
// is what says what to do with the picture.
func (m *Model) detectImagePaths(text string) []provider.Image {
	var out []provider.Image
	seen := map[string]bool{}
	for _, f := range strings.Fields(text) {
		// Trailing punctuation from prose: "look at shot.png," and "(shot.png)".
		f = strings.Trim(f, "\"'`,;:()[]<>")
		if f == "" || !attach.LooksLikeImage(f) {
			continue
		}
		p := expandPath(f, m.root)
		if seen[p] {
			continue
		}
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			continue
		}
		img, err := attach.Load(p)
		if err != nil {
			// Said out loud rather than dropped: a path that is plainly an
			// image and plainly there, going unattached with no explanation,
			// looks like the feature is broken.
			m.add(errStyle.Render("✗ " + err.Error()))
			continue
		}
		seen[p] = true
		out = append(out, img)
	}
	return out
}

// dropped tries to read a paste as files dragged onto the window.
//
// A terminal has no notion of a drop: dragging a file onto one inserts its path
// as a bracketed paste, exactly as if it had been typed. So a paste that is
// nothing but paths to existing images is treated as a drop, and anything else
// — prose, a stack trace, a path with a sentence around it — is left to fall
// through to the input untouched. That asymmetry is the whole rule: pasting
// text must never be hijacked, and dropping a file should not need a command.
//
// Returns false when the paste is not a drop, in which case nothing was staged.
func (m *Model) dropped(paste string) bool {
	paths, ok := dropPaths(paste, m.root)
	if !ok {
		return false
	}
	var staged bool
	for _, p := range paths {
		img, err := attach.Load(expandPath(p, m.root))
		if err != nil {
			m.add(errStyle.Render("✗ " + err.Error()))
			continue
		}
		m.stage(img)
		staged = true
	}
	// A drop of nothing but unreadable files has already been reported, but it
	// must still count as handled: putting the raw paths into the input after
	// saying they could not be read would be nonsense.
	return staged || len(paths) > 0
}

// dropPaths splits a paste into file paths, reporting false unless every one of
// them is an image that exists.
//
// Terminals disagree on the details. Most escape spaces with a backslash, some
// quote the whole path, and a few send a file:// URL; several send more than one
// path separated by spaces when several files are dragged at once. All of those
// have to parse, and anything that does not has to be rejected rather than
// guessed at.
func dropPaths(paste string, root string) ([]string, bool) {
	s := strings.TrimSpace(paste)
	// A newline means prose far more often than it means two files, and the
	// terminals that send several paths use spaces.
	if s == "" || strings.ContainsAny(s, "\n\r") {
		return nil, false
	}
	fields, ok := splitDrop(s)
	if !ok || len(fields) == 0 {
		return nil, false
	}
	for _, f := range fields {
		if !attach.LooksLikeImage(f) {
			return nil, false
		}
		// Only an existing file is a drop. Without this, typing a sentence that
		// happens to end in ".png" and pasting it would swallow the text.
		if st, err := os.Stat(expandPath(f, root)); err != nil || st.IsDir() {
			return nil, false
		}
	}
	return fields, true
}

// splitDrop breaks a dropped string into paths, honouring the quoting and
// escaping a terminal applies. It reports false on anything it cannot account
// for, such as an unterminated quote.
func splitDrop(s string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	var quote rune

	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '\\' && quote == 0 && i+1 < len(runes):
			// An escaped space is part of the name, not a separator — which is
			// how a Mac sends "Screenshot 2024 at 10.00.png".
			i++
			cur.WriteRune(runes[i])
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case quote == 0 && c == ' ':
			flush()
		default:
			cur.WriteRune(c)
		}
	}
	if quote != 0 {
		return nil, false
	}
	flush()

	for i, p := range out {
		// file:///path/to/a%20shot.png, which is what a few terminals send.
		if rest, found := strings.CutPrefix(p, "file://"); found {
			// The authority is empty for a local file: file:///a is /a.
			if u, err := url.Parse(p); err == nil && u.Host == "" {
				rest = u.Path
			}
			out[i] = rest
		}
	}
	return out, true
}

// expandPath resolves what was typed against the working directory, with ~ for
// home — a screenshot lives in ~/Desktop far more often than in the project.
func expandPath(p string, root string) string {
	p = strings.TrimSpace(p)
	if after, ok := strings.CutPrefix(p, "~"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, after)
		}
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}

func byteSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
