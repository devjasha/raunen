package ui

import (
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
)

// Assistant replies are markdown, and rendering them as flat text loses the
// shape of lists, headings and code. This is a deliberately small line-oriented
// renderer rather than a full markdown library: output is flushed to scrollback
// one line at a time, so a line-at-a-time renderer is the natural fit, and a
// library like Glamour would paint code-block backgrounds, which this program
// never does.
var (
	headingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true)
	bulletStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	codeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	gutterStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	quoteStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	boldStyle    = lipgloss.NewStyle().Bold(true)
	italicStyle  = lipgloss.NewStyle().Italic(true)
)

var (
	reHeading  = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	reBullet   = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	reNumbered = regexp.MustCompile(`^(\s*)(\d+)\.\s+(.*)$`)
	reFence    = regexp.MustCompile("^\\s*```(.*)$")
	reCode     = regexp.MustCompile("`([^`]+)`")
	reBold     = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reItalic   = regexp.MustCompile(`(^|[^*])\*([^*]+)\*`)
)

// markdown holds the state a line renderer needs across lines, which is only
// whether we are inside a fenced code block.
type markdown struct {
	inCode bool
}

// entry renders a markdown line as a transcript entry, choosing prefixes so
// that wrapped text hangs under its marker: a bullet that runs past the width
// continues under the bullet, not back at column zero.
func (md *markdown) entry(s string) entry {
	if m := reFence.FindStringSubmatch(s); m != nil {
		md.inCode = !md.inCode
		// The fence itself carries no information once the block is styled;
		// show the language, if given, as a quiet label.
		if md.inCode && strings.TrimSpace(m[1]) != "" {
			return entry{first: gutterStyle.Render("│ "), cont: gutterStyle.Render("│ "),
				text: gutterStyle.Render(strings.TrimSpace(m[1]))}
		}
		return entry{first: gutterStyle.Render("│")}
	}

	if md.inCode {
		// A gutter marks the block without painting a background.
		g := gutterStyle.Render("│ ")
		return entry{first: g, cont: g, text: codeStyle.Render(s)}
	}

	if m := reHeading.FindStringSubmatch(s); m != nil {
		return entry{text: headingStyle.Render(strings.TrimSpace(m[2]))}
	}

	if m := reBullet.FindStringSubmatch(s); m != nil {
		return entry{
			list:  true,
			first: m[1] + bulletStyle.Render("• "),
			cont:  m[1] + "  ",
			text:  inline(m[2]),
		}
	}

	if m := reNumbered.FindStringSubmatch(s); m != nil {
		marker := m[2] + ". "
		return entry{
			list:  true,
			first: m[1] + bulletStyle.Render(marker),
			cont:  m[1] + strings.Repeat(" ", len(marker)),
			text:  inline(m[3]),
		}
	}

	if rest, ok := strings.CutPrefix(strings.TrimSpace(s), "> "); ok {
		return entry{
			first: quoteStyle.Render("│ "),
			cont:  quoteStyle.Render("│ "),
			text:  quoteStyle.Render(rest),
		}
	}

	// A horizontal rule becomes a quiet divider.
	if t := strings.TrimSpace(s); t == "---" || t == "***" || t == "___" {
		return entry{text: gutterStyle.Render(strings.Repeat("─", 24))}
	}

	return entry{text: inline(s)}
}

// line renders a markdown line as a single string, without the hanging indent.
func (md *markdown) line(s string) string {
	e := md.entry(s)
	return e.first + e.text
}

// inline applies span-level styling: code first, so that emphasis markers
// inside a code span are left alone.
func inline(s string) string {
	s = reCode.ReplaceAllStringFunc(s, func(m string) string {
		return codeStyle.Render(strings.Trim(m, "`"))
	})
	s = reBold.ReplaceAllStringFunc(s, func(m string) string {
		return boldStyle.Render(strings.Trim(m, "*"))
	})
	s = reItalic.ReplaceAllStringFunc(s, func(m string) string {
		// The leading character is captured to avoid matching bold markers, so
		// it has to be put back.
		i := strings.Index(m, "*")
		return m[:i] + italicStyle.Render(strings.Trim(m[i:], "*"))
	})
	return s
}
