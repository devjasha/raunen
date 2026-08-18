package companion

import (
	"os"
	"path/filepath"
	"testing"
)

func fresh() *Companion {
	return &Companion{Models: map[string]int64{}}
}

func TestLevelsRiseWithTokens(t *testing.T) {
	c := fresh()
	if got := c.Level(); got != 1 {
		t.Errorf("a new companion is level %d, want 1", got)
	}
	if got := c.Title(); got != "Hush" {
		t.Errorf("title = %q, want Hush", got)
	}

	// Each threshold reached is exactly one level, checked across the whole
	// ladder rather than the first few: the curve is a formula now, and an
	// off-by-one in it would only show up high up.
	for _, level := range []int{2, 3, 10, 50, 100, 250, 499, MaxLevel} {
		need := TokensForLevel(level)
		c.Tokens = need
		if got := c.Level(); got != level {
			t.Errorf("at %d tokens level = %d, want %d", need, got, level)
		}
		// One token short is the level below, which is where a rounding error
		// in the inverted curve would surface.
		c.Tokens = need - 1
		if got := c.Level(); got != level-1 {
			t.Errorf("at %d tokens (one short of %d) level = %d, want %d",
				need-1, level, got, level-1)
		}
	}

	// Past the top it stays at the top rather than running off the end of the
	// titles.
	c.Tokens = TokensForLevel(MaxLevel) * 100
	if got := c.Level(); got != MaxLevel {
		t.Errorf("level = %d, want it capped at %d", got, MaxLevel)
	}
	if got := c.Title(); got != "Thunder" {
		t.Errorf("title = %q, want Thunder", got)
	}
}

// Feed reports the levels either side so a caller can notice a level-up without
// keeping track itself.
func TestFeedReportsLevelUp(t *testing.T) {
	c := fresh()

	before, after := c.Feed("ollama/qwen3", TokensForLevel(2)-1)
	if before != 1 || after != 1 {
		t.Errorf("Feed just short of the threshold = %d→%d, want 1→1", before, after)
	}

	before, after = c.Feed("ollama/qwen3", 1)
	if before != 1 || after != 2 {
		t.Errorf("Feed onto the threshold = %d→%d, want 1→2", before, after)
	}

	// Nothing to add is not a level-up.
	before, after = c.Feed("ollama/qwen3", 0)
	if before != after {
		t.Errorf("Feed(0) = %d→%d, want no change", before, after)
	}
}

// The companion belongs to the user, not to a model, so tokens from every
// provider go into the same total.
func TestFeedAggregatesAcrossProviders(t *testing.T) {
	c := fresh()
	c.Feed("ollama/qwen3.5", 5_000)
	c.Feed("openrouter/nvidia/nemotron:free", 20_000)
	c.Feed("groq/llama-3.3", 7_000)
	c.Feed("ollama/qwen3.5", 1_000)

	if c.Tokens != 33_000 {
		t.Errorf("total = %d, want 33000 — every provider feeds the same companion", c.Tokens)
	}
	if got := c.Models["ollama/qwen3.5"]; got != 6_000 {
		t.Errorf("ollama share = %d, want 6000 accumulated across both turns", got)
	}

	top := c.Top(2)
	if len(top) != 2 {
		t.Fatalf("Top(2) returned %d", len(top))
	}
	if top[0].Ref != "openrouter/nvidia/nemotron:free" {
		t.Errorf("largest contributor = %q, want the openrouter one", top[0].Ref)
	}
	if top[1].Ref != "groq/llama-3.3" {
		t.Errorf("second = %q, want groq", top[1].Ref)
	}
}

func TestProgressWithinALevel(t *testing.T) {
	c := fresh()
	c.Tokens = TokensForLevel(2) + (TokensForLevel(3)-TokensForLevel(2))/2

	into, span := c.Progress()
	if into*2 != span {
		t.Errorf("halfway through level 2 reads as %d of %d", into, span)
	}
	if c.Next() != span-into {
		t.Errorf("Next() = %d, want %d", c.Next(), span-into)
	}

	// At the top a bar should read as full, not as a division by zero.
	c.Tokens = TokensForLevel(MaxLevel)
	into, span = c.Progress()
	if into != span || span == 0 {
		t.Errorf("at max level progress = %d of %d, want a full bar", into, span)
	}
	if c.Next() != 0 {
		t.Errorf("Next() at max = %d, want 0", c.Next())
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	c := Load()
	c.Feed("ollama/qwen3", 42_000)
	c.Turns = 7
	c.Tools = 19
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	again := Load()
	if again.Tokens != 42_000 || again.Turns != 7 || again.Tools != 19 {
		t.Errorf("round trip lost data: %+v", again)
	}
	if again.Models["ollama/qwen3"] != 42_000 {
		t.Errorf("per-model tally lost: %v", again.Models)
	}
	if again.Level() != c.Level() {
		t.Errorf("level changed across a save: %d then %d", c.Level(), again.Level())
	}
}

// A damaged file must not stop the program. Losing a level is a smaller problem
// than refusing to start.
func TestLoadSurvivesACorruptFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	path := filepath.Join(dir, "raunen", "companion.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json at all"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := Load()
	if c == nil {
		t.Fatal("Load returned nil on a corrupt file")
	}
	if c.Level() != 1 || c.Models == nil {
		t.Errorf("corrupt file did not yield a usable companion: %+v", c)
	}
}

// TestCurveIsMonotonic walks every level rather than sampling. A formula can be
// wrong in one place in a way a table cannot, and a level that costs less than
// the one below it would let a companion move backwards.
func TestCurveIsMonotonic(t *testing.T) {
	prev := TokensForLevel(1)
	if prev != 0 {
		t.Errorf("level 1 starts at %d tokens, want 0", prev)
	}
	for level := 2; level <= MaxLevel; level++ {
		got := TokensForLevel(level)
		if got <= prev {
			t.Fatalf("level %d starts at %d, which is not above level %d at %d",
				level, got, level-1, prev)
		}
		prev = got
	}
}

// TestLevelRoundTripsTheWholeLadder is the guard for inverting the curve with a
// square root: float error near an exact threshold would put the companion one
// level off, and only at certain values.
func TestLevelRoundTripsTheWholeLadder(t *testing.T) {
	c := fresh()
	for level := 1; level <= MaxLevel; level++ {
		c.Tokens = TokensForLevel(level)
		if got := c.Level(); got != level {
			t.Fatalf("at the exact threshold for level %d (%d tokens) Level() = %d",
				level, c.Tokens, got)
		}
	}
}

// TestTitlesSpanTheLadder checks the ten names cover the five hundred levels
// without a gap or an unreachable one.
func TestTitlesSpanTheLadder(t *testing.T) {
	if got := TitleForLevel(1); got != "Hush" {
		t.Errorf("level 1 is %q, want Hush", got)
	}
	if got := TitleForLevel(MaxLevel); got != "Thunder" {
		t.Errorf("level %d is %q, want Thunder", MaxLevel, got)
	}
	seen := map[string]bool{}
	for level := 1; level <= MaxLevel; level++ {
		seen[TitleForLevel(level)] = true
	}
	if len(seen) != len(titles) {
		t.Errorf("%d of %d titles are reachable", len(seen), len(titles))
	}
}

// TestPrestigeKeepsTheHistory covers the point of ascending: the climb resets,
// the record does not.
func TestPrestigeKeepsTheHistory(t *testing.T) {
	c := fresh()

	if c.Ascend() {
		t.Error("ascended from level 1")
	}

	c.Feed("ollama/qwen3", TokensForLevel(MaxLevel))
	if !c.AtTop() {
		t.Fatalf("at %d tokens the companion is level %d, want %d",
			c.Tokens, c.Level(), MaxLevel)
	}
	lifetime := c.Lifetime

	if !c.Ascend() {
		t.Fatal("could not ascend at the top of the ladder")
	}
	if c.Prestige != 1 {
		t.Errorf("prestige = %d, want 1", c.Prestige)
	}
	if c.Level() != 1 {
		t.Errorf("level = %d after ascending, want 1", c.Level())
	}
	if c.Tokens != 0 {
		t.Errorf("tokens = %d after ascending, want 0", c.Tokens)
	}
	if c.Lifetime != lifetime {
		t.Errorf("lifetime = %d after ascending, want it kept at %d", c.Lifetime, lifetime)
	}
	if len(c.Models) != 0 {
		t.Errorf("models = %v after ascending, want the new climb to start empty", c.Models)
	}

	// And it climbs again from there.
	c.Feed("ollama/qwen3", TokensForLevel(2))
	if c.Level() != 2 {
		t.Errorf("level = %d after feeding a fresh climb, want 2", c.Level())
	}
	if c.Lifetime <= lifetime {
		t.Error("lifetime did not carry on accumulating after a prestige")
	}
}

// TestFeedAtTopDoesNotAutoReset guards a deliberate decision: reaching the top
// is the achievement, and silently starting over the moment it lands would take
// that away rather than reward it.
func TestFeedAtTopDoesNotAutoReset(t *testing.T) {
	c := fresh()
	c.Feed("ollama/qwen3", TokensForLevel(MaxLevel)*2)
	if c.Level() != MaxLevel {
		t.Errorf("level = %d, want %d", c.Level(), MaxLevel)
	}
	if c.Prestige != 0 {
		t.Errorf("prestige = %d, want it to wait to be asked for", c.Prestige)
	}
}
