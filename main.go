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
	"time"

	tea "charm.land/bubbletea/v2"

	"raunen/internal/agent"
	"raunen/internal/companion"
	"raunen/internal/config"
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
	ag := agent.New(provider.New(p.BaseURL, p.Key(), model), reg, cfg.System)
	ag.SetContext(window)
	ag.SetRef(ref)
	ag.SetAutoSwitch(cfg.AutoSwitch)
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
		return oneShot(ag, strings.Join(args, " "))
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
	_, err = tea.NewProgram(ui.New(cfg, ag, root, ref, sess, comp)).Run()
	return err
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
