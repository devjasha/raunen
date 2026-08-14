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
)

// picker is the model chooser: a small overlay above the input, listing what
// the configured endpoints actually serve. The list is fetched rather than
// configured, so it reflects what is installed right now.
type picker struct {
	needsKey map[string]string
	all      []string
	filtered []string
	filter   string
	cursor   int
	loading  bool
	err      error
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
			return modelsMsg{err: fmt.Errorf("no models found (unreachable: %s)",
				strings.Join(errs, ", "))}
		}
		return modelsMsg{models: all, needsKey: needs}
	}
}

func (p *picker) setModels(models []string) {
	p.all = models
	p.loading = false
	p.apply()
}

// apply refreshes the visible list after the filter changes.
func (p *picker) apply() {
	p.filtered = p.filtered[:0]
	needle := strings.ToLower(p.filter)
	for _, m := range p.all {
		if needle == "" || strings.Contains(strings.ToLower(m), needle) {
			p.filtered = append(p.filtered, m)
		}
	}
	if p.cursor >= len(p.filtered) {
		p.cursor = max(0, len(p.filtered)-1)
	}
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
	head := "switch model"
	if p.loading {
		head = "switch model — loading…"
	}
	b.WriteString(dimStyle.Render(head))
	if p.filter != "" {
		b.WriteString(dimStyle.Render("  /" + p.filter))
	}
	b.WriteString("\n")

	switch {
	case p.err != nil:
		b.WriteString(errStyle.Render(ansi.Truncate(p.err.Error(), inner, "…")))
	case p.loading:
		b.WriteString(dimStyle.Render("asking the configured endpoints…"))
	case len(p.filtered) == 0:
		b.WriteString(dimStyle.Render("nothing matches " + p.filter))
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
