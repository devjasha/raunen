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

// The two points where the art changes. They are fixed levels rather than a
// share of the ladder: a third of five hundred levels is 222M tokens, which
// would leave a companion fed for months still drawn as an egg. The stages mark
// the beginning of the climb — hatching within a day or so, grown within a few
// weeks — and the rest of the five hundred is spent as a full dragon, which is
// the point of the number being large.
const (
	eggUntil      = 10
	younglingTill = 50
)

// stageArt returns the art for a level: an egg, a coiled young dragon, then the
// full thing.
func stageArt(level int) []string {
	switch {
	case level <= eggUntil:
		return hatchling
	case level <= younglingTill:
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

	lvl := levelStyle.Render(fmt.Sprintf("%d  %s", c.Level(), c.Title()))
	if c.Prestige > 0 {
		// The climbs already finished, which the level number alone cannot say
		// once it has been reset to 1.
		lvl += dimStyle.Render(fmt.Sprintf("   ✦%d", c.Prestige))
	}
	row("level", lvl)

	// A bar for the level, and what it would take to fill it.
	into, span := c.Progress()
	const cells = 20
	filled := int(int64(cells) * into / max64(span, 1))
	bar := xpFull.Render(strings.Repeat("█", filled)) +
		dimStyle.Render(strings.Repeat("░", cells-filled))
	if next := c.Next(); next > 0 {
		// The name only when it is about to change: within a band "to Murmur"
		// reads as a journey to where the dragon already is, so the rest of the
		// time the number is the honest answer.
		to := fmt.Sprintf("level %d", c.Level()+1)
		if title := companion.TitleForLevel(c.Level() + 1); title != c.Title() {
			to = title
		}
		row("progress", bar+dimStyle.Render(fmt.Sprintf("  %s to %s", humanTokens(int(next)), to)))
	} else {
		row("progress", bar+okStyle.Render("  fully grown")+
			dimStyle.Render("  /prestige to begin again"))
	}

	row("context", dimStyle.Render(fmt.Sprintf("%s tokens across %d turns",
		humanTokens(int(c.Tokens)), c.Turns)))
	if c.Prestige > 0 {
		row("lifetime", dimStyle.Render(fmt.Sprintf("%s tokens across %d climbs",
			humanTokens(int(c.Lifetime)), c.Prestige+1)))
	}
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

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
