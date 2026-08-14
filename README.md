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

  auto · ⎇ main · qwen3.5-8k:latest        ██░░░░░░░░ 22% · 1.8k
  ╭────────────────────────────────────────────────────────────╮
  │ ›                                                          │
  ╰────────────────────────────────────────────────────────────╯
```

## Install

```sh
go build -o ~/.local/bin/raunen .
```

Go 1.25 or newer. The binary is about 11 MB and depends on nothing at runtime.

## Use

```sh
raunen                              # start the TUI in the current directory
raunen -m ollama/qwen3:8b           # pick a model for this run
raunen 'what does main.go do?'      # one-shot; stdout stays clean for pipes
raunen --continue                   # resume this directory's last session
raunen --sessions                   # list saved sessions
raunen --running                    # list running instances
raunen -config                      # print the config path
```

| Key | |
|---|---|
| `enter` | send |
| `shift+enter` / `alt+enter` | newline without sending |
| `tab` | cycle auto / accept edits / plan |
| `esc` | cancel the running turn |
| `ctrl+c` | cancel if working, otherwise quit |
| `pgup` / `pgdn` | scroll the transcript |
| `shift+↑` / `shift+↓` | scroll by a line |
| `y` / `n` | answer an approval prompt |

| Command | |
|---|---|
| `/model` | choose a model from a list |
| `/model <provider/model>` | switch directly |
| `/providers` | list configured endpoints |
| `/sessions` | list saved sessions |
| `/resume <id>` | pick up a saved session |
| `/clear` | start a new session, keeping the old one |
| `/help` | show this |
| `/quit` | exit |

## Modes

`tab` cycles three modes, shown at the left of the status bar.

| Mode | Behaviour |
|---|---|
| `auto` | every tool runs immediately |
| `accept edits` | read-only tools run freely; anything that changes state asks first |
| `plan` | anything that changes state is refused, so the model investigates and proposes |

In `accept edits` a prompt takes over the status row — `? run write main.go   y
approve  n decline` — and the agent is blocked until answered. The mode is also
written into the system prompt, so the model knows the rules instead of learning
them by collecting refusals.

**How "changes state" is decided.** `read` and `list` never do; `write` and
`edit` always do. `bash` is the awkward one: it can do anything, so it counts as
mutating unless the command matches a conservative allowlist (`ls`, `cat`,
`grep`, `rg`, `find`, `git status|log|diff|show`, and similar). Redirection,
command substitution, env-var prefixes, absolute paths and anything unrecognised
all count as mutating.

That is an allowlist rather than a denylist of dangerous commands, deliberately:
there is no way to enumerate every way a shell can change something, and a wrong
"this is safe" writes to your disk. The cost is that plan mode sometimes refuses
a harmless command it does not recognise.

## Sessions

Conversations are saved to `~/.local/share/raunen/sessions/` after every turn,
one JSON file each.

```sh
raunen --continue        # the most recent session for this directory
raunen --resume <id>     # a specific one
```

Resuming restores both halves: the transcript is rebuilt so you can see what was
said, and the message history goes back to the model, so it remembers. `/clear`
starts a fresh session and leaves the old one on disk.

## Finding running sessions from tmux

Add to `tmux.conf`:

```tmux
bind R display-popup -E -w 70% -h 50% "raunen-picker"
```

`prefix + R` lists every raunen currently running and jumps to its pane.
[`raunen-picker`](contrib/raunen-picker) is a small script that reads
`raunen --running` and pipes it through [fzf](https://github.com/junegunn/fzf).

Instances announce themselves in `~/.local/share/raunen/running/` for this to
work. tmux cannot be asked directly: `pane_current_command` reports a pane's
*foreground* process, which during a turn is whatever the agent happens to be
running, so it shows `ollama` or `git` rather than `raunen`.

## Configuration

`~/.config/raunen/config.json`, written on first run:

```json
{
  "default": "ollama/qwen3.5:latest",
  "providers": {
    "ollama":   { "base_url": "http://localhost:11434/v1", "context": 4096 },
    "lmstudio": { "base_url": "http://localhost:1234/v1" },
    "llamacpp": { "base_url": "http://localhost:8080/v1" },
    "openrouter": {
      "base_url": "https://openrouter.ai/api/v1",
      "api_key_env": "OPENROUTER_API_KEY"
    }
  }
}
```

Adding a provider is a base URL — no adapter code, because they all speak the
same wire format. Models are `provider/model`, split on the first `/` so Ollama
names containing slashes (`hf.co/user/repo`) still work.

`context` is optional and declares the model's window in tokens. It drives the
usage bar, the history trimming below, and automatic switching; without it you
get a plain token count. There is no way to ask an OpenAI-compatible endpoint how big its context
is, so it has to be declared.

`/model` with no argument lists what the endpoints actually serve, fetched over
`GET /v1/models`. Adding a model to Ollama makes it appear without touching the
config.

Anthropic and Gemini are the notable formats *not* covered; they would need a
real adapter behind the same `provider.Client` interface.

## Switching models automatically

Off by default. Turn it on with a ladder of models, largest last:

```json
{
  "auto_switch": true,
  "fallback": [
    "ollama/qwen3.5-16k:latest",
    "openrouter/anthropic/claude-sonnet-4"
  ]
}
```

When the conversation outgrows the current model, raunen moves up the ladder and
carries on instead of failing:

```
  ⇅ switched to ollama/qwen3.5-16k:latest
    — the question and its results need 1612 tokens of a 2048-token window
```

It escalates on two triggers. **Before a request**, when what trimming is not
allowed to drop — the system prompt, the tool schemas, the question being
answered and its results — already fills 70% of the window. Trimming cannot
help there, so the model would be cut off mid-answer. **After a request** that
came back with `finish_reason: "length"` and no content, in which case the same
request is retried on the roomier model. Nothing had been emitted, so the retry
cannot leave a seam in the reply.

A rung whose declared context is no larger than the current one is skipped —
moving sideways hits the same ceiling. A rung with no declared context is used
anyway: you put it in the ladder on purpose, and hosted models usually have
room. Ordering is treated as intent rather than second-guessed.

Each turn starts at the bottom of the ladder again, so a short question does not
inherit the expensive model just because an earlier one needed it. Escalation
never goes back down within a conversation.

## Working with local models

Everything here came out of running this against Ollama. Most of it will bite
with any small model.

**Give the model room.** Ollama serves a 4096-token context by default whatever
the model supports — qwen3.5 advertises 262144 and gets 4096. This is the single
biggest cause of bad results and it does not look like a context problem from
the outside: you get an empty reply, or the model answering a question you never
asked, because the server truncates the conversation from the front and your
question is among the first things to go.

It fills faster than you would think. The system prompt and five tool schemas
are ~740 tokens before anything happens; asking for an overview of a real
project reached the ceiling after six tool calls, with seven tokens left to
answer in:

```
prompt=742 → 786 → 821 → 1969 → 2966 → 3613 → 4089   completion=7
```

Fix it, then set `context` to match:

```sh
# server-wide, needs a restart
OLLAMA_CONTEXT_LENGTH=16384 ollama serve

# or a model variant, no restart
printf 'FROM qwen3.5:latest\nPARAMETER num_ctx 16384\n' > Modelfile
ollama create qwen3.5-16k -f Modelfile
```

Size it against your RAM, not just the model: 16384 on a 16 GB machine running a
9.7B model ran out of memory and dropped connections mid-response. Check what
you are actually getting with `ollama ps` — the `CONTEXT` column is the truth.
`RAUNEN_DEBUG=1` prints per-request token accounting on stderr.

**Two mechanisms keep requests inside the window.** Tool output is capped
relative to the context, about a quarter of it per result — a fixed 30 KB cap is
~8000 tokens, larger than the whole context of a model served at 4096, so one
`read` would evict everything around it. And history is trimmed before each
request: the system prompt and the question being answered are never dropped,
everything else goes oldest first, in whole tool-call groups so the request
stays valid.

**Reasoning models** (qwen3, deepseek-r1, gpt-oss) stream thinking in a separate
`reasoning` field while `content` stays empty, sometimes for a minute. It is
shown live under a `thinking` label and dropped when the answer starts — never
stored, never sent back.

**They leak tool-call scaffolding into text** — stray `</tool_call>`,
`</parameter>`, `</function>` alongside a perfectly good structured tool call.
Filtered out of the content stream, matching only that specific list of tag
names so a model writing HTML is unaffected.

**An assistant message with tool calls and no text must still serialize
`"content": ""`.** Omitting the key makes Ollama reject the *next* request with
`invalid message content type: nil`, which breaks every conversation after the
first tool call.

**They call tools with empty arguments** — `bash {}`, `read {}`. Every tool
rejects missing arguments with a message the model can act on; returning
something bland like `[no output]` invites it to retry the same empty call in a
loop.

**Empty replies have two distinct causes**, and both used to look identical.
`finish_reason: "length"` means the model was cut off mid-generation — with a
reasoning model that happens during thinking, so nothing is produced at all. A
dropped connection is the other: `bufio.Scanner` ends cleanly at EOF, so a
stream that stops early is indistinguishable from one that finished unless you
check for `[DONE]` or a `finish_reason`. They are now reported separately.

## Terminal behaviour

**The input is welded to the bottom row** and never moves — not while a reply
streams, not when a long block of output arrives. That requires owning the whole
screen, so the program takes the alternate screen and renders the transcript
itself.

The cost: tmux's scrollback and copy-mode no longer hold the conversation. In
exchange the transcript scrolls in-app and sessions are saved to disk. Quitting
leaves the terminal exactly as it was found — nothing is replayed into it.

**No background is ever painted.** `View.BackgroundColor` stays nil and nothing
calls `Background()`, so your terminal's background, including transparency,
shows through. Colours are ANSI indices 0–15, so they follow your palette
instead of fighting it.

**The transcript is built from logical entries**, not flat strings, each with
separate first-row and continuation prefixes. That is what gives bullets and
tool calls a hanging indent when they wrap, and what lets the indent survive a
resize — wrapping is redone at the new width rather than baked in.

## Layout

```
main.go                    CLI entry, one-shot mode
internal/agent             the tool-use loop, modes, history trimming
internal/config            providers and models
internal/provider          OpenAI-compatible streaming client
internal/session           saving, resuming, running instances
internal/tools             bash, read, write, edit, list
internal/ui                Bubble Tea TUI
internal/vcs               git branch for the status bar
contrib/raunen-picker      tmux session picker
```

## Tests

```sh
go test ./...
```

The parts worth testing are the ones that run on every line of model output and
have edge cases that are easy to get wrong: the markup stripper (a tag split
across streaming deltas), the markdown renderer (a code fence whose contents
must survive untouched), history trimming (tool-call groups that must stay
intact), the read-only command allowlist (from both directions), and stream
termination (a truncated response must not look like an empty one).

## Licence

MIT
