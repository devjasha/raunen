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

	// Each threshold reached is exactly one level.
	for i, need := range thresholds {
		c.Tokens = need
		if got, want := c.Level(), i+1; got != want {
			t.Errorf("at %d tokens level = %d, want %d", need, got, want)
		}
	}

	// Past the top it stays at the top rather than running off the end of the
	// titles.
	c.Tokens = thresholds[len(thresholds)-1] * 100
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

	before, after := c.Feed("ollama/qwen3", thresholds[1]-1)
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
	c.Tokens = thresholds[1] + (thresholds[2]-thresholds[1])/2

	into, span := c.Progress()
	if into*2 != span {
		t.Errorf("halfway through level 2 reads as %d of %d", into, span)
	}
	if c.Next() != span-into {
		t.Errorf("Next() = %d, want %d", c.Next(), span-into)
	}

	// At the top a bar should read as full, not as a division by zero.
	c.Tokens = thresholds[len(thresholds)-1]
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
