package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"raunen/internal/companion"
)

// The dragon grows in three stages, so a level-up is something you can see
// rather than a number changing. Levels 1-3 are an egg and what comes out of it,
// 4-6 a coiled young dragon, 7 and up the full thing.
var hatchling = []string{
	"    _~_",
	"   ( o )",
	"  (  ~  )",
	"   '---'",
}

// stageArt returns the art for a level, and how many stages remain above it.
func stageArt(level int) []string {
	switch {
	case level <= 3:
		return hatchling
	case level <= 6:
		return smallDragon
	default:
		return dragon
	}
}

var (
	levelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	xpFull     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

// companionRows renders the companion view: what it is now, how far to the next
// stage, and what has been feeding it.
func (m Model) companionRows() []entry {
	c := m.comp
	var out []entry

	add := func(e entry) { out = append(out, e) }
	row := func(k, v string) {
		add(entry{
			first: "  " + dimStyle.Render(fmt.Sprintf("%-10s", k)),
			cont:  indent,
			text:  v,
		})
	}

	add(entry{})
	add(entry{rule: true, stamp: "companion"})

	for _, l := range stageArt(c.Level()) {
		add(entry{first: "    ", cont: "    ", text: dragonStyle.Render(l)})
	}
	add(entry{})

	row("level", levelStyle.Render(fmt.Sprintf("%d  %s", c.Level(), c.Title())))

	// A bar for the level, and what it would take to fill it.
	into, span := c.Progress()
	const cells = 20
	filled := int(int64(cells) * into / max64(span, 1))
	bar := xpFull.Render(strings.Repeat("█", filled)) +
		dimStyle.Render(strings.Repeat("░", cells-filled))
	if next := c.Next(); next > 0 {
		row("progress", bar+dimStyle.Render(fmt.Sprintf("  %s to %s",
			humanTokens(int(next)), companionTitle(c.Level()+1))))
	} else {
		row("progress", bar+dimStyle.Render("  fully grown"))
	}

	row("context", dimStyle.Render(fmt.Sprintf("%s tokens across %d turns",
		humanTokens(int(c.Tokens)), c.Turns)))
	row("work", dimStyle.Render(fmt.Sprintf("%d tool calls  ·  %d delegated tasks",
		c.Tools, c.Tasks)))
	row("hatched", dimStyle.Render(c.Age()))

	// The point of a companion rather than a per-model score: it grows on
	// whatever you happen to be using.
	top := c.Top(4)
	if len(top) == 0 {
		row("fed by", dimStyle.Render("nothing yet"))
		return out
	}
	models := "models"
	if len(c.Models) == 1 {
		models = "model"
	}
	row("fed by", dimStyle.Render(fmt.Sprintf("%d %s", len(c.Models), models)))
	for _, t := range top {
		share := 100 * t.Tokens / max64(c.Tokens, 1)
		add(entry{
			first: indent,
			cont:  indent + "  ",
			text: dimStyle.Render(fmt.Sprintf("%-44s %5s  %2d%%",
				ansi.Truncate(t.Ref, 44, "…"), humanTokens(int(t.Tokens)), share)),
		})
	}
	return out
}

// companionTitle names a level without needing a Companion to ask.
func companionTitle(level int) string {
	c := companion.Companion{}
	// Titles are a property of the level, so borrow one at that level.
	for level > companion.MaxLevel {
		level = companion.MaxLevel
	}
	c.Tokens = companion.TokensForLevel(level)
	return c.Title()
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
