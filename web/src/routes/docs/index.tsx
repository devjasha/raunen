import { createFileRoute, Link } from "@tanstack/react-router";
import { REPO } from "../../components/Chrome";

export const Route = createFileRoute("/docs/")({
  head: () => ({ meta: [{ title: "Docs — raunen" }] }),
  component: Overview,
});

function Overview() {
  return (
    <>
      <h1>Docs</h1>
      <p className="lead">
        raunen is a terminal agent for local LLMs: one Go binary that reads and
        writes files, runs commands, and keeps the conversation where you can see
        it.
      </p>

      <p>
        It works in the directory you start it in. Tools are rooted there, so{" "}
        <code>raunen</code> in a project is what gives it something to read. There
        is no separate window and no web UI — a running agent has nowhere else to
        put things.
      </p>

      <h2>Where to go</h2>
      <ul>
        <li>
          <Link to="/docs/install">Install</Link> — one command, or build from
          source with Go 1.25.
        </li>
        <li>
          <Link to="/docs/configuration">Configuration</Link> — providers, models
          and where the files live.
        </li>
        <li>
          <Link to="/docs/modes">Modes</Link> — how much rope the agent gets, and
          how that is decided.
        </li>
        <li>
          <Link to="/docs/commands">Commands &amp; keys</Link> — everything{" "}
          <code>/help</code> lists.
        </li>
        <li>
          <Link to="/docs/models">Models &amp; ladders</Link> — choosing one, and
          escalating when it runs out of room.
        </li>
        <li>
          <Link to="/docs/subagents">Sub-agents</Link> — delegating investigation,
          several at a time.
        </li>
        <li>
          <Link to="/docs/mcp">MCP servers</Link> — adding tools from outside.
        </li>
        <li>
          <Link to="/docs/local-models">Local models</Link> — context windows, and
          the Ollama catch that bites everyone.
        </li>
        <li>
          <Link to="/cost">What it costs</Link> — free, and how to keep it that
          way.
        </li>
      </ul>

      <div className="note">
        <p>
          <strong>These pages summarise the README.</strong> It is the canonical
          reference and goes into more detail on every topic here — the{" "}
          <a href={`${REPO}#readme`}>full README</a> is worth reading once.
        </p>
      </div>

      <h2>The short version</h2>
      <p>
        Install it, have something serving an OpenAI-compatible API, and run{" "}
        <code>raunen</code> in a project. The first run writes a config, asks your
        endpoints what they serve and picks a model — preferring anything local.{" "}
        <code>/help</code> lists everything and <code>ctrl+c</code> leaves. The
        conversation is saved, so <code>raunen --continue</code> picks it back up
        tomorrow.
      </p>

      <div className="pager">
        <span />
        <Link to="/docs/install">Install →</Link>
      </div>
    </>
  );
}
