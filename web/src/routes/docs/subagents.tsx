import { createFileRoute, Link } from "@tanstack/react-router";
import { Term, U, T, D, G } from "../../components/Chrome";

export const Route = createFileRoute("/docs/subagents")({
  head: () => ({
    meta: [
      { title: "Sub-agents — raunen" },
      {
        name: "description",
        content:
          "The model delegates self-contained investigation to a sub-agent with its own context. Siblings run concurrently; ctrl+o watches one.",
      },
    ],
  }),
  component: Subagents,
});

function Subagents() {
  return (
    <>
      <h1>Sub-agents</h1>
      <p className="lead">
        The model can delegate a self-contained piece of investigation with the{" "}
        <code>task</code> tool. The sub-agent gets its own empty context, does the
        work, and returns only its final answer.
      </p>

      <Term title="delegation">
        <T>{"  ◆ task"}</T>
        {"  find where the read tool is defined\n"}
        <D>{"      ⏺ bash  grep -rn \"read\" internal/tools\n"}</D>
        <D>{"        ↳ 12 lines\n"}</D>
        <D>{"      ⏺ read  internal/tools/tools.go\n"}</D>
        <D>{"        ↳ 300 lines\n"}</D>
        <D>{"    ↳ returned 180 chars after 3 steps\n"}</D>
        {"\n"}
        {"  The read tool is defined in internal/tools/tools.go\n"}
        {"  and returns the file with line numbers."}
      </Term>

      <h2>What they buy</h2>
      <p>
        <strong>Room, first.</strong> A sub-agent spends its own window reading
        whatever it needs and hands back a short answer. The main conversation pays
        for the answer instead of for everything that produced it — which is the
        difference between finishing a question and running out of context halfway
        through it.
      </p>
      <p>
        <strong>Then time.</strong> When the model delegates more than one task in
        a turn, the siblings run concurrently. A sub-agent spends nearly all of its
        time waiting on a model rather than on your machine, so three against a
        hosted endpoint finish in roughly the time of the slowest instead of the
        sum.
      </p>

      <div className="note">
        <p>
          Against a single local model there is nothing to win — one GPU serves one
          request at a time — but nothing to lose either: the requests queue at the
          server rather than in raunen.
        </p>
      </div>

      <h2>What stays sequential</h2>
      <p>
        Only delegated tasks run in parallel. Ordinary tools do not: two edits
        racing on one file, or a build racing a write, is a worse failure than any
        saving is worth. Results are appended in the order the model asked for them
        however they finish, because a tool result that does not follow its call is
        rejected outright.
      </p>
      <p>
        <strong>Approvals queue too.</strong> In{" "}
        <Link to="/docs/modes">accept edits</Link> mode, two children reaching a
        mutating tool at the same moment would otherwise issue two prompts, and a
        single <code>y</code> would land on whichever asked last — approving
        something you were never shown.
      </p>

      <h2>Watching them</h2>
      <p>
        Their steps do not go into the transcript: you asked for an answer, not for
        a record of how it was found. While they run, the status row under the
        input says so.
      </p>
      <Term title="status row">
        <T>{"◆ ⠹"}</T>
        <G>{" 3 sub-agents"}</G>
        <D>{" · 15 steps  ctrl+o to watch"}</D>
      </Term>
      <p>
        <code>ctrl+o</code> opens a panel on the first, pressing it again steps to
        the next, and the press after the last puts the panel away.
      </p>
      <Term title="ctrl+o">
        {"╭──────────────────────────────────────────────────╮\n"}
        {"│ "}
        <T>{"◆ ⠹ working on"}</T>
        {"  search the agent package"}
        <D>{"  2/3"}</D>
        {"  │\n"}
        {"│ "}
        <D>{"⏺ grep  dispatch internal/agent"}</D>
        {"                  │\n"}
        {"│ "}
        <D>{"  ↳ 7 matches"}</D>
        {"                                    │\n"}
        {"╰──────────────────────────────────────────────────╯"}
      </Term>
      <p>
        A sub-agent nobody is watching costs no rows at all, so the transcript
        keeps its height whether one is running or three.
      </p>

      <h2>Talking while they work</h2>
      <p>
        Delegating long work is only worth it if you can carry on. Press enter
        while an answer is still arriving and the new question is answered beside
        it rather than queued behind it.
      </p>
      <p>
        It cannot join the turn already running — that one is blocked on a tool
        result it asked for — so it gets a fork of the conversation: the same
        tools, everything said up to that moment, and its own transcript to answer
        into. The exchange is folded back when it finishes, so the conversation
        ends up holding every turn even though they were answered side by side.
      </p>
      <p>
        Two answers arriving into one transcript have to be readable apart, so
        once a second turn starts every line carries a gutter mark naming the turn
        it belongs to — by shape as well as colour, so a screenshot in black and
        white still reads.
      </p>
      <Term title="two turns at once">
        <D>{"────────────────────────────────────────────── 14:02\n"}</D>
        <G>{"┃ "}</G>
        <U>{"▌ summarise every file in internal/"}</U>
        {"\n\n"}
        <D>{"────────────────────────────────────────────── 14:02\n"}</D>
        <T>{"┆ "}</T>
        <U>{"▌ meanwhile, what does vcs.Branch do?"}</U>
        {"\n\n"}
        <T>{"┆ "}</T>
        {"It shells out to git rev-parse.\n"}
        <G>{"┃ "}</G>
        <D>{"  ⏺ read  internal/ui/markdown.go"}</D>
      </Term>
      <p>
        <code>esc</code> cancels the newest turn, so a question asked by mistake
        can be taken back without losing the long piece of work still running
        underneath it. <code>ctrl+c</code> stops everything. The commands that
        rewrite the conversation — <code>/compact</code>, <code>/clear</code>,{" "}
        <code>/resume</code> — wait for it to be quiet, since there would be
        nothing coherent to rewrite otherwise.
      </p>

      <h2>Bounds</h2>
      <ul>
        <li>
          <strong>They cannot delegate.</strong> A sub-agent simply does not have
          the <code>task</code> tool, so recursion is prevented structurally rather
          than by a depth counter.
        </li>
        <li>
          <strong>They inherit the mode.</strong> Whatever a child does is gated by
          the same rules as the caller, so a sub-agent's edits are approved in the
          same place as any other.
        </li>
        <li>
          <strong>They are bounded more tightly</strong> than a main turn — 16
          steps. A child that wanders is worse than one that gives up: the caller
          is waiting.
        </li>
        <li>
          <strong>They share the health tracker</strong> with their caller, so what
          one learns about a failing endpoint spares the others from finding out
          again.
        </li>
      </ul>

      <h2>Turning them off</h2>
      <p>
        They cost one more tool schema on every request, which is worth having back
        on a very small window:
      </p>
      <Term title="config.json">
        {"  "}
        <U>{'"subagents"'}</U>
        {": false"}
      </Term>

      <div className="pager">
        <Link to="/docs/models">← Models &amp; ladders</Link>
        <Link to="/docs/mcp">MCP servers →</Link>
      </div>
    </>
  );
}
