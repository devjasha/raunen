// Package attach turns a path or a clipboard into an image a model can be
// shown. It is deliberately strict: what an endpoint does with an unsupported
// or oversized attachment is refuse the whole request, usually with a message
// about tokens that says nothing about the file — so the check happens here,
// where the file name is still known.
package attach

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"raunen/internal/provider"
)

// MaxBytes is the per-image ceiling. Twenty mebibytes is what the major
// endpoints accept; larger is refused before it is base64-encoded into a
// request a third larger again.
const MaxBytes = 20 << 20

// Supported are the formats models actually accept. A TIFF or a PDF is not
// half-supported, it is rejected by the endpoint, so it is rejected here.
var Supported = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// ErrNotImage says the bytes are not an image of a kind a model can read.
var ErrNotImage = errors.New("unsupported image format")

// Load reads an image from disk.
func Load(path string) (provider.Image, error) {
	info, err := os.Stat(path)
	if err != nil {
		return provider.Image{}, err
	}
	// Checked before reading: an accidental `raunen --image ./video.mov` should
	// not pull a gigabyte into memory only to be told no.
	if info.Size() > MaxBytes {
		return provider.Image{}, fmt.Errorf("%s is %s, over the %s limit",
			filepath.Base(path), size(info.Size()), size(MaxBytes))
	}
	if info.IsDir() {
		return provider.Image{}, fmt.Errorf("%s is a directory", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return provider.Image{}, err
	}
	return FromBytes(data, filepath.Base(path))
}

// FromBytes wraps bytes already in hand — a clipboard, a piped file.
func FromBytes(data []byte, name string) (provider.Image, error) {
	if len(data) > MaxBytes {
		return provider.Image{}, fmt.Errorf("%s is %s, over the %s limit",
			name, size(int64(len(data))), size(MaxBytes))
	}
	mime, ok := sniff(data)
	if !ok {
		return provider.Image{}, fmt.Errorf("%w: %s", ErrNotImage, name)
	}
	return provider.Image{MIME: mime, Data: data, Name: name}, nil
}

// sniff decides the type from the bytes rather than the extension. A .png that
// is really a JPEG is common enough — screenshots renamed by hand — and the
// endpoint believes the declared type over the content, then fails.
func sniff(data []byte) (string, bool) {
	// http.DetectContentType predates WebP, so that one is matched directly:
	// "RIFF....WEBP".
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return "image/webp", true
	}
	mime, _, _ := strings.Cut(http.DetectContentType(data), ";")
	for _, s := range Supported {
		if mime == s {
			return mime, true
		}
	}
	return "", false
}

// LooksLikeImage reports whether a path is worth trying as an attachment. Used
// to spot an image path typed into a prompt, where guessing from the extension
// is right: the file is not to be read unless it looks intended as one.
func LooksLikeImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

func size(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
