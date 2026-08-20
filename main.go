// Command raunen is a small terminal agent for local and remote LLMs.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"raunen/internal/agent"
	"raunen/internal/companion"
	"raunen/internal/config"
	"raunen/internal/mcp"
	"raunen/internal/provider"
	"raunen/internal/session"
	"raunen/internal/tools"
	"raunen/internal/ui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "raunen:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		modelRef   = flag.String("m", "", "model as provider/model (default: config `default`)")
		configPath = flag.String("c", "", "path to config file")
		printPath  = flag.Bool("config", false, "print the config path and exit")
		resume     = flag.String("resume", "", "resume a saved session by `id`")
		continued  = flag.Bool("continue", false, "resume the most recent session for this directory")
		listSess   = flag.Bool("sessions", false, "list saved sessions and exit")
		listRun    = flag.Bool("running", false, "list running raunen instances and exit")
		showVer    = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("raunen", version)
		return nil
	}
	if *printPath {
		fmt.Println(config.Path())
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	// Skills live in their own file, so a broken one is reported and skipped
	// rather than taken as a reason not to start: a prompt that cannot be
	// expanded is survivable in a way that a missing model is not.
	if skills, err := config.LoadSkills(); err != nil {
		fmt.Fprintln(os.Stderr, "raunen: skills not loaded —", err)
	} else {
		cfg.Skills = skills
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}

	if *listSess {
		return printSessions(root)
	}
	if *listRun {
		return printRunning()
	}

	// The session is opened before the model is chosen, because a resumed
	// conversation reopens on the model it was held with. Picking one up on a
	// different model is a surprise, and on a smaller one it can undo the very
	// reason it was switched.
	sess, err := openSession(*resume, *continued, root, "")
	if err != nil {
		return err
	}

	// Order of preference: -m for a one-off, then the session's own model, then
	// the configured default — which /model keeps up to date, so the last model
	// chosen is the one a new session starts on.
	ref := *modelRef
	if ref == "" && sess.Model != "" {
		if _, _, err := cfg.Resolve(sess.Model); err == nil {
			ref = sess.Model
		}
	}
	if ref == "" {
		ref = cfg.Default
	}
	if ref == "" {
		// Nothing configured: ask the endpoints what they have rather than
		// guessing at a model name the user may not have pulled.
		if ref, err = discoverModel(cfg); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "raunen: no default model set, using", ref)
	}
	p, model, err := cfg.Resolve(ref)
	if err != nil {
		return err
	}
	if sess.Model == "" {
		sess.Model = ref
	}

	// Tool results are capped relative to the model's context, so a single read
	// cannot evict the conversation around it.
	window := windowFor(cfg, p, ref, model)

	reg := tools.Default(root, tools.OutputBudget(window))
	// MCP servers add tools alongside the built-ins; failures are reported but do
	// not stop startup, so one broken server is not the whole session.
	mcpServers := startMCP(cfg)
	defer mcpServers.Close()
	reg = mcpServers.AddTo(reg)

	ag := agent.New(provider.New(p.BaseURL, p.Key(), model), reg, cfg.System)
	ag.SetContext(window)
	ag.SetRef(ref)
	ag.SetAutoSwitch(cfg.AutoSwitch)
	// Zero unless the user set it: turns are unbounded by default.
	ag.SetMaxSteps(cfg.MaxSteps)
	ladder := fallbacks(cfg)
	ag.SetFallbacks(ladder)
	if debug && len(ladder) > 0 {
		fmt.Fprintf(os.Stderr, dim("[ladder] %d models\n"), len(ladder))
		for i, c := range ladder {
			fmt.Fprintf(os.Stderr, dim("  %2d. %-52s ctx=%d\n"), i+1, c.Ref, c.Context)
		}
	}
	if cfg.SubagentsEnabled() {
		ag.EnableSubagents()
	}

	ag.Restore(sess.Messages)

	// A prompt on the command line runs one turn and exits, which keeps the
	// tool usable in pipes and scripts.
	if args := flag.Args(); len(args) > 0 {
		// Expanded here as well as in the UI, so a skill is worth defining for
		// the scripted case too — that is where the same instructions are most
		// often repeated.
		prompt, _ := cfg.ExpandSkills(strings.Join(args, " "))
		return oneShot(ag, prompt)
	}

	// Announce this instance so a picker can find it; tmux cannot, since it
	// only reports a pane's foreground process.
	done := sess.Register(ref)
	defer done()

	// The alternate screen is handed back on exit and the terminal is left as
	// it was found. The conversation is not replayed into it — sessions are
	// saved to disk instead, and resumed with --continue or /resume.
	comp := companion.Load()
	ui.Version = version
	m := ui.New(cfg, ag, root, ref, sess, comp)
	// Counts is read at render time, so a server that revises its toolset
	// mid-session shows up in /mcp and /status without a restart.
	m.SetMCPCounts(mcpServers.Counts)
	m.SetMCPLazy(mcpServers.catalog != nil)
	_, err = tea.NewProgram(m).Run()
	return err
}

// mcpServers is the set of MCP connections opened for this run. It exists so
// the subprocesses can be stopped together on exit, and so the tools they
// contributed can be folded into the registry deterministically.
type mcpServers struct {
	clients []*mcp.Client
	// counts maps a server name to how many tools it contributed, for /status and
	// /mcp. A server may refresh its toolset mid-session when it advertises
	// Tools.ListChanged, so onToolsChanged updates this under mu.
	counts map[string]int
	mu     sync.Mutex
	// catalog holds the tools kept out of the request when there are too many to
	// advertise, nil when they were all registered directly.
	catalog *mcp.Catalog
}

// Close stops every MCP server, freeing its process. The agent keeps running;
// any tool that pointed at a stopped server simply starts failing.
func (s *mcpServers) Close() {
	for _, c := range s.clients {
		c.Close()
	}
}

// lazyThreshold is how many MCP tools may be registered directly before they are
// put behind the search/select pair instead.
//
// The indirection is not free: two extra tools are advertised on every request,
// and reaching a server tool costs two round trips before it can be called. Below
// a handful of tools that overhead is larger than the schemas it saves, so a
// small server is simply registered. It is the large catalogue — the case that
// makes a local model unusable — that the indirection is for.
const lazyThreshold = 5

// AddTo copies the registry and makes the servers' tools reachable, renaming on
// collision so two servers that both expose "search" do not overwrite one
// another. The model sees each tool prefixed with its server.
//
// Small toolsets are registered directly. A large one is held in a catalogue and
// reached through mcp_search_tools and mcp_select_tool, so a server advertising a
// hundred tools costs two schemas per request instead of a hundred — which is the
// difference between a local 8k model working and not.
func (s *mcpServers) AddTo(reg *tools.Registry) *tools.Registry {
	out := reg.Clone()

	// Name every tool up front, so the decision to go lazy is made against the
	// real total and the names are identical either way.
	type named struct {
		server string
		tool   tools.Tool
	}
	var all []named
	taken := map[string]bool{}
	for _, c := range s.clients {
		name := c.Name()
		for _, t := range c.Tools() {
			tname := t.Name
			// Two servers may both expose "search"; disambiguate rather than let
			// the second overwrite the first, which would silently reroute calls.
			for out.Has(tname) || taken[tname] {
				tname = name + "_" + t.Name
			}
			taken[tname] = true
			t.Name = tname
			all = append(all, named{server: name, tool: t})
		}
		// A server's resource and prompt tools come along with its tools,
		// gated on the capability each advertises. They are skipped entirely
		// when the server does not offer the feature, so a tool that can only
		// ever answer "method not found" is never shown.
		for _, t := range c.ResourcePromptTools() {
			tname := t.Name
			for out.Has(tname) || taken[tname] {
				tname = name + "_" + t.Name
			}
			taken[tname] = true
			t.Name = tname
			all = append(all, named{server: name, tool: t})
		}
		s.mu.Lock()
		s.counts[name] = 0
		s.mu.Unlock()
	}
	for _, n := range all {
		s.mu.Lock()
		s.counts[n.server]++
		s.mu.Unlock()
	}

	if len(all) <= lazyThreshold {
		for _, n := range all {
			out.Add(n.tool)
		}
		s.watch(nil)
		return out
	}

	// Selecting a tool adds it to this registry, which the agent re-reads on
	// every step — so a tool becomes callable on the request after it is chosen.
	cat := mcp.NewCatalog(out.Add)
	for _, n := range all {
		cat.Add(n.tool)
	}
	out.Add(cat.SearchTool())
	out.Add(cat.SelectTool())
	s.catalog = cat
	s.watch(cat)
	return out
}

// watch keeps the tool counts, and the catalogue when there is one, up to date
// as servers revise what they offer.
func (s *mcpServers) watch(cat *mcp.Catalog) {
	for _, c := range s.clients {
		c.SetOnToolsChanged(func(server string, ts []tools.Tool) {
			s.mu.Lock()
			s.counts[server] = len(ts)
			s.mu.Unlock()
			if cat != nil {
				cat.Replace(server, ts)
			}
		})
	}
}

// Counts returns a snapshot of how many tools each server contributed, safe to
// read from the UI goroutine while a refresh may be updating it.
func (s *mcpServers) Counts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.counts))
	for k, v := range s.counts {
		out[k] = v
	}
	return out
}

// startMCP launches every enabled MCP server and collects those that came up.
// A server that fails to start is reported and skipped, because a missing tool
// is survivable while a missing model is not.
func startMCP(cfg *config.Config) *mcpServers {
	ss := &mcpServers{counts: map[string]int{}}
	defs := cfg.ActiveMCP()
	if len(defs) == 0 {
		return ss
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for name, def := range defs {
		c, err := mcp.Start(ctx, name, mcp.Server{
			Command: def.Command,
			Args:    def.Args,
			Env:     def.Env,
			Type:    def.Type,
			URL:     def.URL,
			Headers: def.Headers,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "raunen: mcp %q not started — %s\n", name, err)
			continue
		}
		ss.clients = append(ss.clients, c)
	}
	return ss
}

// discoverModel asks the configured endpoints what they serve and picks one,
// preferring anything served locally: it costs nothing to run and needs no key.
func discoverModel(cfg *config.Config) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var local, remote []string
	for name, p := range cfg.Providers {
		ids, err := provider.ListModels(ctx, p.BaseURL, p.Key())
		if err != nil || len(ids) == 0 {
			continue
		}
		sort.Strings(ids)
		ref := name + "/" + ids[0]
		if isLocal(p.BaseURL) {
			local = append(local, ref)
		} else {
			remote = append(remote, ref)
		}
	}

	sort.Strings(local)
	sort.Strings(remote)
	if len(local) > 0 {
		return local[0], nil
	}
	if len(remote) > 0 {
		return remote[0], nil
	}
	return "", fmt.Errorf("no models found — is a provider running? set \"default\" in %s", config.Path())
}

func isLocal(baseURL string) bool {
	return strings.Contains(baseURL, "localhost") || strings.Contains(baseURL, "127.0.0.1")
}

// fallbacks resolves the escalation ladder, skipping entries that do not
// resolve so one bad reference cannot break startup.
func fallbacks(cfg *config.Config) []agent.Candidate {
	out := make([]agent.Candidate, 0, len(cfg.Fallback))
	for _, ref := range cfg.Fallback {
		p, model, err := cfg.Resolve(ref)
		if err != nil {
			fmt.Fprintln(os.Stderr, "raunen: ignoring fallback:", err)
			continue
		}
		if p.APIKeyEnv != "" && p.Key() == "" {
			fmt.Fprintf(os.Stderr, "raunen: ignoring fallback %s: %s is not set\n", ref, p.APIKeyEnv)
			continue
		}
		out = append(out, agent.Candidate{
			Ref:     ref,
			Client:  provider.New(p.BaseURL, p.Key(), model),
			Context: windowFor(cfg, p, ref, model),
		})
	}
	if cfg.FreeFallback {
		out = append(out, freeModels(cfg)...)
	}
	return out
}

// maxFreeRungs caps how many free models are added to the ladder.
//
// A catalogue can be enormous — NVIDIA lists over a hundred, including vision
// and audio models that cannot answer a chat turn at all. Walking dozens of
// them on a rate limit would take longer than failing, so the ladder keeps the
// most promising few. Anything specific belongs in "fallback", which is not
// capped, and /model can still reach every model by hand.
const maxFreeRungs = 8

// freeModels asks the providers which of their models cost nothing and turns
// them into ladder rungs, roomiest first.
//
// Free tiers are rate limited rather than billed, so a ladder of them is a way
// to keep going when one refuses — not a way to spend more. Only providers that
// report pricing contribute; a local endpoint reports none and is skipped,
// which is correct, since a local model is already free and already in use.
func freeModels(cfg *config.Config) []agent.Candidate {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var out []agent.Candidate
	for name, p := range cfg.Providers {
		// A catalogue usually lists without a key while completions do not
		// work without one. Including such a model would turn one failure into
		// eighteen, as the ladder walked through every model it cannot call.
		if p.APIKeyEnv != "" && p.Key() == "" {
			continue
		}
		infos, err := provider.ListModelsDetailed(ctx, p.BaseURL, p.Key())
		if err != nil {
			continue
		}
		// Three ways a model counts as free: the catalogue prices it at zero,
		// it is served from this machine, or the endpoint was declared free
		// because it does not publish prices at all.
		local := isLocal(p.BaseURL)
		declared := p.Free || local
		for _, m := range infos {
			if !m.Free && !declared {
				continue
			}
			// A music or image model on a ladder is a guaranteed failure.
			if !m.Chat {
				continue
			}
			ref := name + "/" + m.ID
			// A declared window wins, then whatever the endpoint reported,
			// then Ollama's native API, which is the only one that knows what
			// a local model is actually being served with.
			window := firstNonZero(cfg.ModelContext(ref), m.Context)
			if window == 0 && local {
				window = provider.OllamaContext(ctx, p.BaseURL, m.ID)
			}
			// The provider default comes last: it describes an endpoint, not a
			// model, and applying it to every model would make them all look
			// identical and the ladder pointless.
			window = firstNonZero(window, cfg.ProviderContext(ref))
			// A rung with no known window cannot be judged an upgrade. For a
			// discovered free model that is reason enough to leave it out; for
			// an endpoint the user declared free it is not, since they asked
			// for it — those are kept and sorted last, and declaring a context
			// under "models" is what makes them useful.
			if window == 0 && !p.Free {
				continue
			}
			out = append(out, agent.Candidate{
				Ref:     ref,
				Client:  provider.New(p.BaseURL, p.Key(), m.ID),
				Context: window,
			})
		}
	}
	// Roomiest first, with unknown windows last: they cannot be shown to be an
	// upgrade, so they are a last resort rather than a first choice.
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Context == 0) != (out[j].Context == 0) {
			return out[j].Context == 0
		}
		return out[i].Context > out[j].Context
	})
	if len(out) > maxFreeRungs {
		out = out[:maxFreeRungs]
	}
	return out
}

func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

// windowFor works out a model's context window: what was declared for it, then
// what the endpoint reports, then what its native API says, then the provider's
// default. Shared so the model in use and every rung of the ladder are measured
// the same way — a rung with no window cannot be judged an upgrade, which is how
// a perfectly good fallback ends up looking useless.
func windowFor(cfg *config.Config, p config.Provider, ref, model string) int {
	if w := cfg.ModelContext(ref); w > 0 {
		return w
	}
	if w := discoverContext(p, model); w > 0 {
		return w
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if w := provider.OllamaContext(ctx, p.BaseURL, model); w > 0 {
		return w
	}
	return cfg.ProviderContext(ref)
}

// discoverContext asks an endpoint how big a model's window is, for providers
// that say. It saves declaring by hand what the server already knows.
func discoverContext(p config.Provider, model string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	infos, err := provider.ListModelsDetailed(ctx, p.BaseURL, p.Key())
	if err != nil {
		return 0
	}
	for _, m := range infos {
		if m.ID == model {
			return m.Context
		}
	}
	return 0
}

// openSession picks up a saved conversation when asked, and otherwise starts a
// fresh one.
func openSession(resume string, continued bool, root, model string) (*session.Session, error) {
	switch {
	case resume != "":
		s, err := session.Load(resume)
		if err != nil {
			return nil, fmt.Errorf("resuming %s: %w", resume, err)
		}
		return s, nil
	case continued:
		s, err := session.Latest(root)
		if err != nil {
			return nil, err
		}
		if s == nil {
			// Nothing to continue is not an error; just start fresh.
			return session.New(root, model), nil
		}
		return s, nil
	}
	return session.New(root, model), nil
}

// printRunning writes one live instance per line, tab-separated, for the tmux
// picker to parse. Machine-readable rather than pretty: the picker does the
// formatting.
func printRunning() error {
	list, err := session.ListRunning()
	if err != nil {
		return err
	}
	for _, r := range list {
		title := r.Title
		if title == "" {
			title = "(new session)"
		}
		fmt.Printf("%s\t%d\t%s\t%s\t%s\n", r.Pane, r.PID, filepath.Base(r.Root), r.Model, title)
	}
	return nil
}

func printSessions(root string) error {
	list, err := session.List("", 50)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("no saved sessions")
		return nil
	}
	for _, s := range list {
		here := " "
		if s.Root == root {
			here = "*"
		}
		fmt.Printf("%s %-22s %-10s %2d turns  %s\n", here, s.ID, s.Age(), s.Turns(), s.Title)
	}
	return nil
}

// version is stamped at build time with -ldflags "-X main.version=...". A
// source build leaves it as "dev", which is the honest answer.
var version = "dev"

// debug enables token accounting on stderr.
var debug = os.Getenv("RAUNEN_DEBUG") != ""

// dim greys text, but only when stderr is a terminal, so redirected output
// stays free of escape codes.
func dim(s string) string {
	if fi, err := os.Stderr.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return s
	}
	return "\x1b[90m" + s + "\x1b[0m"
}

// oneShot runs a single turn and streams plain text to stdout, with tool
// activity on stderr so stdout stays pipeable.
func oneShot(ag *agent.Agent, prompt string) error {
	events := make(chan agent.Event, 64)
	go ag.Run(context.Background(), prompt, events)

	for ev := range events {
		switch e := ev.(type) {
		case agent.TextDelta:
			fmt.Print(e.Text)
		case agent.ReasoningDelta:
			// Thinking goes to stderr so stdout stays clean for pipes.
			fmt.Fprint(os.Stderr, dim(e.Text))
		case agent.Usage:
			// Token accounting is the first thing worth seeing when replies go
			// wrong, so it is available on stderr behind RAUNEN_DEBUG.
			if debug {
				fmt.Fprintf(os.Stderr, dim("\n[usage] prompt=%d completion=%d total=%d\n"),
					e.Prompt, e.Completion, e.Total)
			}
		case agent.Switched:
			fmt.Fprintf(os.Stderr, dim("[switched to %s — %s]\n"), e.To, e.Reason)
		case agent.Trimmed:
			fmt.Fprintf(os.Stderr, dim("[dropped %d earlier messages to fit the context]\n"), e.Messages)
		case agent.ToolStart:
			fmt.Fprintf(os.Stderr, "⏺ %s\n", e.Name)
		case agent.ToolEnd:
			if e.Err != nil {
				fmt.Fprintf(os.Stderr, "  ↳ %v\n", e.Err)
			}
		case agent.TurnEnd:
			fmt.Println()
		case agent.Failed:
			fmt.Println()
			return e.Err
		}
	}
	return nil
}
