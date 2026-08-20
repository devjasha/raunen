# raunen

A terminal agent for local LLMs. One Go binary, no runtime, no server. Go 1.25
or newer; the only dependencies are Bubble Tea and Lip Gloss.

## Working here

- `go build ./... && go test ./...` before proposing a change. Both are fast.
- `gofmt` is not optional; `go vet ./...` should stay silent.
- No new third-party dependencies without saying why. The binary being small and
  self-contained is a feature, not an accident.

## Layout

- `main.go` — flags, config, wiring, and the one-shot path
- `internal/agent` — the tool-use loop. Presentation-free: it emits typed
  `Event`s and knows nothing about the terminal. Keep it that way.
- `internal/ui` — Bubble Tea model. Everything rendered goes through here.
- `internal/acp` — Agent Client Protocol over stdio. A transport in front of
  `agent.Agent`: it must never grow behaviour the terminal does not have.
- `internal/provider` — OpenAI-compatible streaming client
- `internal/tools` — the built-in tools, rooted at the working directory. Every
  result is bounded at the registry, not in the tool: what does not fit is kept
  in the result store and reachable through the `result` handle, so an MCP tool
  cannot bypass the cap by being added later
- `internal/fileset` — what git considers part of the project; shared by the
  search tools and by `@` completion, so the two cannot disagree
- `internal/permission` — standing allow/deny rules. Gating lives in one place,
  `Agent.dispatch`; a tool must never decide its own permissions
- `internal/skills` — SKILL.md discovery, including other agents' directories
- `internal/mcp` — MCP client: stdio, HTTP and SSE transports, OAuth 2.1
- `internal/config` — config, skills and MCP definitions on disk
- `web/` — the marketing and docs site, deployed separately. Not the agent.

## Conventions

**Comments say why, not what.** The code already says what it does. A comment
earns its place by explaining a decision, a trade-off, or the bug that made the
obvious approach wrong.

**No background colours anywhere in `internal/ui`.** The terminal's own
background shows through untouched, and colours are ANSI indices 0–15 so they
follow the user's palette rather than fighting it. A `Background()` call breaks
the transparency the whole UI is built around.

**Errors from an endpoint are shown verbatim.** They are usually specific enough
to act on, and a paraphrase throws that away.

**Prefer failing softly at the edges.** A broken MCP server, an unreadable
skills file or a missing `AGENTS.md` is survivable; a missing model is not.
Report it on stderr and carry on.
