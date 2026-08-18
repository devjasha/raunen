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

### Checking on things

`/status` is the dashboard, printed into the conversation rather than opened in
a browser:

```
─────────────────────────────────────────────────────────── status
  version   v0.1.0
  model     ollama/qwen3.5-8k:latest  ·  8192 tokens
  mode      auto
  context   940 of 8.2k  (11%)
  session   20260814-172209-2ee0  ·  1 turn  ·  ⎇ main
  cwd       ~/Projects/raunen
  ladder    18 models
            1. openrouter/nvidia/nemotron-3.5-lightning:free  ·  1000000
            2. openrouter/poolside/laguna-s-2.1:free  ·  262144
            … and 16 more
  subagents on
  providers ollama        3 models     http://localhost:11434/v1
            lmstudio      unreachable  http://localhost:1234/v1
            openrouter    411 models   needs OPENROUTER_API_KEY
```

Everything known locally appears at once; the endpoints are probed afterwards
and fill in as they answer, because a report you have to wait for is a worse
report.

**`unreachable` means nothing answered at that URL**, not that the software is
missing. The usual causes are a server that is not running, or one running on a
different port — `lsof -nP -iTCP -sTCP:LISTEN | grep -i llama` will show where
it actually is. Point the provider at it:

```json
"llama": { "base_url": "http://localhost:9931/v1" }
```

`no models` is a third state, and a different problem: the endpoint answered
fine, there is just nothing loaded to talk to.

It answers the questions that otherwise need guessing: whether an endpoint is
actually up, which ones are missing a key, how close the context is to full, and
what the agent will switch to when it runs out of room — before it does, rather
than after.

There is no separate window or web UI. raunen is the interface, and a running
agent has nowhere else to put things. `/model`, `/providers` and `/sessions`
cover the same ground in more detail; from outside, `raunen --running` lists
live instances and `RAUNEN_DEBUG=1` prints per-request token accounting and the
resolved ladder on stderr.

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
| `tab` | cycle auto / accept edits / plan, or take the highlighted completion |
| `@` | mark a file or folder in the prompt |
| `↑` / `↓` | move through the completions while typing `/` or `@` |
| `esc` | cancel the running turn, or drop a pending reply |
| `ctrl+o` | watch a running sub-agent, or step to the next |
| `ctrl+c` | cancel if working, otherwise quit |
| `pgup` / `pgdn` | scroll the transcript |
| `shift+↑` / `shift+↓` | scroll by a line |
| `y` / `n` | answer an approval prompt |
| click a reply | quote it in the input |
| mouse wheel | scroll the transcript |

Typing `/` opens the command list above the input and narrows it as you go, so
the commands below are discoverable without having to remember them. `tab` takes
the highlighted one; `enter` completes a half-typed name and runs one typed out
in full; `esc` puts the list away.

`@` does the same for the files and folders around you, so a question can point
at what it is about — `explain @internal/ui/ui.go` — rather than describing it
and hoping the agent looks in the right place. A bare `@` lists the top of the
tree and `tab` on a folder steps into it, so a path can be walked to as well as
typed. Completing a file leaves a space and carries on with the sentence.

The list is what git considers part of the project: tracked files plus anything
untracked that is not ignored, so a `.gitignore`d build directory never appears.
Outside a repository it is the tree itself, minus the usual heavy directories.
It is read in the background the first time a mention is typed, and again when a
new mention starts against a snapshot more than ten seconds old.

The mention goes to the model as the path it names. Nothing is inlined — the
agent has tools to read what it was pointed at, and choosing what to read out of
a folder is exactly the sort of thing it is for.

| Command | |
|---|---|
| `/model` | choose a model from a list |
| `/model <provider/model>` | switch directly |
| `/favourite` | pin or unpin the current model (`/fav`) |
| `/status` | model, context, ladder and endpoints on one screen |
| `/companion` | your dragon's level and what fed it |
| `/providers` | list configured endpoints |
| `/key <provider>` | add an API key |
| `/sessions` | list saved sessions |
| `/resume <id>` | pick up a saved session |
| `/compact [what to keep]` | summarise the conversation to win back context |
| `/clear` | start a new session, keeping the old one |
| `task` (tool) | the model delegates to a sub-agent |
| `/help` | show this |
| `/quit` | exit |

## Resilience

A ladder is only useful if it remembers what just failed. Otherwise a
rate-limited model is retried every turn, fails every turn, and each one pays
the same tax. Three layers, because failures differ in what they imply:

| Failure | Response |
|---|---|
| `429` rate limit | cool that model down, 30s doubling to 15 minutes |
| `402` needs credits | lock that model out for the session |
| `429` on a shared allowance | rest the whole provider for 30 minutes |
| connection refused, `5xx` | after 3 in a row, take the whole endpoint out for 2 minutes |
| `400`/`404` model rejected | lock that model out for the session |

The distinctions are the point. A throughput quota clears on its own, so waiting
works. An empty balance does not — no amount of waiting adds credit to an
account, so a `402` is a lockout rather than a cooldown. A model name that does
not exist never will exist. And a server that is down takes its other models with
it, so trying them one by one only spends time discovering that.

The endpoint's own message is shown rather than a paraphrase, because it is
usually specific enough to act on:

```
✗ openrouter/moonshotai/kimi-k3 refused — This request requires more credits,
  or fewer max_tokens. You requested up to 65536 tokens, but can only afford
  2666. To increase, visit https://openrouter.ai/settings/credits
⇅ switched to openrouter/nvidia/nemotron-3-ultra-550b-a55b:free — needs credits
```

**A shared allowance takes the provider with it.** A free tier's per-day cap
belongs to the account, not to one model, so when it runs out every free model
behind that provider will refuse too. Recognising that from the refusal turns
eight doomed requests into one:

```
✗ openrouter/nvidia/nemotron-3.5-lightning:free refused — Rate limit exceeded:
  free-models-per-day. Add 10 credits to unlock 1000 free model requests per day
⚠ openrouter taken out of rotation — its allowance is used up, retrying in 30m
```

**A model is only remembered once it answers.** Escalating away from a model
that refused updates the saved default, but only after the replacement has
actually completed a turn — adopting it at the moment of switching churned the
default through a whole ladder of models that were failing too.

**Size only matters when room is the problem.** After a refusal, a slightly
smaller model that answers beats a larger one that will not — insisting on a
roomier window left a whole ladder unused, since a model declaring 1,048,576
tokens outranked every free rung at 1,000,000.

A request that succeeds clears the cooldown and the endpoint's failure count,
so one blip does not suppress a good model.

Sub-agents share the tracker with their caller: what one learns about a failing
endpoint spares the other from finding out again.

`/status` shows what is being held back, which is why a ladder can look shorter
than it is:

```
held back 2 models
          groq/llama-3.3-70b  ·  cooling down for 1m30s
          nvidia/typo         ·  locked out: the endpoint rejected it
```

## Choosing a model

`/model` with no argument opens a searchable list of everything your endpoints
actually serve:

```
╭──────────────────────────────────────────────────────────────╮
│ search › gemma free    2 of 464                              │
│ ❯   openrouter/google/gemma-4-26b-a4b-it:free                │
│     openrouter/google/gemma-4-31b-it:free                    │
╰──────────────────────────────────────────────────────────────╯
```

`↑`/`↓` to move, `enter` selects, `esc` cancels. `•` marks the model in use, and
the count shows how far the search has narrowed things.

**Searching is forgiving**, because model names are long and structured. Terms
are separated by spaces and all of them must match, so `gemma free` does what you
would hope. Each term matches as a substring *or* as a subsequence, so `nemlight`
finds `nemotron-3.5-lightning`. Substring matches come first — they are what
someone typing a name expects — with subsequence matches below them.

The list is fetched rather than configured: every provider is asked over
`GET /v1/models`, which any OpenAI-compatible endpoint implements. Adding a model
to Ollama makes it appear without touching the config, and providers are queried
in parallel so one that is not running does not hold up the rest.

`/model provider/model` still switches directly, without the list.

**Your choice is remembered.** Switching with `/model` updates `default` in the
config, so the next session starts on the model you last chose. The config is
the one place that says which model you use, so that is where the answer lives —
visible and editable rather than hidden in a state file.

`-m` is the one-off: it overrides for a single run and changes nothing on disk.
That is the distinction between the two — `/model` is a decision, `-m` is an
experiment.

A resumed session reopens on the model it was held with, which takes precedence
over the default. Picking up a conversation on a different model is a surprise,
and on a smaller one it can undo the reason it was switched. Automatic
escalation is deliberately excluded from all of this: moving to a roomier model
for one turn is a measure, not a decision, and should not follow you into
tomorrow.

## Favouriting models

The catalogue runs into the hundreds, and the ones you actually use are a handful.
`/favourite` pins the current model so it rises to the top of `/model`, and
running it again on the same model unpins it:

```
★ pinned ollama/qwen3.5-8k:latest to favourites
```

`/favourite <provider/model>` pins a named one without switching to it.

**Favourites float to the top of the list.** They are collected above everything
else, in the order they were pinned, marked with `★` (the `•` still marks the
model in use). A long, alphabetical catalogue becomes the short list you reach
for first, with the rest still there beneath:

```
╭──────────────────────────────────────────────────────────────╮
│ search ›                                                     │
│ ★   ollama/qwen3.5-8k:latest                                 │
│ ★   openrouter/nvidia/nemotron-3.5-lightning:free            │
│     openrouter/google/gemma-4-26b-a4b-it:free                │
╰──────────────────────────────────────────────────────────────╯
```

While the list is open, `ctrl+f` pins or unpins the highlighted model without
leaving — so you can mark several in one pass. The set is stored under
`favourites` in the config, written `0600` like everything else there, so it is
visible and editable:

```json
{
  "favourites": ["ollama/qwen3.5-8k:latest"]
}
```

Pinning is independent of `default`: a favourite is a shortcut to reach a model,
not a decision to use it next time.

## API keys

Choosing a model whose provider has no key would otherwise fail with a `401` a
few seconds later, by which time the moment has passed. Instead a prompt opens:

```
╭──────────────────────────────────────────────────────────────╮
│ api key for groq  — paste it and press enter                 │
│ saved to the config, which is written 0600 — or set           │
│ GROQ_API_KEY instead                                         │
│ › *********                                                  │
│ enter to save  ·  esc to cancel                              │
╰──────────────────────────────────────────────────────────────╯
```

Input is masked, and pasting goes where you are looking: bracketed paste arrives
as its own kind of message rather than as key presses, so an overlay has to claim
it explicitly or a pasted key ends up in the conversation instead.

The key is written to the config with `0600` permissions.
An environment variable is still the better place for a secret, which is why the
prompt names the one this provider reads.

`/key <provider>` opens it directly. Providers whose catalogue cannot be listed
without a key — Groq and Cerebras answer `401` even for the model list — appear
in `/model` as `⊕ add a key for groq`, so the chooser is a way in rather than a
dead end.

## Voice input

Nothing to configure. Dictation tools — Wispr Flow, macOS Dictation, Talon —
insert the finished transcription into whatever has focus, and raunen takes it
the same way it takes anything pasted:

```
╭──────────────────────────────────────────────────────────╮
│ › First sentence of dictation.                           │
│   Second sentence on a new line.                          │
│   Third one.                                             │
╰──────────────────────────────────────────────────────────╯
```

Multi-line dictation arrives intact and the input grows to fit. Nothing is sent
until you press enter, because a paste is a paste — the newlines inside it are
content, not a keypress.

**The one case that does not work** is a tool that types a literal `Return`
between sentences rather than inserting text. That is indistinguishable from
you pressing enter, so the first sentence sends on its own. If your tool behaves
that way, dictate into a scratch buffer and paste, or use `shift+enter` for the
line breaks yourself.

**There is no built-in recording.** It would mean a microphone, a Whisper model
and an audio library — several hundred megabytes and a real runtime, for a job
a dedicated dictation tool already does better and system-wide. The single
static binary is worth more than saving you a keystroke.

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

## Replying to a message

Click any line of a reply and it becomes the thing you are answering, the way a
messaging app does:

```
  ↩ replying to apple +2 lines  esc to drop
  ╭──────────────────────────────────────────────────────────╮
  │ › which of these is a stone fruit?                       │
  ╰──────────────────────────────────────────────────────────╯
```

The whole message is selected, not the line under the pointer — transcript lines
are grouped into messages, so clicking the middle of a reply quotes all of it.
Clicking it again, or clicking empty space, drops the reply; so does `esc`.

It is sent as a markdown quote, which the model already understands, and the
transcript shows it as one:

```
  ─────────────────────────────────────────────────── 11:20
  │ apple
  │ banana
  │ cherry
  ▌ which of these is a stone fruit?

  A cherry is a stone fruit (also called a drupe) …
```

**The mouse wheel scrolls the transcript**, which matters more than usual here:
the alternate screen took the terminal's own scrolling, so without this the wheel
did nothing.

Enabling the mouse has a cost worth stating: click-drag selection now goes to
raunen rather than to your terminal. On the alternate screen that was already
limited, and `pgup`/`pgdn` still work, but it is a trade rather than a free win.

## The companion

The dragon is not decoration: it grows on the one thing every provider charges
in, which is context. `/companion` shows where it is up to.

```
─────────────────────────────────────────────────────── companion
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

  level     8  Bellow
  progress  ███░░░░░░░░░░░░░░░░░  2.1M to Roar
  context   2.4M tokens across 214 turns
  work      512 tool calls  ·  31 delegated tasks
  hatched   2 days ago
  fed by    3 models
            openrouter/nvidia/nemotron-3.5-lightning:free  1.2M  50%
            ollama/qwen3.5-8k:latest                        900k  37%
            groq/llama-3.3                                  300k  12%
```

**It is yours, not a model's.** Tokens from every provider go into the same
total, and the breakdown is there to make that visible: a local 8k model and a
hosted 1M one feed the same dragon. Switching model or starting a session
carries it forward — the state lives in `~/.local/share/raunen/companion.json`,
alongside your sessions rather than in any config.

Ten levels, named from quiet to loud, since raunen is to murmur: **Hush,
Whisper, Murmur, Rumour, Echo, Chant, Chorus, Bellow, Roar, Thunder**. The first
few arrive within a session or two so there is something to see; the last is a
genuine haul at ten million tokens.

**It grows visibly.** An egg through level 3, a coiled young dragon to level 6,
and the full thing from 7 — on the welcome screen as well as in `/companion`, so
progress is something you notice rather than a number you look up. A level-up
appears in the transcript as it happens, and the level rides along in the status
bar as `★8`.

The bar drops it first when the terminal is narrow. A level is the least urgent
thing on that line.

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

### OmniRoute and other gateways

A gateway speaks the same wire format, so it is a provider like any other. For
[OmniRoute](https://github.com/diegosouzapw/OmniRoute), which ships in the
default config:

```json
"omniroute": { "base_url": "http://localhost:20128/v1" }
```

No key: it binds to localhost and trusts local callers, so declaring one would
only make raunen report it as unavailable. If you have exposed yours or
configured it to require a key, add one with `/key omniroute`.

Its `auto/*` combinations work as models — `omniroute/auto/best-coding` routes
per request on its side while raunen treats it as one endpoint:

```
auto · ⎇ main · auto/best-coding · ★6

  ▌ read go.mod and tell me the module name and go version

    ⏺ read  go.mod
      ↳ 28 lines

  The module is named raunen and uses Go 1.25.0.
```

It reports `context_length` per model, so windows are discovered rather than
declared — `auto/best-coding` comes through as 1,048,576 tokens and the usage bar
works without any configuration.

It carries no `"free": true` flag, but it does not need one: anything served from
`localhost` is treated as costing nothing, a gateway included. That is what puts
its catalogue on the fallback ladder, and it is the point of running one — a
gateway with subscriptions attached is exactly what a small local model should
escalate into.

Be aware of the assumption, though. A gateway can route to paid providers, and
raunen cannot see which side of that line a request lands on. If yours bills you,
keep it off the ladder with `"free_fallback": false` and name your rungs
explicitly in `fallback`.

### Local first, subscription when it matters

The arrangement worth copying, and the one this was built for:

```json
{
  "default": "ollama/qwen3.5-8k:latest",
  "auto_switch": true,
  "fallback": ["omniroute/auto/best-coding"]
}
```

Everything runs on the local model — free, unmetered, private — until a
conversation outgrows its window, at which point it moves to a 1M-token model
without dropping anything it has already found:

```
auto · ⎇ main · qwen3.5-8k:latest

    ⏺ read  README.md          ↳ 210 lines
    ⏺ read  internal/ui/ui.go  ↳ 196 lines

  The UI is a terminal user interface that takes the alternate screen …

    ⇅ switched to omniroute/auto/best-coding  — context full at 8192 tokens

auto · ⎇ main · auto/best-coding · ░░░░░░░░░░ 0% · 5.9k
```

That is the whole design in one exchange: cheapest thing that works, a bigger one
only when the work demands it, and no memory lost in between.

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

**Local models count as free too.** Anything served from your own machine joins
the ladder without needing pricing to say so. There is a catch specific to
Ollama: it reports the *architecture's maximum* context, not the window it is
actually serving. qwen3.5 reports 262144 and is served 4096. Only an explicit
`num_ctx` is trusted, so a model without one is reported as unknown rather than
guessed at — otherwise the ladder would happily "upgrade" from an 8192 model to
one it believed had 262144 and that actually had 4096.

To put a roomier local model on the ladder, give it a window and it appears:

```sh
printf 'FROM qwen3.5:latest\nPARAMETER num_ctx 32768\n' > Modelfile
ollama create qwen3.5-32k -f Modelfile
```

`/status` says whether any rung is actually roomier than what you are on, since
escalation skips the rest:

```
ladder    3 models, none roomier than 8192
```

**Endpoints that do not publish prices have to be told.** OpenRouter prices
every model, so its free ones are found automatically. Groq, Cerebras and NVIDIA
publish nothing, and an unstated price cannot be assumed to be zero — so their
provider entries carry `"free": true`, which is what makes them eligible:

```json
"groq":     { "base_url": "https://api.groq.com/openai/v1",      "api_key_env": "GROQ_API_KEY",     "free": true },
"cerebras": { "base_url": "https://api.cerebras.ai/v1",          "api_key_env": "CEREBRAS_API_KEY", "free": true },
"nvidia":   { "base_url": "https://integrate.api.nvidia.com/v1", "api_key_env": "NVIDIA_API_KEY",   "free": true }
```

All three ship in the default config, so getting on the ladder is a key away:

```sh
export GROQ_API_KEY=...      # console.groq.com
export CEREBRAS_API_KEY=...  # cloud.cerebras.ai
export NVIDIA_API_KEY=...    # build.nvidia.com
```

**Name the models you actually want.** An auto-discovered ladder is capped at
eight rungs, because these catalogues are large and undiscriminating — NVIDIA
lists over a hundred models including vision and embedding models that cannot
answer a chat turn at all, and rotating through those on a rate limit takes
longer than failing. Rungs with a known window sort first, so the cap trims the
guesswork rather than the good options.

For a ladder you can rely on, list them explicitly. `fallback` is not capped,
and declaring windows makes escalation able to tell an upgrade from a sideways
move:

```json
{
  "auto_switch": true,
  "fallback": ["groq/llama-3.3-70b-versatile", "nvidia/meta/llama-3.1-405b-instruct"],
  "models": {
    "groq/llama-3.3-70b-versatile":        { "context": 131072 },
    "nvidia/meta/llama-3.1-405b-instruct": { "context": 131072 }
  }
}
```

`/model` reaches every model these endpoints serve regardless of the ladder —
except the ones that cannot hold a conversation, which are filtered out
everywhere. A catalogue lists plenty of those: OpenRouter has 61 `:batch`
variants reachable only through a separate batch endpoint, and image and music
models sit alongside the chat ones. A model only counts as usable if everything
it outputs is text, since a music model declares `text+audio` and would
otherwise pass — one of them reached the fallback ladder before this check
existed.

When an endpoint does refuse a model, its own words are shown rather than a
paraphrase, because they are usually specific enough to act on:

```
✗ openrouter/minimax/minimax-m3:batch refused
  — This model is only available through the Batch API.
⇅ switched to openrouter/nvidia/nemotron-3-ultra-550b-a55b:free
```

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

## Cancelling

`esc` stops the turn, including anything a sub-agent is doing and any command
either of them is running.

Getting that right took more than passing a context around. A command runs in its
own process group and the whole group is signalled, because killing the shell
leaves its children alive holding the output pipe — and reading that pipe then
blocks on an EOF that never comes. `esc` during an `npx install` appeared to do
nothing at all until the install finished on its own. Measured, the difference is
a command that releases in 300ms rather than one that never releases.

`esc` also drops a pending reply, but only when nothing is running: stopping the
agent is what the key is for, and after clicking a message mid-turn the first
press otherwise looked like it had been ignored.

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

**Several run at once.** When the model delegates more than one task in a turn
they run concurrently, because a sub-agent spends its time waiting on a model
rather than on the machine: three of them against a hosted endpoint finish in
roughly the time of the slowest instead of the sum. Ordinary tools stay
sequential — two edits racing on one file is a worse failure than any saving is
worth — and approval prompts queue, since you have one keyboard.

While they run the status row says so, and `ctrl+o` opens a panel to watch one:

```
◆ ⠹ 3 sub-agents · 15 steps  ctrl+o to watch
```

Pressing it again steps to the next, and the press after the last puts the panel
away. Sub-agents work in their own panel above the input, so their steps never
flood the transcript:

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

**Growing beats summarising beats shrinking.** When a conversation outgrows the
window, raunen tries three things in that order.

First it climbs the fallback ladder, because a roomier model costs nothing but
the switch. Then it **compacts**: one model call writes down what the older
messages established — paths, symbols, decisions, what is still open — and the
summary goes in where they were. Only if that is impossible does it **trim**,
dropping the oldest messages whole.

The order is the point. Trimming makes the model forget what it already found
and investigate the same thing again, so it is the last resort and it says so:

```
⋯ dropped 4 earlier messages — no roomier model to switch to, so the agent
  may repeat work it has already done
```

Compaction happens by itself when the window fills, and reports what it won:

```
⋯ context was full, so the conversation was compacted — 120 messages into a
  summary, 87% smaller
  240k → 31k tokens, keeping the last 6 messages in full
```

**`/compact` does it on demand**, before the window forces the issue — useful
when you are about to start something large and would rather pay for the summary
now. `/compact <what to keep>` passes an instruction to the summariser:
`/compact the migration plan and why we rejected the first one`.

The recent tail is never summarised. Whatever you are working on right now is
worth more in full than in précis, and the last three tenths of the budget stays
verbatim. The summary rejoins the conversation as a labelled record rather than
as something you said, the material is flattened to plain text so no endpoint
has to reason about tool calls it was never given schemas for, and a compaction
that fails leaves the conversation exactly as it was — so trying one costs a
model call and nothing else. Sub-agents are never compacted: a sub-agent's whole
context is discarded a few steps later anyway.

**Tool output is capped** relative to the context, about a quarter of it per
result — a fixed 30 KB cap is ~8000 tokens, larger than the whole context of a
model served at 4096, so one `read` would evict everything around it.

**Trimming keeps the request valid.** The system prompt and the question being
answered are never dropped, everything else goes oldest first, in whole
tool-call groups so no result is left referring to a call that has gone.

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
internal/agent             the tool-use loop, modes, compaction and trimming
internal/companion         the mascot's progress across sessions
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
must survive untouched), history trimming and compaction (tool-call groups that
must stay intact either way, and a failed summary that must leave the
conversation alone), the read-only command allowlist (from both directions), and
stream termination (a truncated response must not look like an empty one).

## Licence

MIT
