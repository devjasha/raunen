// Command raunen is a small terminal agent for local and remote LLMs.
package main

import (
	"context"
	"errors"
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
	"raunen/internal/attach"
	"raunen/internal/companion"
	"raunen/internal/config"
	"raunen/internal/instructions"
	"raunen/internal/mcp"
	"raunen/internal/permission"
	"raunen/internal/provider"
	"raunen/internal/session"
	"raunen/internal/skills"
	"raunen/internal/tools"
	"raunen/internal/ui"
)

func main() {
	err := run()
	if err == nil {
		return
	}
	// A run that carried its own status says what it should be — 130 for a turn
	// stopped by ctrl+c, which is what lets a script tell an interruption from a
	// model that failed. Everything else is an ordinary failure.
	var ex exitError
	if errors.As(err, &ex) {
		if !ex.quiet {
			fmt.Fprintln(os.Stderr, "raunen:", err)
		}
		os.Exit(ex.code)
	}
	fmt.Fprintln(os.Stderr, "raunen:", err)
	os.Exit(1)
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
		asJSON     = flag.Bool("json", false, "with a prompt, print one JSON result instead of prose")
		noSave     = flag.Bool("no-save", false, "with a prompt, do not save the session")
	)
	var images imagePaths
	flag.Var(&images, "image", "attach an image `file` to the prompt (repeatable)")
	flag.Parse()

	// A subcommand rather than a flag: `raunen acp` starts a server that speaks
	// a protocol on stdio and never draws anything, which is a different mode of
	// operation from a flag that adjusts a run.
	acpMode := flag.Arg(0) == "acp"

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
	if saved, err := config.LoadSkills(); err != nil {
		fmt.Fprintln(os.Stderr, "raunen: skills not loaded —", err)
	} else {
		cfg.Skills = saved
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}

	// SKILL.md directories, here and in the places other agents keep them, so a
	// repository that already has skills works without being adapted. Folded in
	// beside skills.json, which stays supported.
	found := skills.Load(root, skills.UserDir(filepath.Dir(config.Path())))
	for _, p := range found.Problems {
		fmt.Fprintln(os.Stderr, "raunen: skill skipped —", p)
	}
	cfg.AddSkills(asConfigSkills(found))

	// Serving the protocol takes over stdout, so it happens before anything
	// that might print. Skills, project instructions and MCP are resolved per
	// session against the directory the editor asks for, not this one.
	if acpMode {
		return serveACP(cfg, *modelRef)
	}

	if *listSess {
		return printSessions(root)
	}
	if *listRun {
		return printRunning()
	}

	// Started here, as early as the config allows, because connecting is a
	// network round trip per server and nothing below this depends on the
	// result. By the time the tools are wanted the handshakes are usually done.
	// Failures are reported but do not stop startup: one broken server is not
	// the whole session.
	// Silent only when this run will draw: a prompt on the command line means a
	// one-shot, which prints to stderr and never opens the alternate screen, so
	// a headless login can still be completed there by hand.
	mcpServers := startMCP(cfg, len(flag.Args()) == 0)
	defer mcpServers.Close()

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
	// What the project says about itself, from AGENTS.md here and in the
	// directories above. Read after the agent exists so the block can be
	// replaced later without rebuilding it.
	instr := instructions.Load(root, config.InstructionsPath())
	ag.SetProject(instr.Prompt(root))
	// Standing rules about what may run without asking. A malformed rule is
	// reported and dropped rather than fatal — one typo should not take the
	// other nineteen with it — and dropping fails closed, back to asking.
	perms, problems := permission.Parse(cfg.Permissions)
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, "raunen:", p)
	}
	ag.SetPermissions(perms)
	ag.SetContext(window)
	ag.SetRef(ref)
	ag.SetAutoSwitch(cfg.AutoSwitch)
	// Zero unless the user set it: turns are unbounded by default.
	ag.SetMaxSteps(cfg.MaxSteps)
	if cfg.SubagentsEnabled() {
		ag.EnableSubagents()
	}

	ag.Restore(sess.Messages)

	// A prompt on the command line runs one turn and exits, which keeps the
	// tool usable in pipes and scripts.
	if args := flag.Args(); len(args) > 0 {
		reg = mcpServers.AddTo(reg)
		ag.SetTools(reg)
		ag.SetFallbacks(buildLadder(cfg))
		// Expanded here as well as in the UI, so a skill is worth defining for
		// the scripted case too — that is where the same instructions are most
		// often repeated.
		prompt, _ := cfg.ExpandSkills(strings.Join(args, " "))
		// Loaded before the turn starts so a bad path fails immediately, with
		// its own message, rather than after the model has been paid to read
		// half a question.
		attached, err := images.load()
		if err != nil {
			return err
		}
		return oneShot(ag, sess, prompt, oneShotOpts{json: *asJSON, save: !*noSave, images: attached})
	}

	// The two slow parts of startup, finished off the critical path so the
	// terminal draws immediately: connecting to the MCP servers, and asking
	// every provider which of its models are free. Between them they were the
	// whole of the wait before the first frame.
	//
	// Both are safe to land late. The agent re-reads its toolset on every step,
	// and the ladder is only consulted when a turn needs to escalate — which
	// cannot happen before the user has typed anything.
	ready := make(chan struct{})
	go func() {
		defer close(ready)
		mcpServers.Attach(reg)
		ladder := buildLadder(cfg)
		ag.SetFallbacks(ladder)
		if debug && len(ladder) > 0 {
			fmt.Fprintf(os.Stderr, dim("[ladder] %d models\n"), len(ladder))
			for i, c := range ladder {
				fmt.Fprintf(os.Stderr, dim("  %2d. %-52s ctx=%d\n"), i+1, c.Ref, c.Context)
			}
		}
	}()

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
	m.SetMCPLazy(mcpServers.Lazy)
	m.SetMCPFailures(mcpServers.Failures)
	m.SetMCPAuth(mcpLogin(cfg), mcpLogout(cfg))
	m.SetReady(ready)
	m.SetProject(instr.Summary(root))
	_, err = tea.NewProgram(m).Run()
	// Let the background wiring finish before the deferred Close tears the
	// servers down under it, so a slow server cannot be closed mid-handshake.
	<-ready
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
	// connected is closed once every server has answered or failed. Connecting is
	// a network round trip per server, which is the single largest thing standing
	// between launching raunen and being able to type, so it happens off the
	// startup path and anything that needs the tools waits here instead.
	connected chan struct{}
	// silent says a login prompt cannot be announced during background dialling,
	// because the terminal is about to take over the screen. The one-shot and ACP
	// runs leave it false: they draw nothing, so their stderr stays readable and
	// a headless login can still be completed by hand.
	silent bool
	// failures records why each server did not start, keyed by server name.
	//
	// Kept rather than only printed because connecting now finishes after the
	// alternate screen is open, and anything written to stderr from that point
	// lands on a screen that is discarded on exit. So the reason is held here
	// and shown in /mcp, where it can still be read.
	failures map[string]string
}

// Failures reports why each server did not start, for /mcp. Verbatim: an
// endpoint's own words are usually specific enough to act on.
func (s *mcpServers) Failures() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.failures))
	for k, v := range s.failures {
		out[k] = v
	}
	return out
}

// Wait blocks until every server has finished connecting. AddTo calls it, so a
// caller that only wants the tools does not have to.
func (s *mcpServers) Wait() {
	if s.connected != nil {
		<-s.connected
	}
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
	// The tools cannot be named until every server has said what it has.
	s.Wait()
	out := reg.Clone()
	s.attach(out)
	return out
}

// Attach waits for the servers and folds their tools into an existing registry,
// in place. It is what lets the terminal draw before the servers have answered:
// the agent re-reads the toolset on every step, so a tool that arrives while the
// user is still reading the first frame is callable by the time they ask for it.
//
// AddTo, which copies, is still what the one-shot and ACP paths use — they have
// no frame to draw and nothing to gain by starting without their tools.
func (s *mcpServers) Attach(reg *tools.Registry) {
	s.Wait()
	s.attach(reg)
}

// attach names every tool and adds it to reg, either directly or behind the
// catalogue. The caller decides whether reg is a copy.
func (s *mcpServers) attach(out *tools.Registry) {
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
		// A server's resource and prompt tools come along with its tools,
		// gated on the capability each advertises. They are skipped entirely
		// when the server does not offer the feature, so a tool that can only
		// ever answer "method not found" is never shown.
		for _, t := range append(c.Tools(), c.ResourcePromptTools()...) {
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
		return
	}

	// Selecting a tool adds it to this registry, which the agent re-reads on
	// every step — so a tool becomes callable on the request after it is chosen.
	cat := mcp.NewCatalog(out.Add)
	for _, n := range all {
		cat.Add(n.tool)
	}
	out.Add(cat.SearchTool())
	out.Add(cat.SelectTool())
	// A model sometimes calls an MCP tool by name from memory before selecting
	// it. The name is ours, so the honest reply is "load it first" rather than a
	// bare "no such tool" that strands the turn — the same recovery fx gives a
	// call to an advertised-but-unselected dynamic tool.
	out.SetResolver(func(name string) (string, bool) {
		if !cat.Advertised(name) {
			return "", false
		}
		return fmt.Sprintf("%s is not loaded yet. Call mcp_select_tool with "+
			"{\"name\": %q}, then call it on the next step after its schema is "+
			"advertised.", name, name), true
	})
	s.mu.Lock()
	s.catalog = cat
	s.mu.Unlock()
	s.watch(cat)
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

// Lazy reports whether the tools ended up behind the search/select pair. Safe to
// call before the servers have connected, when the answer is simply "not yet".
func (s *mcpServers) Lazy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalog != nil
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
// silent says the caller is about to take over the screen, so a server that
// would stop to ask for a login must not be left dialling in the background.
func startMCP(cfg *config.Config, silent bool) *mcpServers {
	ss := &mcpServers{counts: map[string]int{}, failures: map[string]string{}, silent: silent}
	defs := cfg.ActiveMCP()
	if len(defs) == 0 {
		return ss
	}
	// A server that may open a browser is connected before this returns, not
	// after. Its flow prints the authorization URL to stderr and then waits up
	// to five minutes for a human — and once the alternate screen is open, that
	// instruction is written onto a screen that gets discarded, leaving the user
	// staring at a prompt while a login they were never told about times out.
	//
	// Only the first authorization is interactive: a stored token refreshes
	// without a browser. So this costs a wait once, on the run that was always
	// going to be interrupted anyway.
	eager, deferred := splitInteractive(defs)
	for name, def := range eager {
		c, err := startOneMCP(name, def, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "raunen: mcp %q not started — %s\n", name, err)
			ss.failures[name] = err.Error()
			continue
		}
		ss.clients = append(ss.clients, c)
	}

	if len(deferred) == 0 {
		return ss
	}
	ss.connected = make(chan struct{})
	go ss.dial(deferred)
	return ss
}

// splitInteractive separates the servers that must be connected before the
// terminal draws from those that can finish in the background.
//
// Only "required" makes a server eager, which is the user saying a turn is not
// worth starting without it. raunen does not try to work that out: a workflow
// built around one particular toolset is not something it can see.
//
// An OAuth server used to be inferred as eager, on the grounds that its login
// prints a url and then waits for a human, and a background connect has no
// terminal to print it to. That was a guess standing in for a missing command.
// Now that a login can be run deliberately with "/mcp auth", such a server
// simply fails to connect and says why, which is both truer and quieter than
// making everyone wait in case a browser is needed.
func splitInteractive(defs map[string]config.MCP) (eager, deferred map[string]config.MCP) {
	eager, deferred = map[string]config.MCP{}, map[string]config.MCP{}
	for name, def := range defs {
		if def.Required {
			eager[name] = def
			continue
		}
		deferred[name] = def
	}
	return eager, deferred
}

// dial connects to every server at once and closes connected when they have all
// answered.
//
// Concurrent rather than sequential because the servers are independent: three
// servers that each take a second cost a second between them, not three. A
// slow or unreachable one no longer delays the ones behind it — it only spends
// its own timeout.
func (s *mcpServers) dial(defs map[string]config.MCP) {
	defer close(s.connected)

	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, def := range defs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := startOneMCP(name, def, !s.silent)
			if err != nil {
				// Still on stderr, which is what a one-shot or ACP run reads and
				// where a terminal run that has not yet drawn will show it. The
				// copy in failures is what survives the alternate screen.
				fmt.Fprintf(os.Stderr, "raunen: mcp %q not started — %s\n", name, err)
				s.mu.Lock()
				s.failures[name] = err.Error()
				s.mu.Unlock()
				return
			}
			mu.Lock()
			s.clients = append(s.clients, c)
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Deterministic order regardless of which server answered first, so the
	// tools are named and numbered the same way on every run.
	sort.Slice(s.clients, func(i, j int) bool { return s.clients[i].Name() < s.clients[j].Name() })
}

// startOneMCP performs the handshake with a single server.
// startOneMCP performs the handshake with a single server. visible says whether
// anything the flow prints can still be read — true before the terminal hands
// itself to the alternate screen, false once a background connect is racing it.
func startOneMCP(name string, def config.MCP, visible bool) (*mcp.Client, error) {
	// An OAuth block is carried across rather than shared, so config keeps no
	// knowledge of the protocol and mcp keeps no dependency on config.
	var oa *mcp.OAuth
	if def.OAuth != nil {
		oa = &mcp.OAuth{
			Issuer:   def.OAuth.Issuer,
			ClientID: def.OAuth.ClientID,
			Scopes:   def.OAuth.Scopes,
			Resource: def.OAuth.Resource,
		}
	}
	// 15 seconds is plenty for a server that just answers, and nowhere near
	// enough for one whose first request opens a browser and waits for a
	// human to log in — so an OAuth server gets the whole flow's budget.
	budget := 15 * time.Second
	if oa != nil {
		budget = 5 * time.Minute
	}
	// Where a login cannot be announced, one is not started. Falling back to
	// printing the url only helps when someone is reading stderr; a server
	// connecting behind the alternate screen would print onto a surface that is
	// discarded and then wait minutes for a browser nobody was told to expect.
	// Failing instead puts the reason in /mcp, where it can be read.
	open := mcp.OpenBrowserOrPrint
	if !visible {
		open = func(string) error {
			return fmt.Errorf("raunen was already running; authorize it with a fresh start")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	return mcp.Start(ctx, name, mcp.Server{
		Command:     def.Command,
		Args:        def.Args,
		Env:         def.Env,
		Type:        def.Type,
		URL:         def.URL,
		Headers:     def.Headers,
		OAuth:       oa,
		OpenBrowser: open,
	})
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

// buildLadder resolves the escalation ladder, skipping entries that do not
// resolve so one bad reference cannot break startup.
func buildLadder(cfg *config.Config) []agent.Candidate {
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

// asConfigSkills converts what discovery found into the shape the rest of the
// program already uses. The conversion lives here rather than in either package
// so that config keeps no knowledge of SKILL.md and skills keeps none of the
// config file — the same separation MCP and OAuth have.
func asConfigSkills(found *skills.Set) map[string]config.Skill {
	out := make(map[string]config.Skill, found.Len())
	for _, name := range found.Names() {
		sk, ok := found.Get(name)
		if !ok {
			continue
		}
		out[sk.Name] = config.Skill{
			Description: sk.Description,
			Prompt:      sk.Prompt,
			Source:      sk.Path,
		}
	}
	return out
}

// mcpLogin runs the OAuth flow for one configured server, deliberately, from a
// terminal that is waiting for it.
//
// It returns the authorization URL alongside any error so the caller can show it
// rather than print it: this exists precisely because a background connect
// cannot announce a login, and repeating that mistake here would defeat it.
func mcpLogin(cfg *config.Config) func(context.Context, string) (string, error) {
	return func(ctx context.Context, name string) (string, error) {
		def, ok := cfg.MCP[name]
		if !ok {
			return "", fmt.Errorf("no MCP server called %s", name)
		}
		if def.OAuth == nil {
			return "", fmt.Errorf("%s does not use oauth", name)
		}
		if def.URL == "" {
			return "", fmt.Errorf("%s has no url to authorize against", name)
		}

		// Captured so a browser that will not open can still be answered by
		// hand: the url goes back to the UI, which has somewhere to show it.
		var authURL string
		var mu sync.Mutex
		open := func(u string) error {
			mu.Lock()
			authURL = u
			mu.Unlock()
			return mcp.OpenBrowser(u)
		}

		_, err := mcp.Authorize(ctx, def.URL, "", mcp.OAuth{
			Issuer:   def.OAuth.Issuer,
			ClientID: def.OAuth.ClientID,
			Scopes:   def.OAuth.Scopes,
			Resource: def.OAuth.Resource,
		}, mcp.DefaultTokenStore(), open)

		mu.Lock()
		u := authURL
		mu.Unlock()
		if err != nil {
			return u, err
		}
		return "", nil
	}
}

// mcpLogout drops a server's stored token, so the next run authorizes afresh.
func mcpLogout(cfg *config.Config) func(string) error {
	return func(name string) error {
		def, ok := cfg.MCP[name]
		if !ok {
			return fmt.Errorf("no MCP server called %s", name)
		}
		if def.OAuth == nil {
			return fmt.Errorf("%s does not use oauth", name)
		}
		store, ok := mcp.DefaultTokenStore().(mcp.TokenForgetter)
		if !ok {
			return fmt.Errorf("this token store cannot forget a token")
		}
		resource := def.OAuth.Resource
		if resource == "" {
			resource = def.URL
		}
		return store.Forget(def.OAuth.Issuer, resource)
	}
}

// imagePaths collects a repeatable --image flag. flag.Value rather than a
// comma-separated string, because a screenshot's path frequently contains a
// comma and splitting on one would break it in a way that is hard to see.
type imagePaths []string

func (p *imagePaths) String() string { return strings.Join(*p, ", ") }

func (p *imagePaths) Set(v string) error {
	*p = append(*p, v)
	return nil
}

// load reads every attachment, naming the file that failed. One bad path fails
// the run: a scripted turn that quietly asked about fewer images than it was
// given would produce an answer that looks right and is not.
func (p imagePaths) load() ([]provider.Image, error) {
	var out []provider.Image
	for _, path := range p {
		img, err := attach.Load(path)
		if err != nil {
			return nil, fmt.Errorf("--image %s: %w", path, err)
		}
		out = append(out, img)
	}
	return out, nil
}
