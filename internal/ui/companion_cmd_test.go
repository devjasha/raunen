package ui

import (
	"strings"
	"testing"

	"raunen/internal/agent"
	"raunen/internal/companion"
	"raunen/internal/provider"
)

// TestPrestigeWaitsForTheTop covers the refusal, which is the case almost every
// user will meet: the command exists in the list long before it can be used, so
// it has to say what it is waiting for rather than do nothing.
func TestPrestigeWaitsForTheTop(t *testing.T) {
	m := testModel(t)
	m.comp.Feed("ollama/qwen3", companion.TokensForLevel(4))

	m.command("/prestige")

	out := strings.Join(rowsOf(m), "\n")
	if !strings.Contains(out, "500") {
		t.Errorf("refusal did not say what level it waits for:\n%s", out)
	}
	if m.comp.Prestige != 0 || m.comp.Tokens == 0 {
		t.Errorf("a refused /prestige still changed the companion: %+v", m.comp)
	}
}

// TestPrestigeAscendsAndPersists is the whole point of the command: the climb
// resets, the history does not, and it survives closing the terminal.
func TestPrestigeAscendsAndPersists(t *testing.T) {
	m := testModel(t)
	m.comp.Feed("ollama/qwen3", companion.TokensForLevel(companion.MaxLevel))
	lifetime := m.comp.Lifetime

	m.command("/prestige")

	if m.comp.Prestige != 1 {
		t.Errorf("prestige = %d, want 1", m.comp.Prestige)
	}
	if m.comp.Level() != 1 {
		t.Errorf("level = %d after ascending, want 1", m.comp.Level())
	}
	out := strings.Join(rowsOf(m), "\n")
	if !strings.Contains(out, "ascended") {
		t.Errorf("nothing in the transcript said it happened:\n%s", out)
	}

	// Reloaded from disk, since the command saves rather than waiting for the
	// next turn to do it.
	saved := companion.Load()
	if saved.Prestige != 1 || saved.Tokens != 0 || saved.Lifetime != lifetime {
		t.Errorf("saved companion = %+v, want prestige 1, no tokens, %d lifetime",
			saved, lifetime)
	}
}

// TestLevelUpNoticeIsQuietUntilTheNameChanges guards the reason the notice was
// split: at five hundred levels a full announcement every level is noise in the
// middle of a conversation, and the band change stops being noticeable.
func TestLevelUpNoticeIsQuietUntilTheNameChanges(t *testing.T) {
	// Two levels inside one title band say only the number.
	within := noticeFor(t, companion.TokensForLevel(2), companion.TokensForLevel(3))
	if !strings.Contains(within, "level 3") {
		t.Errorf("a plain level-up said %q, want the new level in it", within)
	}
	if strings.Contains(within, companion.TitleForLevel(3)) {
		t.Errorf("a plain level-up repeated the unchanged title: %q", within)
	}

	// Crossing into a new band names it.
	band := 1 + companion.MaxLevel/10
	crossing := noticeFor(t, companion.TokensForLevel(band-1), companion.TokensForLevel(band))
	if !strings.Contains(crossing, companion.TitleForLevel(band)) {
		t.Errorf("crossing into %s said %q, want the new title in it",
			companion.TitleForLevel(band), crossing)
	}

	// And the top of the ladder points at what comes next.
	top := noticeFor(t, companion.TokensForLevel(companion.MaxLevel-1),
		companion.TokensForLevel(companion.MaxLevel))
	if !strings.Contains(top, "/prestige") {
		t.Errorf("reaching the top said %q, want it to mention /prestige", top)
	}
}

// noticeFor drives a usage event through the model and returns whatever the
// transcript said about the level it produced. The notice is written where the
// tokens are counted, so nothing short of the real event exercises it.
func noticeFor(t *testing.T, from, to int64) string {
	t.Helper()
	m := testModel(t)
	m.comp.Tokens = from
	m.onEvent(begin(&m), agent.Usage{Usage: provider.Usage{Total: int(to - from)}})
	if m.comp.Level() != levelAt(to) {
		t.Fatalf("companion ended at level %d, want %d", m.comp.Level(), levelAt(to))
	}
	return strings.Join(rowsOf(m), "\n")
}

// levelAt is what the companion should read at a total, asked of the package
// rather than worked out again here.
func levelAt(tokens int64) int {
	c := companion.Companion{Tokens: tokens}
	return c.Level()
}

// TestHumanTokensReachesBillions covers the unit the long ladder added: the top
// of it is 2.49 billion tokens, and the count is rendered in a bar and a
// progress line where the extra three digits do not fit.
func TestHumanTokensReachesBillions(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{940, "940"},
		{1_200, "1.2k"},
		{24_000, "24k"},
		{2_400_000, "2.4M"},
		{240_000_000, "240M"},
		{2_490_000_000, "2.5B"},
		{31_000_000_000, "31B"},
	} {
		if got := humanTokens(tc.n); got != tc.want {
			t.Errorf("humanTokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestStagesArriveEarly guards the reason the art thresholds are fixed levels
// rather than a share of the ladder: at a third of five hundred a companion fed
// for months would still be drawn as an egg, and the art is meant to show early
// progress rather than mark the middle of the climb.
func TestStagesArriveEarly(t *testing.T) {
	if same(stageArt(1), stageArt(eggUntil+1)) {
		t.Error("the egg never hatches within the first ten levels")
	}
	if same(stageArt(eggUntil+1), stageArt(younglingTill+1)) {
		t.Error("the young dragon never grows up within the first fifty levels")
	}
	// And the top of the ladder is spent as the full dragon, which is most of
	// it by design.
	if !same(stageArt(younglingTill+1), stageArt(companion.MaxLevel)) {
		t.Error("the art changes again above the last stage")
	}
	// The welcome screen reads the same boundary, so a narrow terminal cannot
	// show an egg next to a companion view showing a dragon.
	if !same(stageArt(eggUntil), hatchling) {
		t.Error("stageArt and the welcome screen disagree about where the egg ends")
	}
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestProgressNamesTheNextBandOnlyWhenItChanges covers a line that read as
// nonsense at ten levels' worth of titles spread over five hundred: "10k to
// Hush" while already Hush.
func TestProgressNamesTheNextBandOnlyWhenItChanges(t *testing.T) {
	within := progressLine(t, companion.TokensForLevel(2))
	if strings.Contains(within, "to "+companion.TitleForLevel(2)) {
		t.Errorf("progress inside a band pointed at its own title: %q", within)
	}
	if !strings.Contains(within, "to level 3") {
		t.Errorf("progress inside a band = %q, want it counting to the next level", within)
	}

	edge := companion.MaxLevel / 10 // the last level of the first band
	crossing := progressLine(t, companion.TokensForLevel(edge))
	if !strings.Contains(crossing, "to "+companion.TitleForLevel(edge+1)) {
		t.Errorf("progress at a band edge = %q, want it naming %s",
			crossing, companion.TitleForLevel(edge+1))
	}
}

// progressLine is the progress row of the companion view at a given total.
func progressLine(t *testing.T, tokens int64) string {
	t.Helper()
	m := testModel(t)
	m.comp.Tokens = tokens
	for _, e := range m.companionRows() {
		m.push(e)
	}
	for _, r := range rowsOf(m) {
		if strings.Contains(r, "progress") {
			return r
		}
	}
	t.Fatalf("no progress row at %d tokens", tokens)
	return ""
}
