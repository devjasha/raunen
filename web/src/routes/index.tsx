import { createFileRoute, Link } from "@tanstack/react-router";
import {
  Header,
  Footer,
  Term,
  CopyLine,
  INSTALL_CMD,
  REPO,
  U,
  T,
  D,
  G,
} from "../components/Chrome";
import { DRAGON, DragonArt } from "../components/dragon";

export const Route = createFileRoute("/")({
  head: () => ({
    meta: [
      { title: "raunen — a terminal agent for local LLMs" },
      {
        name: "description",
        content:
          "One Go binary, no runtime, no server. Point it at Ollama, LM Studio, llama.cpp, vLLM or OpenRouter.",
      },
    ],
  }),
  component: Home,
});

function Home() {
  return (
    <>
      <Header />
      <main>
        <div className="hero">
          <div className="wrap">
            <DragonArt className="dragon-hero" />

            <h1>A small terminal agent for local LLMs.</h1>
            <p className="tagline">
              <strong>One Go binary, no runtime, no server.</strong> Point it at
              Ollama, LM Studio, llama.cpp, vLLM, OpenRouter — anything that speaks
              the OpenAI <code>/v1/chat/completions</code> format. It reads and
              writes files, runs commands, and keeps the conversation where you can
              see it.
            </p>

            <CopyLine cmd={INSTALL_CMD} />
            <p className="beneath">
              macOS and Linux, Intel and ARM · verified against the release
              checksums · about 3 MB · <Link to="/cost">free to run</Link>
            </p>
          </div>
        </div>

        <div className="wrap">
          <div className="stage">
            <DragonArt className="dragon-stage" />
            <div className="stage-term">
              <Term title="~/Projects/raunen">
                <D>{"  ──────────────────────────────────────────────────────── 10:42\n"}</D>
                <U>{"  ▌ what does main.go do?\n"}</U>
                {"\n"}
                <T>{"    ⏺ read"}</T>
                {"  main.go\n"}
                <D>{"      ↳ 84 lines\n"}</D>
                {"\n"}
                {"  It parses flags, loads the config, and starts either the TUI or a\n"}
                {"  single one-shot turn.\n"}
                {"\n"}
                <D>
                  {"  • --continue resumes the last session for the directory\n"}
                  {"  • a prompt as an argument skips the UI entirely\n"}
                </D>
                {"\n"}
                {"  ╭────────────────────────────────────────────────────────────╮\n"}
                {"  │ ›                                                          │\n"}
                {"  ╰────────────────────────────────────────────────────────────╯\n"}
                <D>{"  auto · ⎇ main · qwen3.5-8k:latest · ██░░░░░░░░ 22% · 1.8k"}</D>
              </Term>
            </div>
          </div>
        </div>

        <div className="wrap">
          <ul className="reasons">
            <li>
              <strong>Free to run.</strong> Point it at a local model and the bill
              is zero; a hosted model is only used when a conversation outgrows the
              window.
            </li>
            <li>
              <strong>Tiny and self-contained.</strong> One ~3 MB Go binary, no
              runtime, no server, no node_modules.
            </li>
            <li>
              <strong>Yours alone.</strong> No account, no sign-in, no telemetry.
              Your code never leaves the machine unless you point it somewhere.
            </li>
            <li>
              <strong>It is the terminal.</strong> No separate window and no web
              UI — the conversation lives where you already are.
            </li>
          </ul>
        </div>

        <section>
          <div className="wrap">
            <h2>What it is</h2>
            <p className="lede">
              An agent that works in the directory you start it in. Tools are rooted
              there, so <code>raunen</code> in a project is what gives it something
              to read. There is no separate window and no web UI — a running agent
              has nowhere else to put things.
            </p>

            <div className="grid">
              <div className="cell">
                <h3>Any endpoint</h3>
                <p>
                  Adding a provider is a base URL, not adapter code — they all speak
                  the same wire format.
                </p>
              </div>
              <div className="cell">
                <h3>Seven tools</h3>
                <p>
                  <code>read</code>, <code>write</code>, <code>edit</code>,{" "}
                  <code>list</code>, <code>bash</code>, <code>grep</code> and{" "}
                  <code>glob</code> — plus anything an MCP server brings along.
                </p>
              </div>
              <div className="cell">
                <h3>Three modes</h3>
                <p>
                  <code>tab</code> cycles auto, accept edits and plan. The mode goes
                  into the system prompt, so the model knows the rules.
                </p>
              </div>
              <div className="cell">
                <h3>Sub-agents</h3>
                <p>
                  Investigation is delegated to a sub-agent with its own empty
                  context, which returns only its answer. Several run at once.
                </p>
              </div>
              <div className="cell">
                <h3>Sessions</h3>
                <p>
                  The conversation is saved per directory.{" "}
                  <code>raunen --continue</code> picks it back up tomorrow.
                </p>
              </div>
              <div className="cell">
                <h3>Visible context</h3>
                <p>
                  A bar in the status row, because running out of room is the most
                  common cause of a bad answer.
                </p>
              </div>
              <div className="cell">
                <h3>Skills &amp; instructions</h3>
                <p>
                  Save a checklist as a <code>SKILL.md</code> and pull it in with{" "}
                  <code>#</code>, or drop an <code>AGENTS.md</code> in the repo so
                  the agent learns the project's conventions.
                </p>
              </div>
              <div className="cell">
                <h3>Search without the shell</h3>
                <p>
                  <code>grep</code> and <code>glob</code> are first-class tools,
                  not bash commands — so they always run and never trip plan mode.
                </p>
              </div>
            </div>
          </div>
        </section>

        <hr className="rule" />

        <section>
          <div className="wrap">
            <div className="split">
              <div>
                <h2>Local first, hosted when it matters</h2>
                <p>
                  Everything runs on the local model — free, unmetered, private —
                  until a conversation outgrows its window, at which point it moves
                  to a roomier one without dropping anything it has already found.
                </p>
                <p>
                  That is the whole design in one exchange: cheapest thing that
                  works, a bigger one only when the work demands it, and no memory
                  lost in between.
                </p>
                <p>
                  <Link to="/cost">What it costs to run →</Link>
                </p>
              </div>
              <Term title="escalation">
                <D>{"auto · ⎇ main · qwen3.5-8k:latest\n"}</D>
                {"\n"}
                <T>{"    ⏺ read"}</T>
                {"  README.md          "}
                <D>{"↳ 210 lines\n"}</D>
                <T>{"    ⏺ read"}</T>
                {"  internal/ui/ui.go  "}
                <D>{"↳ 196 lines\n"}</D>
                {"\n"}
                {"  The UI is a terminal user interface that takes\n"}
                {"  the alternate screen …\n"}
                {"\n"}
                <G>{"    ⇅ switched to omniroute/auto/best-coding\n"}</G>
                <D>{"      — context full at 8192 tokens\n"}</D>
                {"\n"}
                <D>{"auto · ⎇ main · auto/best-coding · ░░░░░░░░░░ 0% · 5.9k"}</D>
              </Term>
            </div>
          </div>
        </section>

        <hr className="rule" />

        <section>
          <div className="wrap">
            <div className="split">
              <div>
                <h2>The dashboard is in the conversation</h2>
                <p>
                  <code>/status</code> answers the questions that otherwise need
                  guessing: whether an endpoint is actually up, which ones are
                  missing a key, how close the context is to full, and what the agent
                  will switch to when it runs out of room.
                </p>
                <p>
                  Everything known locally appears at once and the endpoints fill in
                  as they answer, because a report you have to wait for is a worse
                  report.
                </p>
              </div>
              <Term title="/status">
                <D>{"─────────────────────────────────────────── status\n"}</D>
                {"  version   v0.6.0\n"}
                {"  model     "}
                <U>{"ollama/qwen3.5-8k:latest"}</U>
                {"  ·  8192\n"}
                {"  mode      auto\n"}
                {"  context   940 of 8.2k  "}
                <T>{"(11%)"}</T>
                {"\n"}
                {"  ladder    18 models\n"}
                {"  subagents on\n"}
                {"  providers ollama      "}
                <G>{"3 models"}</G>
                {"     :11434\n"}
                {"            lmstudio    "}
                <D>{"unreachable"}</D>
                {"  :1234\n"}
                {"            openrouter  "}
                <G>{"411 models"}</G>
                {"   needs key"}
              </Term>
            </div>
          </div>
        </section>

        <hr className="rule" />

        <section>
          <div className="wrap">
            <div className="split">
              <div>
                <pre className="dragon" aria-hidden="true" style={{ marginBottom: "1.5rem" }}>
                  {DRAGON}
                </pre>
                <h2>Levels you actually earn</h2>
                <p>
                  The dragon is not decoration: it grows on the one thing every
                  provider charges in, which is context. Feed it enough — across
                  every model, every session — and you climb from a quiet{" "}
                  <strong>Hush</strong> to a roaring <strong>Thunder</strong>.
                </p>
                <p>
                  It is yours, not a model's, and it carries forward between
                  sessions, so the work you put in shows up next time you open
                  raunen.
                </p>
                <p>
                  <a href={`${REPO}#the-companion`}>How levels work →</a>
                </p>
              </div>
              <div>
                <div className="levels">
                  <div className="level"><span className="n">1</span><span className="name">Hush</span></div>
                  <div className="level"><span className="n">2</span><span className="name">Whisper</span></div>
                  <div className="level"><span className="n">3</span><span className="name">Murmur</span></div>
                  <div className="level"><span className="n">4</span><span className="name">Rumour</span></div>
                  <div className="level"><span className="n">5</span><span className="name">Echo</span></div>
                  <div className="level"><span className="n">6</span><span className="name">Chant</span></div>
                  <div className="level"><span className="n">7</span><span className="name">Chorus</span></div>
                  <div className="level"><span className="n">8</span><span className="name">Bellow</span></div>
                  <div className="level"><span className="n">9</span><span className="name">Roar</span></div>
                  <div className="level final"><span className="n">10</span><span className="name">Thunder</span></div>
                </div>
              </div>
            </div>
          </div>
        </section>

        <hr className="rule" />

        <section>
          <div className="wrap">
            <h2>Getting started</h2>
            <p className="lede">
              Three steps, and the third one is asking it something.
            </p>
            <Term title="shell">
              <D>{"# 1. have a model to talk to — anything speaking the OpenAI API\n"}</D>
              <T>{"$"}</T>
              {" ollama pull qwen3.5\n"}
              {"\n"}
              <D>{"# 2. run it in the directory you want to work in\n"}</D>
              <T>{"$"}</T>
              {" cd ~/Projects/my-thing\n"}
              <T>{"$"}</T>
              {" raunen\n"}
              {"\n"}
              <D>{"raunen: no default model set, using ollama/qwen3.5:latest\n"}</D>
              {"\n"}
              <D>{"# 3. ask it something. /help lists everything, ctrl+c leaves."}</D>
            </Term>
            <p className="beneath">
              The first run writes <code>~/.config/raunen/config.json</code> and,
              since no model is configured yet, asks your endpoints what they have
              and picks one — preferring anything local. Read the{" "}
              <Link to="/docs">docs</Link> or the{" "}
              <a href={`${REPO}#readme`}>full README</a>.
            </p>

            <p className="lede" style={{ marginTop: "2.5rem", marginBottom: "0.75rem" }}>
              Or skip the UI entirely and get one answer back:
            </p>
            <Term title="shell">
              <T>{"$"}</T>
              {" raunen --json 'summarise the diff on this branch'\n"}
              <D>{"  → a single JSON document on stdout, ready for a script"}</D>
            </Term>
            <p className="beneath">
              Pass a prompt as an argument for a one-shot turn, or{" "}
              <code>--json</code> for machine-readable output with an exit status.
              See <Link to="/docs/scripting">scripting</Link>.
            </p>
          </div>
        </section>
      </main>
      <Footer />
    </>
  );
}
