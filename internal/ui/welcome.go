package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The mascot. Foreground colours only, like everything else here, so it sits on
// the terminal's own background.
var (
	dragonStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	flameStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	nameStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true)
	taglineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
)

// dragon is drawn with a leading backslash on the first line, so the raw string
// is written with explicit escapes rather than a backquoted literal.
var dragon = []string{
	"                \\||/",
	"                |  @___oo",
	"      /\\  /\\   / (__,,,,|",
	"     ) /^\\) ^\\/ _)",
	"     )   /^\\/   _)",
	"     )   _ /  / _)",
	" /\\  )/\\/ ||  | )_)",
	"<  >      |(,,) )__)",
	" ||      /    \\)___)\\",
	" | \\____(      )___) )___",
	"  \\______(_______;;; __;;;",
}

// smallDragon is for terminals too short or narrow for the full mascot. A
// welcome screen that pushes the input off the bottom is worse than no welcome
// screen at all.
var smallDragon = []string{
	"     \\||/",
	"     |  @___oo",
	"/\\  /\\  / (__,,,,|",
	") /^\\)^\\/ _)",
	")  _ /  /_)",
	"\\_(  ) )__)",
}

// welcomeRows renders the splash shown while the transcript is empty, centred
// in the space available. It returns nil when there is not even room for the
// small version, so a very short terminal simply starts with an empty screen.
func welcomeRows(width, height int, model, mode, root string) []string {
	art, gap := dragon, 2
	if height < len(dragon)+8 || width < 46 {
		art, gap = smallDragon, 1
	}
	if height < len(art)+5 {
		return nil
	}

	lines := make([]string, 0, len(art)+6)
	for _, l := range art {
		lines = append(lines, dragonStyle.Render(l))
	}
	// A wisp of fire, in a warmer colour than the dragon itself. Only the full
	// mascot has one, so the suffix is checked rather than assumed.
	const flame = "\\||/"
	if strings.HasSuffix(art[0], flame) {
		lines[0] = dragonStyle.Render(strings.TrimSuffix(art[0], flame)) + flameStyle.Render(flame)
	}

	for i := 0; i < gap; i++ {
		lines = append(lines, "")
	}

	// Narrow terminals get shorter copy rather than truncated copy: a tagline
	// cut off mid-word reads worse than a brief one.
	tagline := "  ·  a small terminal agent for local LLMs"
	meta := mode + "  ·  " + model + "  ·  " + root
	hint := "/help for commands  ·  tab to change mode"
	if width < 60 {
		tagline = "  ·  local LLM agent"
		meta = mode + "  ·  " + model
		hint = "/help  ·  tab for mode"
	}
	lines = append(lines,
		nameStyle.Render("raunen")+taglineStyle.Render(tagline),
		"",
		taglineStyle.Render(meta),
		taglineStyle.Render(hint),
	)

	// Whatever is still too wide is trimmed, so the splash can never spill into
	// the gutter or wrap and push the input off the screen.
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, width, "…")
	}
	return centre(lines, width)
}

// centre pads each line so the block sits in the middle, measured on display
// width so the styling does not throw the alignment off.
func centre(lines []string, width int) []string {
	widest := 0
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > widest {
			widest = w
		}
	}
	pad := max(0, (width-widest)/2)
	prefix := strings.Repeat(" ", pad)

	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l == "" {
			out = append(out, "")
			continue
		}
		out = append(out, prefix+l)
	}
	return out
}
