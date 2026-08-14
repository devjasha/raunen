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

macOS and Linux, Intel and ARM. Downloads the binary for your platform into
`~/.local/bin`, verifies it against the release checksums, and tells you if that
directory is not on your `PATH`.

To change where it goes or pin a version, put the variables on the `sh` side of
the pipe — in `VAR=x curl ... | sh` they would apply to `curl`, not to the shell
running the script:

```sh
curl -fsSL https://raw.githubusercontent.com/devjasha/raunen/main/install.sh \
  | RAUNEN_INSTALL_DIR=/usr/local/bin RAUNEN_VERSION=v0.1.0 sh
```

Piping a script into a shell is worth reading first:

```sh
curl -fsSL https://raw.githubusercontent.com/devjasha/raunen/main/install.sh | less
```

Or from source, which needs Go 1.25 or newer:

```sh
go build -o ~/.local/bin/raunen .
```

Nothing is needed at runtime either way — one static binary, about 3 MB
compressed.

## Getting started

**1. Have a model to talk to.** Anything speaking the OpenAI API will do. With
[Ollama](https://ollama.com):

```sh
ollama pull qwen3.5          # or any model you like
```

**2. Run it in the directory you want to work in.** Tools are rooted there, so
`raunen` in a project is what gives it something to read.

```sh
cd ~/Projects/my-thing
raunen
```

The first run writes `~/.config/raunen/config.json` and, since no model is
configured yet, asks your endpoints what they have and picks one:

```
raunen: no default model set, using ollama/qwen3.5:latest
```

**3. Ask it something.** It reads and writes files and runs commands in that
directory, showing each step as it goes:

```
  ──────────────────────────────────────────────────────── 10:42
  ▌ what does main.go do?

    ⏺ read  main.go
      ↳ 84 lines

  It parses flags, loads the config, and starts either the TUI or a
  single one-shot turn.
```

**4. Decide how much rope it gets.** `tab` cycles the mode, shown bottom-left.
Start in `plan` if you would rather it proposed than acted:

```
plan · ⎇ main · qwen3.5:latest · █░░░░░░░░░ 11% · 925
```

**5. Watch the context bar.** Local models run out of room faster than you would
expect, and that is the most common cause of a bad answer. When it turns amber,
`/clear` starts fresh. See [working with local models](#working-with-local-models)
for how to give it more room — it matters more than anything else here.

`/help` lists everything; `ctrl+c` leaves. The conversation is saved, so
`raunen --continue` picks it back up tomorrow.

### Where the dashboard is

There is no separate dashboard or web UI: raunen is the interface, and a running
agent has nowhere else to put things. The equivalents are in the TUI itself —
`/model` lists every model your endpoints serve, `/providers` the endpoints
themselves, `/sessions` your history, and the bar under the input carries mode,
branch, model and context at all times. From outside, `raunen --running` shows
live instances and `RAUNEN_DEBUG=1` prints per-request token accounting and the
resolved model ladder on stderr.

## Use

```sh
raunen                              # start the TUI in the current directory
raunen -m ollama/qwen3:8b           # pick a model for this run
raunen 'what does main.go do?'      # one-shot; stdout stays clean for pipes
raunen --continue                   # resume this directory's last session
raunen --sessions                   # list saved sessions
raunen --running                    # list running instances
raunen -config                      # print the config path
raunen -version                     # print the version
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
| `task` (tool) | the model delegates to a sub-agent |
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
  "default": "",
  "providers": {
    "ollama":       { "base_url": "http://localhost:11434/v1", "context": 4096 },
    "ollama-cloud": { "base_url": "https://ollama.com/v1", "api_key_env": "OLLAMA_API_KEY" },
    "lmstudio":     { "base_url": "http://localhost:1234/v1" },
    "llamacpp":     { "base_url": "http://localhost:8080/v1" },
    "openrouter":   { "base_url": "https://openrouter.ai/api/v1", "api_key_env": "OPENROUTER_API_KEY" }
  },
  "models": {}
}
```

Adding a provider is a base URL — no adapter code, because they all speak the
same wire format. Models are `provider/model`, split on the first `/` so names
containing slashes (`hf.co/user/repo`, `openai/gpt-oss-120b`) still work.

`default` may be left empty, in which case the first run asks the configured
endpoints what they serve and picks one, preferring anything local. Nothing here
assumes a particular model is installed.

Providers added to the defaults in later versions are merged into an existing
config on load, so new endpoints appear without a rewrite. Anything you have
edited is left alone.

### Context windows

```json
"models": {
  "ollama/qwen3-coder:30b":         { "context": 32768 },
  "ollama-cloud/qwen3-coder:480b":  { "context": 262144 }
}
```

A window is a property of the model, not the endpoint: one Ollama serves many
models with very different limits. Declare them here; `context` on a provider is
only a default for models not listed. There is no way to ask an
OpenAI-compatible endpoint how big a window is, so it has to be written down.

Without it you still get a working agent and a raw token count — just no usage
percentage, no history trimming, and no automatic switching, all of which need
something to measure against.

### Cloud models

`ollama-cloud` is configured out of the box. Create a key at
[ollama.com/settings/keys](https://ollama.com/settings/keys) and export it:

```sh
export OLLAMA_API_KEY=...
```

Cloud catalogues list without a key, so `/model` shows them either way — models
whose provider has no key are marked `needs OLLAMA_API_KEY` rather than failing
later with a 401. The same applies to any keyed provider.

Local and cloud models mix freely, which is what makes the fallback ladder
useful: start on something small and local, escalate to a large hosted model
only when a conversation actually needs the room.

Anthropic and Gemini are the notable formats *not* covered; they would need a
real adapter behind the same `provider.Client` interface.

## Switching models automatically

Off by default. Turn it on with a ladder of models, largest last:

```json
{
  "auto_switch": true,
  "fallback": [
    "ollama/qwen3-coder:30b",
    "ollama-cloud/qwen3-coder:480b"
  ],
  "models": {
    "ollama/qwen3-coder:30b":        { "context": 32768 },
    "ollama-cloud/qwen3-coder:480b": { "context": 262144 }
  }
}
```

The ladder is yours to define and can mix local and hosted models freely.

### Free models

```json
{ "auto_switch": true, "free_fallback": true }
```

This appends every model the providers report as free to the ladder, roomiest
first. On OpenRouter today that is 18 models, several with a 1M-token context —
so a local model that runs out of room can hand off to something far larger
without a bill.

Free is read from the endpoint's own pricing rather than from a maintained
list, so it stays current: a model counts as free when both its prompt and
completion prices are zero. Prices that cannot be reduced to a single number —
some models quote tiered pricing as an array — are never assumed to be free.

The same discovery supplies **context windows**, which is why free rungs get
real limits without being declared. Any provider that reports `context_length`
gives raunen the window for free; an explicitly configured `context` still wins.

Free tiers refuse with `429` rather than a bill, and that is treated as a
routing decision rather than a failure: raunen moves to the next rung and
retries. Rate-limited moves are the one case where a *smaller* window is
acceptable — any model that answers beats one that will not.

**"Free" means no per-token cost, not no account.** OpenRouter's free models
still need `OPENROUTER_API_KEY`; without it they return `401`. Rungs whose key
is missing are dropped from the ladder rather than kept to fail one by one, so
the feature is inert until a key is set instead of turning one failure into
eighteen.

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

## Sub-agents

The model can delegate a self-contained piece of investigation with the `task`
tool. The sub-agent gets its own empty context, does the work, and returns only
its final answer:

```
  ◆ task  find where the read tool is defined
      ⏺ bash  grep -rn "read" internal/tools
        ↳ 12 lines
      ⏺ read  internal/tools/tools.go
        ↳ 300 lines
    ↳ returned 180 chars after 3 steps

  The read tool is defined in internal/tools/tools.go and returns the file
  with line numbers.
```

Sub-agents work in their own panel above the input, so their steps never flood
the transcript:

```
  ╭──────────────────────────────────────────────────────────╮
  │ ◆ ⠇ working on  Summarize vcs.go and subview.go          │
  │ ⏺ read  internal/vcs/vcs.go                              │
  │   ↳ 38 lines                                             │
  │ ⏺ read  internal/ui/subview.go                           │
  │   ↳ 70 lines                                             │
  ╰──────────────────────────────────────────────────────────╯
  ⠇ working  ⏎ 1 queued  ·  esc to cancel
  ╭──────────────────────────────────────────────────────────╮
  │ ›                                                        │
  ╰──────────────────────────────────────────────────────────╯
  auto · ⎇ main · qwen3.5-8k:latest · ██░░░░░░░░ 22% · 1.8k
```

The panel opens when a sub-agent starts, follows it live, and collapses when it
finishes, leaving one line in the transcript. What a sub-agent did is
working-out, not conversation.

**You can keep typing while it runs.** Press enter and the message is held and
sent the moment the turn ends — the status row shows `⏎ 1 queued`. It cannot be
delivered sooner: the model is mid-turn, blocked on a tool result it asked for,
and cannot accept new input until that returns. Queueing is the honest version
of carrying on while it works.

**This is a context technique, not a concurrency one.** Nothing runs in
parallel: a local model serves one request at a time — two concurrent requests
to the same Ollama measured *slower* than two sequential ones — so parallelism
would buy nothing on a local setup.

What it buys is room. Measured on the run above:

| | prompt tokens |
|---|---|
| parent before delegating | 916 |
| sub-agent, internally | 4,115 |
| parent after it returned | 1,356 |

The sub-agent spent 4,115 tokens reading files; the parent grew by 440. On an
8k window that is the difference between answering the question and running out
of context halfway through it.

The costs are real too: the work still happens, so it takes just as long, and
the extra tool schema is charged on every request. Turn it off with
`"subagents": false` if you are working with a very small window.

A sub-agent **inherits the caller's mode**, so plan mode still refuses writes
and accept mode still prompts — approvals surface in the same place, and
delegation cannot launder a write past a refusal. It **cannot delegate further**:
the child is built without the `task` tool, so recursion is impossible by
construction rather than by a depth counter.

## Tool output cleaning

Command output is written for a terminal, not for a model. Before a result is
charged to the context it is stripped of colour codes, progress bars redrawn
over themselves, runs of blank lines, and repeated identical lines (replaced
with one line and a count). This happens *before* truncation, so a given budget
holds more of what matters and the useful end of a long log is likelier to
survive.

Everything here preserves meaning. Nothing rewrites words, reorders lines or
summarises — a tool result is evidence, and a model reasoning about paraphrased
evidence is worse off than one reasoning about less of it.

**Measured savings, on output from this repository:**

| payload | raw | cleaned | saved |
|---|---|---|---|
| `go build -x` | 108,748 | 108,747 | 0.0% |
| `git log --oneline -30` | 171 | 170 | 0.6% |
| `ls -laR internal` | 2,697 | 2,696 | 0.0% |
| a Go source file | 34,332 | 34,331 | 0.0% |
| ANSI + progress + duplicates | 56 | 21 | 62.5% |

Close to nothing on ordinary output, and a lot on noisy output. The reason is
worth knowing: commands here run through `exec` without a terminal attached, so
most tools disable colour and progress bars on their own. There is usually
nothing left to strip. The cleaning earns its place on the cases that do produce
noise — anything forcing `--color=always`, progress that ignores the lack of a
TTY, or logs that repeat themselves — and costs a single pass otherwise.

If you have seen claims of 60–90% savings from this kind of filtering, that is
the noisy column. It is real, and it does not describe typical output.

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
