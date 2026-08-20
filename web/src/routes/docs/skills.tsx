import { createFileRoute, Link } from "@tanstack/react-router";
import { Term, U, D, T } from "../../components/Chrome";

export const Route = createFileRoute("/docs/skills")({
  head: () => ({ meta: [{ title: "Skills & project instructions — raunen" }] }),
  component: Skills,
});

function Skills() {
  return (
    <>
      <h1>Skills &amp; project instructions</h1>
      <p className="lead">
        Two ways to feed the agent standing context: <strong>skills</strong> are
        instructions you name and pull into a prompt, and <strong>AGENTS.md</strong>{" "}
        is a file a project keeps for whatever agent reads it.
      </p>

      <h2>Skills</h2>
      <p>
        A skill is a piece of instruction saved under a name — a review checklist,
        a house style, the way commits are written here. Too long to retype, too
        situational for the system prompt, where you would pay for it on every turn.
      </p>
      <p>
        It is a directory with a <code>SKILL.md</code> in it. The frontmatter is
        optional; without it the directory names the skill and the first line
        describes it:
      </p>
      <Term title="skills/review/SKILL.md">
        <U>{"---"}</U>
        {"\n"}
        <U>{"name: review"}</U>
        {"\n"}
        <U>{"description: review checklist"}</U>
        {"\n"}
        <U>{"---"}</U>
        {"\n\n"}
        {"Check for data races, unhandled errors and missing tests. Say what you\n"}
        {"would change and why, and do not change it yourself."}
      </Term>
      <p>
        <code>SKILL.md</code> rather than a format of our own, because it is what
        other agents already read. These directories are all searched, project
        first:
      </p>
      <Term title="searched">
        {"skills/            .raunen/skills/     .agents/skills/\n"}
        {".claude/skills/    .codex/skills/      .opencode/skills/"}
      </Term>
      <p>
        A repository that has already written its skills down works here without
        being adapted, and a skill written here is not wasted on the next tool.{" "}
        <code>~/.config/raunen/skills/</code> holds your own, applied in every
        project — and a project skill of the same name wins.
      </p>
      <p>
        The older <code>skills.json</code> still works and needs no migration; the
        two are folded into one list and a <code>SKILL.md</code> wins a name
        collision.
      </p>

      <h2>Using one</h2>
      <p>
        Reference a skill with <code>#</code> and it goes to the model with your
        message. Typing <code>#</code> opens the list the way <code>/</code> and{" "}
        <code>@</code> do, with the description beside each name; <code>tab</code>{" "}
        takes the highlighted one and a bare <code>#</code> lists everything, so
        skills are discoverable without remembering what you called them.{" "}
        <code>/skills</code> prints the same list, naming the file each came from.
      </p>
      <Term title="raunen">
        <D>{"  ▌ #review the diff on this branch"}</D>
        {"\n"}
        <D>{"    # review"}</D>
        {"\n\n"}
        {"  Three things worth changing …"}
      </Term>
      <p>
        The skill is appended to the message on its way out, labelled{" "}
        <code>[skill: review]</code> so the model can tell where one set of
        instructions ends when a message names two. What you see on screen is what
        you typed, plus a dim line naming the skills that went with it. A name that
        is not a skill — <code>#4213</code>, <code>example.com/x#review</code> — is
        left alone. One skill is capped at 32 KB.
      </p>

      <h2>Project instructions</h2>
      <p>
        A project has conventions invisible in any one file: which command runs the
        tests, which directories are generated. Put them in <code>AGENTS.md</code>{" "}
        at the top of the repository, and it is read at startup and added to the
        system prompt. Keep it short — it costs context on every turn.
      </p>
      <Term title="AGENTS.md">
        <U>{"# raunen"}</U>
        {"\n\n"}
        {"Go 1.25, no runtime dependencies. `go test ./...` before a change.\n"}
        {"- `internal/agent` must stay presentation-free\n"}
        {"- Comments explain why, not what"}
      </Term>
      <p>
        It is <code>AGENTS.md</code> rather than a name of our own because it is the
        file other agents already read — one written for raunen is not wasted on
        whatever gets used next.
      </p>
      <p>
        <strong>Nested files apply to what is under them.</strong> Every{" "}
        <code>AGENTS.md</code> from the top of the tree down to the working
        directory is read, outermost first, so a rule in a sub-package overrides
        the one at the top. <code>~/.config/raunen/AGENTS.md</code> comes before
        both and applies everywhere — the place for how <em>you</em> work. The walk
        stops at your home directory, so a stray file up the tree cannot attach
        itself to unrelated work.
      </p>
      <Term title="/status">
        <D>{"  project   AGENTS.md, apps/web/AGENTS.md"}</D>
      </Term>
      <p>
        <code>/status</code> names what was loaded, because instructions that
        quietly did not arrive look exactly like the model ignoring them. Files are
        capped at 32 KB each and 64 KB in total, and what is cut is reported rather
        than dropped silently. Sub-agents inherit the same instructions, since they
        edit the same directory and cannot see the conversation.
      </p>

      <div className="pager">
        <Link to="/docs/wispr-flow">← Wispr Flow</Link>
        <Link to="/docs/configuration">Configuration →</Link>
      </div>
    </>
  );
}
