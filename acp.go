package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"raunen/internal/acp"
	"raunen/internal/agent"
	"raunen/internal/config"
	"raunen/internal/instructions"
	"raunen/internal/permission"
	"raunen/internal/provider"
	"raunen/internal/session"
	"raunen/internal/skills"
	"raunen/internal/tools"
)

// serveACP runs the Agent Client Protocol over stdin and stdout, so an editor
// can drive raunen as its coding agent.
//
// The agent it serves is the one the terminal uses: same tools, same permission
// rules, same skills, same AGENTS.md, same escalation ladder. Only the front
// end differs, which is the whole reason the loop was kept presentation-free.
//
// Stdout belongs to the protocol. Anything raunen would normally print —
// a skill that failed to parse, an MCP server that would not start — goes to
// stderr, because one stray line on stdout is an unparseable frame and a dead
// connection.
func serveACP(cfg *config.Config, modelRef string) error {
	// One set of MCP servers for the process rather than per session: they are
	// subprocesses, and starting another copy for every session an editor opens
	// would multiply them without bound.
	mcpServers := startMCP(cfg, false)
	defer mcpServers.Close()

	build := func(cwd string) (*agent.Agent, *session.Session, acp.Expander, error) {
		ag, sess, dirCfg, err := buildAgent(cfg, cwd, modelRef, mcpServers)
		if err != nil {
			return nil, nil, nil, err
		}
		// Skills are expanded on the way out, exactly as they are for a
		// one-shot run: an editor can type #review as readily as a terminal can.
		return ag, sess, dirCfg.ExpandSkills, nil
	}

	return acp.Serve(context.Background(), os.Stdin, os.Stdout, version, build)
}

// buildAgent assembles an agent for a working directory.
//
// This is the same sequence run() performs for the terminal, with the pieces
// that depend on the directory — project instructions, the tool root, skills —
// resolved against the directory the client asked for rather than against the
// process's own. An editor can hold several projects open, and a tool rooted in
// the wrong one would read the wrong files.
func buildAgent(cfg *config.Config, cwd, modelRef string, mcpServers *mcpServers) (*agent.Agent, *session.Session, *config.Config, error) {
	root, err := filepath.Abs(cwd)
	if err != nil {
		return nil, nil, nil, err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, nil, nil, fmt.Errorf("not a directory: %s", cwd)
	}

	// This project's skills, so a session opened on one project does not
	// inherit another's.
	cfg = acpSkills(cfg, root)

	sess := session.New(root, "")

	// Order of preference matches the terminal: an explicit --model for the
	// process, then the configured default, then whatever the endpoints serve.
	ref := modelRef
	if ref == "" {
		ref = cfg.Default
	}
	if ref == "" {
		if ref, err = discoverModel(cfg); err != nil {
			return nil, nil, nil, err
		}
	}
	p, model, err := cfg.Resolve(ref)
	if err != nil {
		return nil, nil, nil, err
	}
	sess.Model = ref

	window := windowFor(cfg, p, ref, model)
	reg := tools.Default(root, tools.OutputBudget(window))
	reg = mcpServers.AddTo(reg)

	ag := agent.New(provider.New(p.BaseURL, p.Key(), model), reg, cfg.System)

	// Per-directory, so each project speaks for itself.
	instr := instructions.Load(root, config.InstructionsPath())
	ag.SetProject(instr.Prompt(root))

	perms, problems := permission.Parse(cfg.Permissions)
	for _, msg := range problems {
		fmt.Fprintln(os.Stderr, "raunen:", msg)
	}
	ag.SetPermissions(perms)

	ag.SetContext(window)
	ag.SetRef(ref)
	ag.SetAutoSwitch(cfg.AutoSwitch)
	ag.SetMaxSteps(cfg.MaxSteps)
	ag.SetFallbacks(buildLadder(cfg))
	if cfg.SubagentsEnabled() {
		ag.EnableSubagents()
	}
	return ag, sess, cfg, nil
}

// acpSkills folds this directory's SKILL.md files into a copy of the config, so
// a session opened on one project does not inherit another's skills.
//
// A copy rather than a mutation: the config is shared by every session on the
// connection, and skills are the one part of it that is per-directory.
func acpSkills(cfg *config.Config, root string) *config.Config {
	clone := *cfg
	clone.Skills = map[string]config.Skill{}
	for name, s := range cfg.Skills {
		clone.Skills[name] = s
	}
	found := skills.Load(root, skills.UserDir(filepath.Dir(config.Path())))
	for _, problem := range found.Problems {
		fmt.Fprintln(os.Stderr, "raunen: skill skipped —", problem)
	}
	clone.AddSkills(asConfigSkills(found))
	return &clone
}
