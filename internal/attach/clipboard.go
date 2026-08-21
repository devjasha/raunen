package attach

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"raunen/internal/provider"
)

// ErrNoClipboardImage says the clipboard holds something, but not a picture.
var ErrNoClipboardImage = errors.New("no image on the clipboard")

// Clipboard reads an image from the system clipboard.
//
// Every platform needs a helper for this and none of them is guaranteed to be
// installed, so the candidates are tried in turn and the first that produces
// image bytes wins. Shelling out keeps the binary free of a GUI dependency —
// a clipboard library would pull in cgo and X11 headers for a feature that is
// used a handful of times a session.
func Clipboard(ctx context.Context) (provider.Image, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var missing []string
	for _, r := range readers() {
		data, err := r.read(ctx)
		if errors.Is(err, exec.ErrNotFound) {
			missing = append(missing, r.name)
			continue
		}
		if err != nil || len(data) == 0 {
			continue
		}
		return FromBytes(data, "clipboard")
	}
	if len(missing) > 0 && len(missing) == len(readers()) {
		return provider.Image{}, fmt.Errorf("no clipboard reader found; install one of: %s",
			strings.Join(missing, ", "))
	}
	return provider.Image{}, ErrNoClipboardImage
}

type reader struct {
	name string
	read func(context.Context) ([]byte, error)
}

func readers() []reader {
	switch runtime.GOOS {
	case "darwin":
		return []reader{
			{name: "pngpaste", read: run("pngpaste", "-")},
			// AppleScript is always present, so this is the fallback that needs
			// nothing installed. It hands back hex rather than bytes.
			{name: "osascript", read: appleScript},
		}
	case "windows":
		return []reader{{name: "powershell", read: powerShell}}
	default:
		return []reader{
			{name: "wl-paste", read: run("wl-paste", "--type", "image/png")},
			{name: "xclip", read: run("xclip", "-selection", "clipboard", "-t", "image/png", "-o")},
		}
	}
}

func run(name string, args ...string) func(context.Context) ([]byte, error) {
	return func(ctx context.Context) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		var out, errBuf bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errBuf
		if err := cmd.Run(); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}
}

// appleScript asks for the clipboard as PNG. «class PNGf» fails outright when
// the clipboard holds text, which is the signal that there is no image.
func appleScript(ctx context.Context) ([]byte, error) {
	out, err := run("osascript", "-e", "the clipboard as «class PNGf»")(ctx)
	if err != nil {
		return nil, err
	}
	// The result comes back as «data PNGf89504E47...».
	s := strings.TrimSpace(string(out))
	_, s, ok := strings.Cut(s, "PNGf")
	if !ok {
		return nil, ErrNoClipboardImage
	}
	s = strings.TrimSuffix(s, "»")
	return hex.DecodeString(s)
}

func powerShell(ctx context.Context) ([]byte, error) {
	const script = `Add-Type -AssemblyName System.Windows.Forms
$i = [Windows.Forms.Clipboard]::GetImage()
if ($i -eq $null) { exit 1 }
$s = New-Object IO.MemoryStream
$i.Save($s, [Drawing.Imaging.ImageFormat]::Png)
[Console]::OpenStandardOutput().Write($s.ToArray(), 0, $s.Length)`
	return run("powershell", "-NoProfile", "-Command", script)(ctx)
}
