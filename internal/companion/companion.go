// Package companion keeps the mascot's progress across every session, model and
// provider.
//
// The point of it being a companion rather than a score is that it belongs to
// you, not to a model: switching from a local 8k model to a hosted 1M one, or
// starting a new session, carries the same dragon forward. It is fed by the one
// resource every provider charges in — context — so it grows on whatever you
// happen to be talking to.
package companion

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Titles run from quiet to loud, which is the joke the name is already making:
// raunen is to murmur. A new dragon barely makes a sound.
var titles = []string{
	"Hush", "Whisper", "Murmur", "Rumour", "Echo",
	"Chant", "Chorus", "Bellow", "Roar", "Thunder",
}

// thresholds are the cumulative tokens each level needs. The early ones arrive
// within a session or two so there is something to see, and they roughly double
// after that so the last few are a genuine haul.
var thresholds = []int64{
	0,
	10_000,
	30_000,
	75_000,
	175_000,
	400_000,
	900_000,
	2_000_000,
	4_500_000,
	10_000_000,
}

// MaxLevel is the top of the ladder.
var MaxLevel = len(thresholds)

// TokensForLevel is the total a level begins at, so a caller can name a level
// it has not reached.
func TokensForLevel(level int) int64 {
	if level < 1 {
		level = 1
	}
	if level > MaxLevel {
		level = MaxLevel
	}
	return thresholds[level-1]
}

// Companion is the saved state. Counts are cumulative and never reset — a new
// session adds to them rather than starting over.
type Companion struct {
	Tokens int64 `json:"tokens"`
	Turns  int   `json:"turns"`
	Tools  int   `json:"tools"`
	Tasks  int   `json:"tasks"`
	// Models records what fed it, keyed by "provider/model". It is the evidence
	// that the companion is not tied to any one of them.
	Models map[string]int64 `json:"models"`
	Since  time.Time        `json:"since"`

	// file is where this was read from.
	file string `json:"-"`
}

// Path is where the companion lives — data rather than configuration, next to
// the sessions.
func Path() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "raunen", "companion.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "raunen-companion.json"
	}
	return filepath.Join(home, ".local", "share", "raunen", "companion.json")
}

// Load reads the companion, returning a new one when there is nothing saved.
func Load() *Companion {
	path := Path()
	c := &Companion{Models: map[string]int64{}, Since: time.Now(), file: path}

	b, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	// A corrupt file should not stop the program: a lost level is a smaller
	// problem than refusing to start.
	if json.Unmarshal(b, c) != nil {
		return &Companion{Models: map[string]int64{}, Since: time.Now(), file: path}
	}
	if c.Models == nil {
		c.Models = map[string]int64{}
	}
	c.file = path
	return c
}

// Save writes the companion out.
func (c *Companion) Save() error {
	if c.file == "" {
		c.file = Path()
	}
	if err := os.MkdirAll(filepath.Dir(c.file), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.file + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.file)
}

// Feed adds the tokens a request cost, attributed to the model that served it.
// It returns the levels before and after, so a caller can notice a level-up
// without tracking it.
func (c *Companion) Feed(ref string, tokens int64) (before, after int) {
	before = c.Level()
	if tokens <= 0 {
		return before, before
	}
	c.Tokens += tokens
	if c.Models == nil {
		c.Models = map[string]int64{}
	}
	c.Models[ref] += tokens
	return before, c.Level()
}

// Level is how far up the ladder the total has carried it.
func (c *Companion) Level() int {
	level := 1
	for i, need := range thresholds {
		if c.Tokens >= need {
			level = i + 1
		}
	}
	return level
}

// Title is the name for the current level.
func (c *Companion) Title() string {
	i := c.Level() - 1
	if i < 0 {
		i = 0
	}
	if i >= len(titles) {
		i = len(titles) - 1
	}
	return titles[i]
}

// Progress reports how far into the current level the total is: tokens gained
// since the last threshold, and how many that level spans. At the top both are
// equal, so a bar reads as full rather than as broken.
func (c *Companion) Progress() (into, span int64) {
	level := c.Level()
	if level >= MaxLevel {
		return 1, 1
	}
	from, to := thresholds[level-1], thresholds[level]
	return c.Tokens - from, to - from
}

// Next is how many more tokens the next level needs, zero at the top.
func (c *Companion) Next() int64 {
	level := c.Level()
	if level >= MaxLevel {
		return 0
	}
	return thresholds[level] - c.Tokens
}

// Contributor is one model's share of the total.
type Contributor struct {
	Ref    string
	Tokens int64
}

// Top returns the models that fed it most, largest first.
func (c *Companion) Top(n int) []Contributor {
	out := make([]Contributor, 0, len(c.Models))
	for ref, t := range c.Models {
		out = append(out, Contributor{Ref: ref, Tokens: t})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens != out[j].Tokens {
			return out[i].Tokens > out[j].Tokens
		}
		return out[i].Ref < out[j].Ref
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// Age is how long the companion has been around, phrased so it reads on its own
// rather than needing a word appended.
func (c *Companion) Age() string {
	switch days := int(time.Since(c.Since).Hours() / 24); {
	case days < 1:
		return "today"
	case days == 1:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", days)
	}
}
