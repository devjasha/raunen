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
  | RAUNEN_INSTALL_DIR=/usr/local/bin RAUNEN_VERSION=v0.7.0 sh
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
  version   v0.7.0
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
raunen --json 'what changed?'       # one-shot, machine-readable
raunen --image ui.png 'describe it' # one-shot with an attachment
raunen --no-save 'quick question'   # one-shot, leaving no session behind
raunen acp                          # serve the Agent Client Protocol on stdio
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
| `#` | pull a saved skill into the prompt |
| `↑` / `↓` | move through the completions while typing `/`, `@` or `#` |
| `esc` | cancel the running turn, or drop a pending reply |
| `ctrl+o` | watch a running sub-agent: preview, expand, close |
| `←` / `→` | switch between running sub-agents while a panel is open |
| `ctrl+c` | cancel if working, otherwise quit |
| `pgup` / `pgdn` | scroll the transcript |
| `shift+↑` / `shift+↓` | scroll by a line |
| `y` / `n` | answer an approval prompt |
| `a` | approve and stop asking for calls like it |
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

`#` completes the skills you have saved — instructions you would otherwise
retype — and sends them along with the message. See [Skills](#skills).

| Command | |
|---|---|
| `/model` | choose a model from a list |
| `/model <provider/model>` | switch directly |
| `/favourite` | pin or unpin the current model (`/fav`) |
| `/branch` | choose a branch from a list (`/br`) |
| `/branch <name>` | check it out; `-b <name>` creates it |
| `/status` | model, context, ladder and endpoints on one screen |
| `/companion` | your dragon's level and what fed it |
| `/prestige` | start a new climb once your dragon is fully grown |
| `/providers` | list configured endpoints |
| `/skills` | list the skills you can reference with `#` |
| `/permissions` | what runs without asking (`/perms`) |
| `/image <path>` | attach an image to the next message (`/img`) |
| `/images [clear]` | list attached images, or drop them |
| `/paste` | attach the image on the clipboard |
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

## Switching branches

The status bar already tells you which branch you are on. `/branch` lets you
change it without leaving the conversation to go and type `git checkout` in
another window:

```
╭──────────────────────────────────────────────────────────────╮
│ search ›    4 branches                                       │
│ ❯ • main                                                     │
│     fix-login                                                │
│     spike/streaming                                          │
│     release-2                                                │
╰──────────────────────────────────────────────────────────────╯
```

Same list, same keys as `/model`: `↑`/`↓` to move, `enter` to switch, `esc` to
cancel, and typing narrows. **Branches are ordered by their last commit**, not
alphabetically, because the one you want next is nearly always one you touched
recently. Local branches come first, then any that exist only on a remote —
listed under their short name, `fix-login` rather than `origin/fix-login`, since
that is what checking one out creates and what you would have typed.

**A name nobody has yet is offered rather than refused.** Typing a new name is
how most branches start life, so the list ends with an entry to create it:

```
╭──────────────────────────────────────────────────────────────╮
│ search › fix-parser   1 of 4                                 │
│ ❯   ⊕ create branch fix-parser                               │
╰──────────────────────────────────────────────────────────────╯
```

It always goes last, so it can never be picked by accident when a real branch
matches what you typed.

`/branch <name>` switches directly, and `/branch -b <name>` creates, mirroring
git so the habit carries over.

**The conversation survives the switch.** The files under discussion are the
same files, and throwing away the context would be a strange way to reward
tidying up your branches. What the agent must not do is carry on believing the
old branch is checked out, so the switch is noted into the history the model
reads:

```
✓ switched to fix-login  from main
```

**Git's refusal is git's to explain.** A switch with uncommitted changes in the
way fails with the message git wrote, which already says what to do about it:

```
✗ Your local changes to the following files would be overwritten by checkout: main.go
```

Nothing is stashed, committed or forced on your behalf. This is a shortcut to a
`checkout`, not a workflow of its own.

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

## Images

Attach a screenshot, a mockup or a diagram and the model sees it alongside the
question:

```
› /image ./design.png
  ▣ attached design.png  48 KiB  (1 pending)
› why does the sidebar overlap the content?
```

**Or just drag the file onto the window.** A terminal has no notion of a drop —
dragging a file in pastes its path — so a paste that is nothing but paths to
images that exist is taken as a drop and staged. Escaped spaces, quoted paths,
`file://` URLs and several files at once all work.

`/img` is an alias, `/images` lists what is staged and `/images clear` drops it.
On a Mac, `/paste` takes what is on the clipboard; elsewhere it uses `wl-paste`
or `xclip`. Writing a path into the message works too, with no command at all:

```
› have a look at shot.png and tell me what is wrong
```

Headless runs take `--image`, repeatable:

```sh
raunen --image before.png --image after.png 'what changed?'
```

PNG, JPEG, GIF and WebP, up to 20 MiB each. The type is read from the bytes
rather than the extension, because a JPEG saved as `.png` is common enough and
declaring the wrong type makes the endpoint fail for a reason that has nothing
to do with the actual problem.

**Pasting text is never hijacked.** Only a paste consisting entirely of image
paths that exist counts as a drop. Prose that happens to mention `shot.png`, a
path that is not there, a mix of an image and a text file, or anything with a
newline in it all reach the input as text — the asymmetry is deliberate, since
swallowing a paste is far worse than making you type `/image`.

**Attachments go with one message and are then cleared.** An image that stayed
staged would silently ride along with every later question, which is expensive
and confusing. It stays in the transcript, though — a later "what colour was
that button" has to be answerable by looking again, so the picture remains on
the message it was sent with for the rest of the conversation, including after
`--continue` reloads it from disk.

**The filenames are repeated in the prose**, as `[attached: before.png,
after.png]`. An image block on the wire carries no name, so without this the
model can see two pictures and has no way to say which is which.

**A request only changes shape when it carries an image.** Content normally
goes as a plain string; several local runtimes reject the array form outright,
so it is used for the one case that needs it and nothing else.

There is no fallback for a model that cannot see. fx routes those to a hosted
vision model behind the scenes; raunen does not, because the point of the thing
is that it talks to the endpoint you configured and nothing else. Attach an
image to a text-only model and it will tell you it cannot read it.

## Modes

`tab` cycles three modes, shown at the left of the status bar.

| Mode | Behaviour |
|---|---|
| `auto` | every tool runs immediately |
| `accept edits` | read-only tools run freely; anything that changes state asks first |
| `plan` | anything that changes state is refused, so the model investigates and proposes |

In `accept edits` a prompt takes over the status row — `? run write main.go   y
approve  a always  n decline` — and the agent is blocked until answered. `a`
approves and stops asking for calls like it; see
[permission rules](#permission-rules). The mode is also
written into the system prompt, so the model knows the rules instead of learning
them by collecting refusals.

**How "changes state" is decided.** `read`, `list`, `grep` and `glob` never do;
`write` and `edit` always do. `bash` is the awkward one: it can do anything, so it counts as
mutating unless the command matches a conservative allowlist (`ls`, `cat`,
`grep`, `rg`, `find`, `git status|log|diff|show`, and similar). Redirection,
command substitution, env-var prefixes, absolute paths and anything unrecognised
all count as mutating.

That is an allowlist rather than a denylist of dangerous commands, deliberately:
there is no way to enumerate every way a shell can change something, and a wrong
"this is safe" writes to your disk. The cost is that plan mode sometimes refuses
a harmless command it does not recognise.

## Permission rules

Modes are a blunt instrument. `accept edits` asks about every change, and the
twentieth identical prompt gets approved without being read — which is worse
than no prompt at all, because it looks like oversight. What is missing is the
middle: *this* is fine, *that* never is.

```json
"permissions": {
  "bash":  { "git *": "allow", "git push *": "deny" },
  "edit":  { "docs/*": "allow" },
  "write": "ask"
}
```

A tool maps either to one decision for every call — `"write": "ask"` — or to
patterns with their own. `*` is the only wildcard and it spans everything,
separators included: a rule about `docs/` means the whole of `docs/`. That is
deliberately unlike the `glob` tool, where `*` stops at a slash — requiring
`docs/**` here would be a trap that fails towards granting more than intended.

| | |
|---|---|
| `allow` | runs without asking, even in `accept edits` |
| `deny` | refused, in **every** mode |
| `ask` | prompts — the default when nothing matches |

**A deny holds everywhere, `auto` included.** "Never push" is not advice about
one mode, and an unattended agent in `auto` is exactly where the rule most needs
to mean something. It applies to read-only tools too: `"read": {"*.env": "deny"}`
holds even though reading changes nothing.

**Rules refine modes rather than replacing them.** Plan mode still refuses every
change whatever an `allow` says — a mode is a decision about this session, and a
rule written last week should not quietly undo it.

**The most specific rule wins**, measured by how much of the pattern is not a
wildcard, so `git push *` beats `git *`. Naming a tool beats `*`, and where two
equally specific rules disagree the *denial* wins — if the config contradicts
itself, refusing is the safe reading.

Specificity rather than file order, because the block is a JSON object and Go
ranges maps in a random order. "The last matching rule wins" would decide
differently on different runs, which is an unacceptable property for the thing
deciding whether a command may run.

**A malformed rule is reported and dropped**, not fatal:

```
raunen: permissions.bash.git *: unknown decision alow
```

One typo should not take the other nineteen with it, and dropping fails *closed*
— back to asking — so what remains is always a subset of what was requested
rather than a superset.

### Don't ask again

`a` at an approval prompt approves and stops asking for calls like that one, for
the rest of the session:

```
  will not ask again this session for bash git commit *
```

What it grants is deliberately narrow. A command grants the verb — `git commit
*` from `git commit -m "..."` — because the arguments vary between calls while
the verb is what you actually read. A path grants that file alone: approving one
edit to `main.go` says nothing about the rest of the tree.

**Grants are never written to disk.** Agreeing to something once, while looking
at it, is a different act from writing a rule that will apply next month in a
repository you have not thought about yet. If you want it permanent, put it in
the config, where you can see it and change it.

`/permissions` lists what is in force, in the order the rules are matched:

```
──────────────────────────────────────────────────── permissions
  bash git push *               deny
  bash git *                    allow
  edit docs/*                   allow
  granted this session:
  bash npm test *               allow
  most specific first · a deny holds in every mode
```

Sub-agents inherit the rules and the grants. A denial that could be escaped by
delegating past it would not be a denial, and a grant already given should not
have to be given again by every child.

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

## Skills

A skill is a piece of instruction saved under a name. The things people repeat —
a review checklist, a house style, the way commit messages are written here —
are too long to retype and too situational to put in the system prompt, where
you would pay for them on every turn of every session.

A skill is a directory with a `SKILL.md` in it:

```
skills/
└── review/
    └── SKILL.md
```

```markdown
---
name: review
description: review checklist
---

Check for data races, unhandled errors and missing tests. Say what you would
change and why, and do not change it yourself.
```

The frontmatter is optional. Without it the directory names the skill and the
first line describes it, so the smallest possible skill is a markdown file in a
named folder. Keys we do not recognise — `allowed-tools`, `model`, `license` —
are ignored rather than rejected, which is what lets one file serve several
tools.

**`SKILL.md` rather than a format of our own**, for the reason
[`AGENTS.md`](#project-instructions) won the same argument: it is what other
agents already read. These directories are all searched, project first:

```
skills/            .raunen/skills/     .agents/skills/
.claude/skills/    .codex/skills/      .opencode/skills/
```

A repository that has already written its skills down works here without being
adapted, and a skill written here is not wasted on whatever gets used next.
`~/.config/raunen/skills/` holds your own, applied in every project — and a
project skill of the same name wins, because a repository saying how its own
commits are written is more specific than a global preference.

`skills.json` still works and needs no migration:

```json
{
  "commit": {
    "description": "house commit style",
    "prompt": "Imperative mood, lower case, no full stop, under 72 characters."
  }
}
```

Both are folded into one list, and a `SKILL.md` wins a name collision — if you
have written a directory for a skill you already had, the directory is the newer
statement of intent.

Reference one with `#` and it goes to the model with your message:

```
  ──────────────────────────────────────────────────── 11:41
  ▌ #review the diff on this branch
    # review

  Three things worth changing …
```

Typing `#` opens the list the way `/` and `@` do, narrowed as you go, with the
description beside each name; `tab` takes the highlighted one. A bare `#` lists
everything, so the skills are discoverable without having to remember what you
called them. `/skills` prints the same list.

`description` is for you, not for the model — it is what tells one name from
another while you are choosing. The body is what is sent.

`/skills` names the file each one came from, since two skills can share a name
across a project and a home directory and which is winning is exactly what you
want to know when the wrong one is used. A skill that could not be read is
reported on stderr at startup and skipped, rather than silently contributing
nothing.

**The reference stays in the transcript; the prompt does not.** The skill is
appended to the message on its way out, labelled `[skill: review]` so the model
can tell where one set of instructions ends when a message names two. What you
see on screen is what you typed, plus a dim line naming the skills that went
with it. A page of house style redrawn into the conversation every time it is
used would bury the conversation it is about, and the dim line is there because
without it there would be no way to tell a skill that was pulled in from a name
that was misspelled and quietly went to the model as prose.

**A name that is not a skill is left alone.** `#4213`, `#heading` and
`example.com/x#review` are all far more likely to be what they look like than a
typo, and rewriting prose that was never a reference is worse than ignoring one
that was. Naming the same skill twice in a message sends it once.

Skills live outside `config.json` for the same reason MCP servers do, from the
other direction. A config is personal, holds API keys and is a handful of
settings about which model runs; a skill is prose, sometimes a page of it, and
is the part worth committing to a repository and handing to someone else — which
is the argument for a directory of markdown over a string in JSON.

One skill is capped at 32 KB. Past that it has stopped being an instruction and
become a manual, and it is charged to the context of every turn that names it.

## Project instructions

A project has conventions that are not visible in any one file: which command
runs the tests, which directories are generated, the fact that one package is
load-bearing. A model has no way to find those in the time it has to look, so it
guesses, and it guesses the same thing wrong every session.

Put them in `AGENTS.md` at the top of the repository:

```markdown
# raunen

Go 1.25, no runtime dependencies. `go test ./...` before proposing a change.

- `internal/agent` is the tool-use loop; it must stay presentation-free
- Comments explain why, not what
```

It is read at startup and added to the system prompt, so it costs context on
every turn — which is the argument for keeping it short. A page is fine. A
manual belongs in the repository as a document the agent can read when it needs
to, not in the prompt where it is paid for whether or not it comes up.

`AGENTS.md` rather than a name of our own, because it is the file [other
agents](https://agents.md) already read. A repository that has one works here
without being adapted to raunen specifically, and one written for raunen is not
wasted on whatever gets used next.

**Nested files apply to what is under them.** A monorepo does not have one set
of conventions, so every `AGENTS.md` from the top of the tree down to the
working directory is read, outermost first:

```
project/
├── AGENTS.md          ← always
└── apps/web/
    └── AGENTS.md      ← only when working in apps/web
```

The nearest file is read last, which is what makes it win: a rule in a
sub-package is there precisely to override the one at the top. `~/.config/raunen/AGENTS.md`
comes before both and applies everywhere — the place for how *you* work rather
than how one repository does.

The walk stops at your home directory. Everything above a project is shared by
every project, and a stray `AGENTS.md` in `~` or `/tmp` attaching itself to
unrelated work would be a strange way to be helpful; the global file is the
deliberate way to say something once.

**A direct request outranks the file.** The instructions are framed as standing
conventions rather than as rules, because they are not what you asked for right
now. Without that, `always run the full suite` turns a question about one
function into a ten-minute build.

`/status` names what was loaded, since instructions that quietly did not arrive
look exactly like a model ignoring them:

```
  project   AGENTS.md, apps/web/AGENTS.md
```

Files are capped at 32 KB each and 64 KB in total, and what is cut is reported
rather than dropped silently. Sub-agents inherit the same instructions: they
edit the same working directory, and they cannot see the conversation, so a
convention about how the project is built is the one thing they have no other
way to learn.

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

  level     137  Murmur
  progress  ██████████░░░░░░░░░░  1.3M to level 138
  context   186M tokens across 214 turns
  work      512 tool calls  ·  31 delegated tasks
  hatched   2 days ago
  fed by    3 models
            openrouter/nvidia/nemotron-3.5-lightning:free   93M  50%
            ollama/qwen3.5-8k:latest                        69M  37%
            groq/llama-3.3                                  24M  13%
```

**It is yours, not a model's.** Tokens from every provider go into the same
total, and the breakdown is there to make that visible: a local 8k model and a
hosted 1M one feed the same dragon. Switching model or starting a session
carries it forward — the state lives in `~/.local/share/raunen/companion.json`,
alongside your sessions rather than in any config.

**Five hundred levels, ten names.** The names run from quiet to loud, since
raunen is to murmur: **Hush, Whisper, Murmur, Rumour, Echo, Chant, Chorus,
Bellow, Roar, Thunder** — one to every fifty levels. What moves often is the
number; what moves rarely is what your dragon is called, and reaching the next
name is the thing worth noticing.

The cost of a level is `10,000 × (level − 1)²`, so each one asks 20k more than
the one before it. Level 2 lands inside a session, level 10 at 810k is a day of
use, level 137 is 184M, and level 500 at **2.49 billion tokens** is about a year
of heavy daily use. It is meant to be a long climb.

An existing companion keeps every token it was ever fed and is simply re-read
against the longer ladder, so it moves up rather than down — though a dragon
that had reached Thunder on the old ten-level one will find itself back near the
quiet end of a much longer climb.

**It grows visibly.** An egg through level 10, a coiled young dragon to level
50, and the full thing after that — on the welcome screen as well as in
`/companion`, so progress is something you notice rather than a number you look
up. The stages are fixed levels rather than a share of the ladder on purpose: a
third of five hundred levels is months of feeding, and nobody should still be
looking at an egg by then.

A level-up appears in the transcript as it happens, dimly for a plain level and
in full when the name changes:

```
  ★ level 137
  ★ level 151 — Rumour  /companion
```

The level rides along in the status bar as `★137`. The bar drops it first when
the terminal is narrow — a level is the least urgent thing on that line.

**Prestige.** At level 500 the bar reads `fully grown` and `/prestige` starts
the climb again from level 1. It is a command rather than something that happens
on its own: arriving at the top is the achievement, and silently resetting it on
the way past would take that away rather than reward it. Ascending keeps the
record — lifetime tokens, turns, tools, tasks and the hatch date all carry
across — and the count of finished climbs shows as `✦2` beside the level.

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

Two things live beside it rather than in it: `mcp.json` for MCP servers, whose
definitions can carry a token in their env, and `skills.json` for saved prompts,
which are prose and would bury the settings above. Both are written empty on
first run, and a broken one is reported and skipped rather than taken as a
reason not to start.

A third, `AGENTS.md`, is not created for you: an empty `config.json` documents
the settings that exist, but an empty `AGENTS.md` documents nothing, and a file
sitting there waiting to be filled in invites being filled in with what belongs
in a project instead. See [project instructions](#project-instructions).

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

## Scripting

A prompt as an argument runs one turn and exits. The answer goes to stdout and
everything else — thinking, tool calls, switches, warnings — to stderr, so a
pipe gets the reply and nothing else:

```sh
raunen 'summarise the diff on this branch' | pbcopy
```

That is the right shape when a person reads the result. When a *program* does,
`--json` prints one document instead:

```sh
$ raunen --json 'what does main.go do?'
{
  "output": "It parses flags, loads the config, and starts either the TUI or a\nsingle one-shot turn.",
  "exit_code": 0,
  "model": "ollama/qwen3.5:latest",
  "session_id": "20260820-091144-2ee0",
  "steps": 2,
  "tool_calls": [
    { "name": "read", "status": "success" },
    { "name": "grep", "status": "success" }
  ],
  "usage": { "prompt": 4192, "completion": 210, "total": 4402 }
}
```

```sh
raunen --json 'review this branch' | jq -r .output
raunen --json 'do the thing' | jq -e '.tool_calls[] | select(.status=="error")'
```

**The document is printed even when the run fails**, with the reason in `error`.
A consumer parsing stdout should never have to handle "sometimes there is JSON
and sometimes there is not" — that is the whole reason for the mode:

```json
{
  "output": "",
  "error": "This request requires more credits, or fewer max_tokens.",
  "exit_code": 1,
  "model": "openrouter/moonshotai/kimi-k3",
  "steps": 0,
  "tool_calls": [],
  "usage": { "prompt": 0, "completion": 0, "total": 0 }
}
```

**`tool_calls` is always an array**, never `null`, so `.tool_calls | length`
works on a turn that called nothing. A tool that failed is reported as
`"status": "error"` with the message, but *does not fail the run*: the model is
told and usually recovers, which is the tool working as intended rather than the
turn going wrong.

**`model` is the model that answered**, which is not always the one asked for —
escalation moves up the ladder when the context fills, and reporting the model
it was launched with would be a lie.

| Exit | |
|---|---|
| `0` | the turn finished |
| `1` | it failed; `error` says why |
| `130` | interrupted with ctrl+c |

130 is the shell's convention for SIGINT, and it is there so a script can tell
"the user stopped this" from "the model failed" — those want different handling,
and both used to exit `1`. An interrupted run still prints its document, with
whatever the model had produced in `output`.

**The session is saved either way.** A one-shot turn and an interactive one
produce the same kind of conversation, so `raunen 'question'` followed by
`raunen --continue` picks it up — which it did not before, and that was a bug
rather than a policy. `--no-save` opts out for a throwaway question, and then
`session_id` is empty rather than naming a session that was never written.

The conversation is saved before the result is reported, so a turn that ran out
of context is still resumable: what the model did before it failed is usually
most of the work.

## Editors

`raunen acp` speaks the [Agent Client Protocol](https://agentclientprotocol.com)
on stdin and stdout, so an editor can drive raunen the way it would any other
coding agent. In Zed, that is an entry in `settings.json`:

```json
{
  "agent_servers": {
    "raunen": { "command": "raunen", "args": ["acp"] }
  }
}
```

It is the same agent the terminal runs. Same tools, same permission rules, same
`AGENTS.md`, same skills, same escalation ladder — only the front end differs,
which is the whole reason the loop was kept free of any knowledge of the
terminal.

What travels over the wire:

| | |
|---|---|
| `initialize` | version and capabilities |
| `session/new` · `session/load` | start, or reopen one saved on disk |
| `session/prompt` | a turn, streamed as it happens |
| `session/cancel` | stop the running turn |
| `session/set_mode` | auto, accept edits, plan |
| `session/request_permission` | the agent asking the editor |

**The editor answers the approval prompts.** In accept-edits mode a tool that
changes state pauses and asks, exactly as it does in the terminal; the dialogue
appears in the editor instead of on the status row. "Allow and don't ask again"
records the same narrow session grant `a` does — the verb of a command, or one
file — and it applies to sub-agents too.

**A working directory per session.** The editor says which project a session is
for, and the tools, the `AGENTS.md` files and the skills are resolved against
*that* directory rather than against wherever the server happens to have been
started. An editor with three projects open gets three sessions that cannot read
each other's files.

**MCP servers offered by the client are ignored.** raunen starts the servers
named in its own `mcp.json`, which is a file you control. Taking a server
definition off the wire would let whatever is driving the connection run a
subprocess of its choosing, which is a large door to open for a small
convenience.

**What ACP has no word for is still said.** Escalating to a roomier model,
trimming old exchanges, resting a rate-limited endpoint, a sub-agent reporting
back — none of these have an update in the protocol, because ACP describes what
an agent is doing to your code rather than how it is managing a context window.
They are sent as agent thoughts rather than dropped, because a user watching a
spinner should not have to guess why it is slow.

Stdout belongs to the protocol, so everything raunen would otherwise print goes
to stderr: one stray line on stdout is an unparseable frame and a dead
connection.

Images and audio are declared unsupported in the handshake. raunen sends message
content to the provider as a plain string, so promising otherwise would have an
editor offer an attachment button that silently dropped the file.

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

Pressing it again opens the panel to its full window — the steps, and the answer
once the sub-agent has reported back — and a third press puts it away. With
several running, `←` and `→` move the panel between them. Sub-agents work in
their own panel above the input, so their steps never flood the transcript:

```
  ╭──────────────────────────────────────────────────────────╮
  │ ◆ ⠇ working on  Summarize vcs.go and subview.go          │
  │ ⏺ read  internal/vcs/vcs.go                              │
  │   ↳ 38 lines                                             │
  │ ⏺ read  internal/ui/subview.go                           │
  │   ↳ 70 lines                                             │
  ╰──────────────────────────────────────────────────────────╯
  ⠇ working on 2 turns  esc cancels the newest  ·  ctrl+c all
  ╭──────────────────────────────────────────────────────────╮
  │ ›                                                        │
  ╰──────────────────────────────────────────────────────────╯
  auto · ⎇ main · qwen3.5-8k:latest · ██░░░░░░░░ 22% · 1.8k
```

The panel opens when a sub-agent starts, follows it live, and collapses when it
finishes, leaving one line in the transcript. What a sub-agent did is
working-out, not conversation.

**You can keep talking while it runs.** Press enter and the question is answered
straight away, beside the one already in flight, rather than waiting for it.
That is the point of setting a long piece of work going: you carry on.

It cannot go to the same turn — that one is mid-flight, blocked on a tool result
it asked for — so it gets a fork of the conversation: the same tools, everything
said up to that moment, and its own transcript to answer into. The exchange is
folded back when it finishes, so the conversation ends up holding every turn
even though they were answered side by side.

Two answers arriving into one transcript have to be readable apart, so once a
second turn starts every line carries a gutter mark naming the turn it belongs
to — by shape as well as colour, so a screenshot in black and white still reads:

```
  ────────────────────────────────────────────────────────────── 14:02
  ┃ ▌ summarise every file in internal/

  ────────────────────────────────────────────────────────────── 14:02
  ┆ ▌ meanwhile, what does vcs.Branch do?

  ┆ It shells out to git rev-parse and returns the branch name.
  ┃   ⏺ read  internal/ui/markdown.go
```

`esc` cancels the newest turn, so a question asked by mistake can be taken back
without losing the long piece of work still running underneath it; `ctrl+c`
stops everything. The two commands that rewrite the conversation — `/compact`
and `/clear` — still wait for it to be quiet, since there would be nothing
coherent to rewrite otherwise.

**Delegation is a context technique first.** Sub-agents overlap, but against a
local model that buys little: it serves one request at a time — two concurrent
requests to the same Ollama measured *slower* than two sequential ones.

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

## Searching

`grep` finds text, `glob` finds files:

```
  ⏺ grep  func Start
    ↳ 7 lines

  ⏺ glob  **/*_test.go
    ↳ 9 lines
```

Both could be done through `bash` — `grep` and `rg` are on its allowlist, and
models reach for them readily. Three things are wrong with that, and they are
the reason these exist as tools.

**`bash` is not portable.** `rg` is not installed everywhere, BSD and GNU `grep`
disagree on flags, and the model cannot tell which it is talking to until a
command fails. Every session spent tokens rediscovering that.

**`bash` is not gated for searching.** It counts as mutating unless the command
matches the allowlist, so a search with a pipe or a redirect in it asks for
approval in accept mode and is *refused outright in plan mode* — the one mode
where investigating is all there is to do. `grep` and `glob` never change
anything, so they always run.

**`grep -r` is not bounded.** It walks `node_modules` and `.git`, then returns a
megabyte of minified JavaScript, of which the model sees the first page and has
to go fishing through the rest. The answer is in there somewhere.

**What gets searched is what git says the project is** — everything tracked,
plus everything untracked that is not ignored. That is the right list by
definition and it honours `.gitignore` for free. Outside a repository it walks
the tree instead, minus the directories that are never the answer. The same
listing backs `@` completion, so what the model can search and what you can
mention are the same set of files by construction.

Results are sized to be read rather than truncated: 200 matches in total, 20 per
file, long lines clipped, binaries skipped by sniffing for a null byte. When a
cap is hit it says so and suggests narrowing, because silently returning the
first twenty of four hundred matches is how a model concludes something does not
exist.

```
grep  pattern            RE2 regular expression
      path               limit to a directory, or one file
      glob               limit to matching paths, e.g. *.go
      ignore_case        match case-insensitively
      files_only         names only — much cheaper when you just need to know where
      context            0-5 lines either side

glob  pattern            *.go, internal/**/*.json, **/README.md
      path               limit to a directory
```

A glob with no slash in it matches the base name anywhere in the tree, so
`*.go` means what you would expect rather than only matching the top level.
`**` spans directories, and `a/**/b` matches `a/b` too — zero is a number of
directories.

`files_only` is worth reaching for. A broad search across a large repository
costs a few hundred tokens as a list of paths and several thousand as lines,
and knowing which six files to open is usually the actual question.

The two tools cost about 290 tokens of schema on every request. That is the
argument for their descriptions being terse: on a 4k local model the toolset is
already the largest fixed cost in the window.

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

**A turn runs until the work is done.** There is no step limit: a turn ends when
the model stops asking for tools, not when a counter runs out. A long task is a
long task, and being cut off mid-edit is worse than being slow — when the window
fills, raunen escalates to a roomier model and carries on rather than giving up.

If you are testing a model that loops instead of finishing, `max_steps` is an
opt-in backstop. It is absent by default, and absent means unlimited:

```json
{ "max_steps": 200 }
```

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

**What does not fit is kept, not thrown away.** A build log is four thousand
lines of which six matter, and which six is not knowable until the model looks.
So a large result is stored whole and only its head goes into the conversation,
with a handle for the rest:

```
... [2919 more lines, 84828 bytes total. The whole result is kept as r1: call
result with id "r1" and a match pattern to search it, or from and lines to read
on.]
```

The model then calls `result` to search the full text — `{"id": "r1", "match":
"^FAIL"}` finds the one failing package in an 85 KB test run — or to page
through it with `from` and `lines`. Only what it asks for is charged to the
context; the other 82 KB never enters the conversation, so it is not in every
subsequent request either. That is the difference between a 32k window
compacting twice during a build-fix loop and not compacting at all.

The bound lives on the tool registry rather than inside each tool, so anything
callable is covered by construction — including MCP tools, where the output
belongs to somebody else's server and "please keep it short" is not something
that can be asked. Results are held in memory for the session, most recent
first; an expired handle tells the model to run the tool again rather than
leaving it to guess.

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
main.go                    CLI entry, flags, wiring
oneshot.go                 one-shot runs, --json and exit codes
acp.go                     the acp subcommand, and building an agent per directory
internal/acp               Agent Client Protocol over stdio, for editors
internal/agent             the tool-use loop, modes, compaction and trimming
internal/attach            loading images from a path or the clipboard
internal/companion         the mascot's progress across sessions
internal/config            providers, models and saved skills
internal/provider          OpenAI-compatible streaming client
internal/session           saving, resuming, running instances
internal/fileset           what git considers part of the project
internal/instructions      AGENTS.md discovery
internal/permission        standing allow/deny rules and session grants
internal/skills            SKILL.md discovery
internal/tools             bash, read, write, edit, grep, glob, list, result
internal/ui                Bubble Tea TUI
internal/vcs               git branch for the status bar, and switching it
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
