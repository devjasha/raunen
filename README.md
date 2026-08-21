# raunen

A small terminal agent for local LLMs. One Go binary, no runtime, no server.

```
                \||/
                |  @___oo
      /\  /\   / (__,,,,|
     ) /^\) ^\/ _)
     )   /^\/   _)
     )   _ /  / _)
 /\  )/\/ ||  | )_)
<  >      |(,,) )__)
 ||      /    \)___)\
 | \____(      )___) )___
  \______(_______;;; __;;;
```

Point it at Ollama, LM Studio, llama.cpp, vLLM, OpenRouter — anything that
speaks the OpenAI `/v1/chat/completions` format. It reads and writes files, runs
commands, and keeps the conversation where you can see it.

```
  ──────────────────────────────────────────────────────── 10:42
  ▌ what does main.go do?

    ⏺ read  main.go
      ↳ 84 lines

  It parses flags, loads the config, and starts either the TUI or a
  single one-shot turn.

  • --continue resumes the last session for the directory
  • a prompt as an argument skips the UI entirely

  ╭────────────────────────────────────────────────────────────╮
  │ ›                                                          │
  ╰────────────────────────────────────────────────────────────╯
  auto · ⎇ main · qwen3.5-8k:latest · ██░░░░░░░░ 22% · 1.8k
```

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/devjasha/raunen/main/install.sh | sh
```

macOS and Linux, Intel and ARM: the binary for your platform goes into
`~/.local/bin`, verified against the release checksums.

```sh
go build -o ~/.local/bin/raunen .
```

From source instead, which needs Go 1.25 or newer. Either way nothing is needed
at runtime.

See [the install docs](https://raunen.vercel.app/docs/install) for pinning a
version or choosing a directory.

## Getting started

Have a model running — `ollama pull qwen3.5`, or any endpoint speaking the
OpenAI API — then run `raunen` in the directory you want to work in. Tools are
rooted there. The first run writes `~/.config/raunen/config.json` and, with no
model configured, asks your endpoints what they serve and picks one.

`/help` lists the commands, `tab` cycles the mode, and `raunen --continue` picks
up where you left off. The rest is on
[the docs site](https://raunen.vercel.app/docs).

## What it does

- Reads, writes and edits files, and runs shell commands, rooted at the working
  directory.
- Streams from any OpenAI-compatible endpoint — local or hosted — with the
  endpoint's own errors shown verbatim.
- Standing permission rules in the config: `allow`, `deny` or `ask`, per tool
  and per pattern, with a deny that holds in every mode.
- Three modes — `auto`, `accept edits` and `plan` — cycled with `tab`.
- Skills: named instructions in a `SKILL.md`, pulled into a prompt with `#`,
  read from this project and from other agents' skill directories.
- MCP servers over stdio, HTTP and SSE, including ones that authenticate with
  OAuth.
- Sub-agents: the model delegates a self-contained investigation with the `task`
  tool, several at once, watchable with `ctrl+o`.
- Sessions saved after every turn, with `--continue`, `--resume` and
  `/sessions`.
- Images from a path, a drag onto the window or the clipboard, and `--image` for
  headless runs.
- Voice input: dictation tools insert their transcription like any other paste,
  multi-line and intact.
- Git-aware `grep` and `glob`, and `@` completion over the same file list, so
  what you can mention and what it can search agree.
- `/model` picks from what your endpoints actually serve, `/favourite` pins the
  ones you use, and an optional ladder switches models when one refuses.
- `raunen acp` speaks the Agent Client Protocol on stdio, so an editor such as
  Zed can drive the same agent.
- One-shot scripting: a prompt as an argument prints the answer on stdout, or
  `--json` prints one document a program can parse.
- `raunen --running` lists live instances, which a tmux popup can turn into a
  session picker.
- A context bar showing how full the window is, with `/compact` and `/clear`
  when it fills.
- Project conventions read from `AGENTS.md` at the top of the repository.

## Documentation

Everything else lives at [raunen.vercel.app/docs](https://raunen.vercel.app/docs):

- [Install](https://raunen.vercel.app/docs/install)
- [Commands and keys](https://raunen.vercel.app/docs/commands)
- [Configuration](https://raunen.vercel.app/docs/configuration)
- [Modes](https://raunen.vercel.app/docs/modes)
- [Models and ladders](https://raunen.vercel.app/docs/models)
- [Skills](https://raunen.vercel.app/docs/skills)
- [Sub-agents](https://raunen.vercel.app/docs/subagents)
- [MCP servers](https://raunen.vercel.app/docs/mcp)
- [Scripting](https://raunen.vercel.app/docs/scripting)
- [Local models](https://raunen.vercel.app/docs/local-models)
- [Using tmux](https://raunen.vercel.app/docs/tmux)

## Licence

MIT
