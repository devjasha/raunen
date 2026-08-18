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
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Titles run from quiet to loud, which is the joke the name is already making:
// raunen is to murmur. A new dragon barely makes a sound.
//
// Ten names over five hundred levels, so a title covers a band rather than a
// level: what changes every level is the number, and what changes rarely is
// what it is called. Reaching the next name is the thing worth noticing.
var titles = []string{
	"Hush", "Whisper", "Murmur", "Rumour", "Echo",
	"Chant", "Chorus", "Bellow", "Roar", "Thunder",
}

// MaxLevel is the top of the ladder, after which the companion can prestige.
const MaxLevel = 500

// levelBase scales the whole curve. Level 2 costs this much, and every level
// after it costs a little more than the one before.
const levelBase = 10_000

// TokensForLevel is the total a level begins at, so a caller can name a level
// it has not reached.
//
// The curve is quadratic: base*(n-1)^2. A table would need five hundred rows to
// say the same thing and would be wrong the first time anyone edited it.
//
// Quadratic rather than the doubling the first ten levels used, because
// doubling to level 500 lands past the number of tokens that will ever be
// generated. Here each level costs a fixed 20k more than the last, so the early
// ones still arrive within a session — level 10 at 810k, reachable in a day —
// while level 500 at 2.49 billion is roughly a year of heavy daily use. It is
// meant to be a long climb; prestige is meant to mean something.
func TokensForLevel(level int) int64 {
	if level < 1 {
		level = 1
	}
	if level > MaxLevel {
		level = MaxLevel
	}
	n := int64(level - 1)
	return levelBase * n * n
}

// Companion is the saved state. Counts are cumulative and never reset — a new
// session adds to them rather than starting over.
type Companion struct {
	Tokens int64 `json:"tokens"`
	Turns  int   `json:"turns"`
	Tools  int   `json:"tools"`
	Tasks  int   `json:"tasks"`
	// Prestige is how many times the ladder has been climbed to the top and
	// begun again. Tokens resets on each one; Lifetime does not, so the total
	// spent is never lost.
	Prestige int `json:"prestige,omitempty"`
	// Lifetime is every token ever counted, across prestiges. Tokens is the
	// current climb; this is the whole history.
	Lifetime int64 `json:"lifetime,omitempty"`
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
	c.Lifetime += tokens
	if c.Models == nil {
		c.Models = map[string]int64{}
	}
	c.Models[ref] += tokens
	return before, c.Level()
}

// AtTop reports whether the ladder has been climbed and prestige is available.
func (c *Companion) AtTop() bool { return c.Level() >= MaxLevel }

// Ascend starts the climb again, keeping the history. It is deliberately not
// automatic: reaching five hundred is the achievement, and having it silently
// reset to level one the moment it lands would take that away rather than
// reward it. The user asks for this.
func (c *Companion) Ascend() bool {
	if !c.AtTop() {
		return false
	}
	c.Prestige++
	c.Tokens = 0
	// Models is what fed this climb, so it starts over with it. Lifetime and
	// the counters carry, since they are the record rather than the progress.
	c.Models = map[string]int64{}
	return true
}

// Level is how far up the ladder the total has carried it. It inverts the
// curve rather than scanning it: this is called on every render, and five
// hundred comparisons a frame to learn a number that changes once an hour is a
// poor trade.
func (c *Companion) Level() int {
	if c.Tokens < levelBase {
		return 1
	}
	// n = floor(sqrt(tokens/base)) + 1, guarded against float error near an
	// exact threshold by stepping to whichever side actually satisfies it.
	n := int(math.Sqrt(float64(c.Tokens)/float64(levelBase))) + 1
	if n > MaxLevel {
		// Past the top the curve keeps going but the ladder does not.
		return MaxLevel
	}
	for n > 1 && TokensForLevel(n) > c.Tokens {
		n--
	}
	for n < MaxLevel && TokensForLevel(n+1) <= c.Tokens {
		n++
	}
	return n
}

// Title is the name for the current level. Ten names spread over the ladder, so
// each covers a band of fifty.
func (c *Companion) Title() string {
	return TitleForLevel(c.Level())
}

// TitleForLevel names a level, so a caller can say what the next band is called
// without reaching it.
func TitleForLevel(level int) string {
	if level < 1 {
		level = 1
	}
	if level > MaxLevel {
		level = MaxLevel
	}
	band := (level - 1) * len(titles) / MaxLevel
	if band >= len(titles) {
		band = len(titles) - 1
	}
	return titles[band]
}

// Progress reports how far into the current level the total is: tokens gained
// since the last threshold, and how many that level spans. At the top both are
// equal, so a bar reads as full rather than as broken.
func (c *Companion) Progress() (into, span int64) {
	level := c.Level()
	if level >= MaxLevel {
		return 1, 1
	}
	from, to := TokensForLevel(level), TokensForLevel(level+1)
	return c.Tokens - from, to - from
}

// Next is how many more tokens the next level needs, zero at the top.
func (c *Companion) Next() int64 {
	level := c.Level()
	if level >= MaxLevel {
		return 0
	}
	return TokensForLevel(level+1) - c.Tokens
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
