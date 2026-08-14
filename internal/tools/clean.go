package tools

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Clean strips noise from command output before it is charged to the context.
//
// Terminal output is written for a terminal, not for a model: colour codes,
// progress bars redrawn over themselves, and long runs of identical lines all
// cost tokens and carry nothing. Removing them means a given budget holds more
// of what actually matters, and it happens before truncation, so the useful end
// of a long build log is more likely to survive.
//
// Everything here is meaning-preserving. Nothing rewrites words, reorders
// lines, or summarises: a tool result is evidence, and a model reasoning about
// evidence that has been paraphrased is worse than one reasoning about less of
// it.
func Clean(s string) string {
	if s == "" {
		return s
	}

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))

	var (
		blanks    int
		prev      string
		repeats   int
		flushDupe = func() {
			if repeats > 1 {
				// Replace the run with one line and a count, which is shorter
				// and states plainly what was there.
				out[len(out)-1] = fmt.Sprintf("%s  (×%d)", prev, repeats)
			}
			repeats = 0
		}
	)

	for _, line := range lines {
		// A progress bar rewrites one line many times; only the last state was
		// ever visible, and the rest is pure noise.
		if i := strings.LastIndexByte(line, '\r'); i >= 0 {
			line = line[i+1:]
		}
		line = strings.TrimRight(ansi.Strip(line), " \t")

		if strings.TrimSpace(line) == "" {
			flushDupe()
			prev = ""
			blanks++
			// One blank line separates; more is just spacing for human eyes.
			if blanks > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blanks = 0

		if line == prev {
			repeats++
			continue
		}
		flushDupe()
		prev = line
		repeats = 1
		out = append(out, line)
	}
	flushDupe()

	// Leading and trailing blank lines carry nothing at all.
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}
