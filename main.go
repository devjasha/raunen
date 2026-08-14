// Command raunen is a small terminal agent for local and remote LLMs.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"raunen/internal/agent"
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
	)
	flag.Parse()

	if *printPath {
		fmt.Println(config.Path())
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	ref := *modelRef
	if ref == "" {
		ref = cfg.Default
	}
	p, model, err := cfg.Resolve(ref)
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

	// Tool results are capped relative to the model's context, so a single read
	// cannot evict the conversation around it.
	reg := tools.Default(root, tools.OutputBudget(p.Context))
	ag := agent.New(provider.New(p.BaseURL, p.Key(), model), reg, cfg.System)
	ag.SetContext(p.Context)
	ag.SetRef(ref)
	ag.SetAutoSwitch(cfg.AutoSwitch)
	ag.SetFallbacks(fallbacks(cfg))

	sess, err := openSession(*resume, *continued, root, ref)
	if err != nil {
		return err
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
	_, err = tea.NewProgram(ui.New(cfg, ag, root, ref, sess)).Run()
	return err
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
		out = append(out, agent.Candidate{
			Ref:     ref,
			Client:  provider.New(p.BaseURL, p.Key(), model),
			Context: p.Context,
		})
	}
	return out
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
