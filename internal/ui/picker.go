package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"raunen/internal/config"
	"raunen/internal/provider"
	"raunen/internal/vcs"
)

// pickKind says what a picker is choosing between. The overlay is the same
// searchable list either way; only the nouns and the few model-specific
// decorations differ.
type pickKind int

const (
	pickModel pickKind = iota
	pickBranch
)

// picker is the chooser: a small overlay above the input, listing what the
// configured endpoints actually serve, or what branches the repository has.
// The list is fetched rather than configured, so it reflects what is there
// right now.
type picker struct {
	kind     pickKind
	needsKey map[string]string
	all      []string
	filtered []string
	filter   string
	cursor   int
	loading  bool
	err      error
	// cfg lets the list float pinned models to the top and mark them.
	cfg *config.Config
}

// noun names what is being chosen, for the counts in the search line.
func (p *picker) noun(n int) string {
	if p.kind == pickBranch {
		if n == 1 {
			return "branch"
		}
		return "branches"
	}
	if n == 1 {
		return "model"
	}
	return "models"
}

// branchesMsg carries the repository's branches, or the reason there are none
// to show.
type branchesMsg struct {
	branches []string
	err      error
}

// fetchBranches asks git what branches exist. Off the main loop because it
// shells out, which is fast but not instant on a large repository.
func fetchBranches(root string) tea.Cmd {
	return func() tea.Msg {
		list, err := vcs.Branches(root)
		if err != nil {
			return branchesMsg{err: err}
		}
		return branchesMsg{branches: list}
	}
}

// modelsMsg carries the result of asking every provider what it has.
type modelsMsg struct {
	models []string
	// needsKey maps a provider to the environment variable it wants, for those
	// that declared one and did not find it set. Their catalogue usually lists
	// fine while completions would fail, so it is worth saying so before the
	// model is chosen rather than after it 401s.
	needsKey map[string]string
	err      error
}

const (
	// pickerRows is how many models are visible at once. Small enough to read
	// as a popup rather than a second screen.
	pickerRows = 8
	// addKeyPrefix marks an entry that opens the key prompt rather than
	// switching model. It reads as a sentence in the list and still matches the
	// provider name when filtering.
	addKeyPrefix = "⊕ add a key for "
	// newBranchPrefix marks the entry that creates the branch being searched
	// for. Typing a name nobody has yet is how a branch is usually made, so the
	// list offers it rather than reporting that nothing matches.
	newBranchPrefix = "⊕ create branch "
	// favMarker marks a pinned model at the top of the chooser.
	favMarker = "★ "
)

var (
	pickerBorder   = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	pickerSelected = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true)
)

// fetchModels asks each configured provider for its catalogue, in parallel so
// one unreachable endpoint does not hold up the rest.
func fetchModels(cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		type result struct {
			models []string
			err    error
		}

		var (
			mu    sync.Mutex
			all   []string
			errs  []string
			needs = map[string]string{}
			wg    sync.WaitGroup
			ctx   = context.Background()
		)

		for name, p := range cfg.Providers {
			wg.Add(1)
			go func(name string, p config.Provider) {
				defer wg.Done()
				ids, err := provider.ListModels(ctx, p.BaseURL, p.Key())
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					// An endpoint that is not running is ordinary, not fatal:
					// note it and carry on with whatever else answered.
					errs = append(errs, name)
					// A catalogue that needs a key cannot be listed without
					// one, so those providers would vanish from the chooser
					// entirely — and adding the key is exactly what the user
					// came here to do. Offer that instead of nothing.
					if p.APIKeyEnv != "" && p.Key() == "" {
						all = append(all, addKeyPrefix+name)
					}
					return
				}
				if p.APIKeyEnv != "" && p.Key() == "" {
					needs[name] = p.APIKeyEnv
				}
				for _, id := range ids {
					all = append(all, name+"/"+id)
				}
			}(name, p)
		}
		wg.Wait()

		sort.Strings(all)
		if len(all) == 0 {
			sort.Strings(errs)
			return modelsMsg{err: fmt.Errorf("no models found (unreachable: %s)",
				strings.Join(errs, ", "))}
		}
		return modelsMsg{models: all, needsKey: needs}
	}
}

// sortFavourites moves pinned models above the rest, preserving their order
// and the relative order of everything else. Where they sit matters: the top
// of a long list is the place a favourite is actually useful.
func sortFavourites(models []string, favs []string) []string {
	if len(favs) == 0 {
		return models
	}
	known := make(map[string]bool, len(favs))
	for _, f := range favs {
		known[f] = true
	}
	out := make([]string, 0, len(models))
	for _, f := range favs {
		for _, m := range models {
			if m == f {
				out = append(out, m)
				break
			}
		}
	}
	for _, m := range models {
		if !known[m] {
			out = append(out, m)
		}
	}
	return out
}

func (p *picker) setModels(models []string) {
	var favs []string
	if p.cfg != nil {
		favs = p.cfg.Favourites
	}
	p.all = sortFavourites(models, favs)
	p.loading = false
	p.apply()
}

// setItems fills a picker whose entries have no favourites to float: branches
// arrive already in the order they should be read.
func (p *picker) setItems(items []string) {
	p.all = items
	p.loading = false
	p.apply()
}

// apply refreshes the visible list after the query changes.
//
// Model names are long and structured — "openrouter/nvidia/nemotron-3.5-
// lightning:free" — so matching is more forgiving than a substring test. Terms
// are separated by spaces and all must match, which makes "nvidia free" a
// useful query, and each term matches either as a substring or as a subsequence
// so "nemlight" finds nemotron-lightning.
func (p *picker) apply() {
	p.filtered = p.filtered[:0]

	terms := strings.Fields(strings.ToLower(p.filter))
	if len(terms) == 0 {
		p.filtered = append(p.filtered, p.all...)
		p.clampCursor()
		return
	}

	// Substring matches are what someone typing a name expects to see first;
	// subsequence matches are the helpful surprise and belong below them.
	var exact, fuzzy []string
	for _, m := range p.all {
		lower := strings.ToLower(m)
		hit, allSubstring := true, true
		for _, t := range terms {
			if strings.Contains(lower, t) {
				continue
			}
			allSubstring = false
			if !subsequence(lower, t) {
				hit = false
				break
			}
		}
		if !hit {
			continue
		}
		if allSubstring {
			exact = append(exact, m)
		} else {
			fuzzy = append(fuzzy, m)
		}
	}
	p.filtered = append(append(p.filtered, exact...), fuzzy...)
	p.offerNew(terms)
	p.clampCursor()
}

// offerNew appends the create-a-branch entry when the search names a branch
// that does not exist yet. It goes last so it can never be picked by accident
// when an existing branch matches, and it is skipped for a query that could
// not be a branch name anyway.
func (p *picker) offerNew(terms []string) {
	if p.kind != pickBranch || p.loading || len(terms) != 1 {
		return
	}
	name := strings.TrimSpace(p.filter)
	if !validBranchName(name) {
		return
	}
	for _, b := range p.all {
		if b == name {
			return
		}
	}
	p.filtered = append(p.filtered, newBranchPrefix+name)
}

// validBranchName is a deliberately loose check: git refuses what it dislikes,
// and reports it better than this could. It only rules out the shapes that
// would make the offer look silly.
func validBranchName(s string) bool {
	if s == "" || strings.ContainsAny(s, " ~^:?*[\\") || strings.Contains(s, "..") {
		return false
	}
	return !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "/") && !strings.HasSuffix(s, "/")
}

func (p *picker) clampCursor() {
	if p.cursor >= len(p.filtered) {
		p.cursor = max(0, len(p.filtered)-1)
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// subsequence reports whether every character of needle appears in haystack in
// order, which is what makes an abbreviation find a long name.
func subsequence(haystack, needle string) bool {
	i := 0
	for _, r := range haystack {
		if i < len(needle) && rune(needle[i]) == r {
			i++
		}
	}
	return i == len(needle)
}

func (p *picker) move(d int) {
	if len(p.filtered) == 0 {
		return
	}
	p.cursor = (p.cursor + d + len(p.filtered)) % len(p.filtered)
}

func (p *picker) selected() string {
	if p.cursor < 0 || p.cursor >= len(p.filtered) {
		return ""
	}
	return p.filtered[p.cursor]
}

// cursorCol is where the caret sits in the search line, so it looks like the
// text field it is.
func (p *picker) cursorCol() int {
	return lipgloss.Width("search › ") + lipgloss.Width(p.filter)
}

// height is how many rows the overlay occupies, so the transcript above it can
// give up exactly that much and the input stays put.
func (p *picker) height() int {
	// Border top and bottom, plus the filter line.
	return min(len(p.filtered), pickerRows) + 3
}

// render draws the picker at the given width. Only foreground colours are used,
// so the overlay stays transparent like everything else.
func (p *picker) render(width int, current string) string {
	inner := max(20, width-4)

	var b strings.Builder

	// A visible search line, always: a list that quietly filters when you type
	// is a feature nobody finds.
	b.WriteString(dimStyle.Render("search ") + promptStyle.Render("› ") + p.filter)
	switch {
	case p.loading:
		b.WriteString(dimStyle.Render("   loading…"))
	case p.filter != "":
		b.WriteString(dimStyle.Render(fmt.Sprintf("   %d of %d", len(p.filtered), len(p.all))))
	case len(p.all) > 0:
		b.WriteString(dimStyle.Render(fmt.Sprintf("   %d %s", len(p.all), p.noun(len(p.all)))))
	}
	b.WriteString("\n")

	switch {
	case p.err != nil:
		b.WriteString(errStyle.Render(ansi.Truncate(p.err.Error(), inner, "…")))
	case p.loading:
		if p.kind == pickBranch {
			b.WriteString(dimStyle.Render("reading the repository…"))
		} else {
			b.WriteString(dimStyle.Render("asking the configured endpoints…"))
		}
	case len(p.filtered) == 0:
		b.WriteString(dimStyle.Render("nothing matches"))
	default:
		// Scroll the window so the cursor stays visible.
		start := 0
		if p.cursor >= pickerRows {
			start = p.cursor - pickerRows + 1
		}
		end := min(len(p.filtered), start+pickerRows)
		for i := start; i < end; i++ {
			line := p.filtered[i]
			marker := "  "
			if line == current {
				marker = "• "
			} else if p.kind == pickModel && p.cfg != nil && p.cfg.IsFavourite(line) {
				marker = favMarker
			}
			// Flag a model whose provider has no key: its catalogue lists but
			// its completions will not run.
			suffix := ""
			if p.needsKey != nil {
				if prov, _, ok := strings.Cut(line, "/"); ok {
					if env := p.needsKey[prov]; env != "" {
						suffix = "  needs " + env
					}
				}
			}
			text := ansi.Truncate(marker+line+suffix, inner, "…")
			if i == p.cursor {
				b.WriteString(pickerSelected.Render("❯ " + text))
			} else {
				b.WriteString(dimStyle.Render("  " + text))
			}
			if i < end-1 {
				b.WriteString("\n")
			}
		}
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(pickerBorder.GetForeground()).
		Padding(0, 1).
		Width(max(10, width-2)).
		Render(b.String())
}
