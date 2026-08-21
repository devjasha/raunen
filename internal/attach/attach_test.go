package attach

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Minimal but genuine headers: sniffing is done on the bytes, so the test data
// has to be the real thing rather than a plausible name.
var (
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	jpegBytes = append([]byte("\xff\xd8\xff\xe0"), make([]byte, 32)...)
	gifBytes  = append([]byte("GIF89a"), make([]byte, 32)...)
)

func webpBytes() []byte {
	b := []byte("RIFF____WEBPVP8 ")
	return append(b, make([]byte, 32)...)
}

func TestLoadAcceptsEveryFormatModelsRead(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string][]byte{
		"a.png":  pngBytes,
		"b.jpg":  jpegBytes,
		"c.gif":  gifBytes,
		"d.webp": webpBytes(),
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		img, err := Load(p)
		if err != nil {
			t.Errorf("Load(%s): %v", name, err)
			continue
		}
		if img.Name != name {
			t.Errorf("name = %q, want %q", img.Name, name)
		}
		if !bytes.Equal(img.Data, data) {
			t.Errorf("%s: bytes changed on the way in", name)
		}
	}
}

// The extension is not evidence. A JPEG saved as .png is common — screenshots
// renamed by hand — and declaring the wrong type makes the endpoint fail on
// something that has nothing to do with the actual problem.
func TestTypeComesFromTheBytesNotTheName(t *testing.T) {
	p := filepath.Join(t.TempDir(), "screenshot.png")
	if err := os.WriteFile(p, jpegBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	img, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if img.MIME != "image/jpeg" {
		t.Errorf("mime = %q, want image/jpeg despite the .png name", img.MIME)
	}
}

func TestUnsupportedFormatIsRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(p, []byte("just some prose"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if !errors.Is(err, ErrNotImage) {
		t.Errorf("err = %v, want ErrNotImage", err)
	}
	// The file name has to be in the message: with several attached, "unsupported
	// image format" alone does not say which one to fix.
	if err != nil && !strings.Contains(err.Error(), "notes.txt") {
		t.Errorf("err = %v, want it to name the file", err)
	}
}

func TestOversizedImageIsRefusedWithItsSize(t *testing.T) {
	_, err := FromBytes(make([]byte, MaxBytes+1), "huge.png")
	if err == nil {
		t.Fatal("an image over the limit should be refused")
	}
	if !strings.Contains(err.Error(), "huge.png") || !strings.Contains(err.Error(), "MiB") {
		t.Errorf("err = %v, want the name and the size", err)
	}
}

func TestMissingFileReportsWhy(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.png")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want a not-exist error", err)
	}
}

func TestDirectoryIsNotAnImage(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Error("a directory should not load as an image")
	}
}

func TestLooksLikeImage(t *testing.T) {
	for _, p := range []string{"a.png", "b.JPG", "c.jpeg", "d.gif", "e.webp"} {
		if !LooksLikeImage(p) {
			t.Errorf("LooksLikeImage(%q) = false", p)
		}
	}
	// Things that are emphatically not attachments, including a source file
	// whose name merely contains one.
	for _, p := range []string{"main.go", "png", "notes.txt", "image.png.go", ""} {
		if LooksLikeImage(p) {
			t.Errorf("LooksLikeImage(%q) = true", p)
		}
	}
}

// The whole point of Load's stat check: a huge file must be refused before it
// is read into memory.
func TestOversizedFileIsRefusedBeforeReading(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	// Sparse: the file reports its size without occupying it on disk.
	if err := f.Truncate(MaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("err = %v, want a size refusal", err)
	}
}
