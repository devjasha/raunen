package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"

	"raunen/internal/config"
	"raunen/internal/provider"
)

// Version is stamped by main so /status can report it.
var Version = "dev"

// indent lines up continuation rows under the value column of a status row.
const indent = "            "

// providerStatus is what a probe found at one endpoint.
type providerStatus struct {
	name   string
	url    string
	models int
	needs  string
	err    bool
}

// statusMsg carries the result of probing every configured endpoint.
type statusMsg struct{ providers []providerStatus }

// probeProviders asks each endpoint whether it is actually there. It runs after
// the rest of /status has already been shown, because the answer needs the
// network and everything else does not — a report you wait for is a worse
// report.
func probeProviders(cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		var (
			mu  sync.Mutex
			out []providerStatus
			wg  sync.WaitGroup
		)
		for name, p := range cfg.Providers {
			wg.Add(1)
			go func(name string, p config.Provider) {
				defer wg.Done()
				st := providerStatus{name: name, url: p.BaseURL}
				if p.APIKeyEnv != "" && p.Key() == "" {
					st.needs = p.APIKeyEnv
				}
				ids, err := provider.ListModels(context.Background(), p.BaseURL, p.Key())
				if err != nil {
					st.err = true
				} else {
					st.models = len(ids)
				}
				mu.Lock()
				out = append(out, st)
				mu.Unlock()
			}(name, p)
		}
		wg.Wait()
		sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
		return statusMsg{providers: out}
	}
}

// status writes everything known without asking anyone: what model is in use,
// under what rules, how full its context is, and what is on the ladder.
func (m *Model) status() {
	row := func(k, v string) {
		m.push(entry{
			first: "  " + dimStyle.Render(fmt.Sprintf("%-10s", k)),
			cont:  "            ",
			text:  v,
		})
	}

	m.blank()
	m.push(entry{rule: true, stamp: "status"})

	row("version", dimStyle.Render(Version))

	window := "not declared"
	if c := m.ag.Context(); c > 0 {
		window = fmt.Sprintf("%d tokens", c)
	}
	row("model", modelStyle.Render(m.ref)+dimStyle.Render("  ·  "+window))
	row("mode", modeStyle(m.ag.Mode()).Render(m.ag.Mode().String()))

	used := dimStyle.Render("nothing sent yet")
	if m.ctxTokens > 0 {
		used = humanTokens(m.ctxTokens)
		if limit := m.contextLimit(); limit > 0 {
			pct := min(m.ctxTokens*100/limit, 100)
			used = usageStyle(pct).Render(fmt.Sprintf("%s of %s  (%d%%)",
				humanTokens(m.ctxTokens), humanTokens(limit), pct))
		}
	}
	row("context", used)

	turns := "turns"
	if m.sess.Turns() == 1 {
		turns = "turn"
	}
	sess := dimStyle.Render(m.sess.ID) +
		dimStyle.Render(fmt.Sprintf("  ·  %d %s", m.sess.Turns(), turns))
	if m.branch != "" {
		sess += dimStyle.Render("  ·  ") + branchStyle.Render("⎇ "+m.branch)
	}
	row("session", sess)
	row("cwd", dimStyle.Render(shortRoot(m.root)))

	// The ladder is what happens when this model runs out of room, so it is
	// worth seeing before it does rather than after.
	ladder := m.ag.Ladder()
	// A rung is only useful if it is roomier than what is in use; escalation
	// skips the rest. Counting them all would promise a move that cannot happen.
	usable := 0
	for _, c := range ladder {
		if c.Context == 0 || m.ag.Context() == 0 || c.Context > m.ag.Context() {
			usable++
		}
	}

	switch {
	case !m.cfg.AutoSwitch:
		row("ladder", dimStyle.Render("auto_switch off"))
	case len(ladder) == 0:
		row("ladder", errStyle.Render("nothing to switch to")+
			dimStyle.Render("  see the free models section of the README"))
	case usable == 0:
		row("ladder", askStyle.Render(fmt.Sprintf("%d models, none roomier than %d",
			len(ladder), m.ag.Context())))
	default:
		row("ladder", okStyle.Render(fmt.Sprintf("%d of %d models are roomier", usable, len(ladder))))
		for i, c := range ladder {
			if i == 3 {
				m.push(entry{first: "            ", cont: "            ",
					text: dimStyle.Render(fmt.Sprintf("… and %d more", len(ladder)-3))})
				break
			}
			win := "context not declared"
			if c.Context > 0 {
				win = fmt.Sprintf("%d", c.Context)
			}
			m.push(entry{
				first: "            ",
				cont:  "            ",
				text:  dimStyle.Render(fmt.Sprintf("%d. %s  ·  %s", i+1, c.Ref, win)),
			})
		}
	}

	// What is being held back explains a ladder that looks shorter than it is.
	if held := m.ag.Held(); len(held) > 0 {
		row("held back", askStyle.Render(fmt.Sprintf("%d models", len(held))))
		for i, n := range held {
			if i == 3 {
				m.push(entry{first: indent, cont: indent,
					text: dimStyle.Render(fmt.Sprintf("… and %d more", len(held)-3))})
				break
			}
			m.push(entry{first: indent, cont: indent + "  ",
				text: dimStyle.Render(n.Ref + "  ·  " + n.Reason)})
		}
	}

	subs := "off"
	if m.cfg.SubagentsEnabled() {
		subs = "on"
	}
	row("subagents", dimStyle.Render(subs))
	row("providers", dimStyle.Render("checking…"))
}

// showProviders appends what the probe found.
//
// What you can act on comes first — whether it answered, and what it wants —
// so the URL is what gets truncated on a narrow terminal rather than the
// reason a provider is not working.
func (m *Model) showProviders(list []providerStatus) {
	for _, p := range list {
		state, note := okStyle.Render(fmt.Sprintf("%d models", p.models)), ""
		switch {
		case p.err:
			state = errStyle.Render("unreachable")
		case p.models == 0:
			// It answered, so the endpoint is fine — there is just nothing
			// loaded to talk to, which is a different problem with a different
			// fix, and green would have implied otherwise.
			state = askStyle.Render("no models")
		}
		if p.needs != "" {
			note = "needs " + p.needs
		}

		// Padded before styling, so the escape codes do not throw the columns.
		left := fmt.Sprintf("%-14s", p.name)
		mid := fmt.Sprintf("%-13s", ansiPad(state, 13))
		text := left + mid + dimStyle.Render(note)
		if note == "" {
			text = left + mid + dimStyle.Render(p.url)
		}

		m.push(entry{first: indent, cont: indent + "  ", text: text})
	}
}

// ansiPad pads a styled string to a display width, since %-13s would count the
// escape codes and leave the columns ragged.
func ansiPad(s string, w int) string {
	if n := ansi.StringWidth(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// showMCP lists the configured MCP servers and how many tools each brought
// into the session. A server that is defined but not started shows its tools as
// zero, which is the clue that its command failed to launch.
func (m *Model) showMCP() tea.Cmd {
	m.blank()
	m.push(entry{rule: true, stamp: "mcp"})

	defs := m.cfg.MCP
	if len(defs) == 0 {
		m.add(dimStyle.Render("no MCP servers configured — see mcp.json"))
		m.add(dimStyle.Render("  add one with a text editor, or /status to see what loaded"))
		return nil
	}

	active := m.cfg.ActiveMCP()
	// Stable order for a list whose contents are meaningful.
	names := make([]string, 0, len(defs))
	for n := range defs {
		names = append(names, n)
	}
	sort.Strings(names)

	total := 0
	for _, n := range names {
		_, on := active[n]
		count := m.mcp[n]
		total += count
		state := dimStyle.Render("off")
		if on {
			if count > 0 {
				state = okStyle.Render(fmt.Sprintf("%d tools", count))
			} else {
				state = errStyle.Render("not started")
			}
		}
		m.add(dimStyle.Render(fmt.Sprintf("  %-16s %s", n, state)))
	}
	m.add(dimStyle.Render(fmt.Sprintf("  %d servers · %d tools", len(active), total)))
	return nil
}
