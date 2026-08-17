import { createFileRoute, Link } from "@tanstack/react-router";
import { Term, T, D } from "../../components/Chrome";

export const Route = createFileRoute("/docs/local-models")({
  head: () => ({ meta: [{ title: "Local models — raunen" }] }),
  component: LocalModels,
});

function LocalModels() {
  return (
    <>
      <h1>Local models</h1>
      <p className="lead">
        Local models run out of room faster than you expect, and that is the most
        common cause of a bad answer. Giving them more room matters more than
        anything else here.
      </p>

      <div className="note">
        <p>
          <strong>The Ollama catch.</strong> It reports the <em>architecture's
          maximum</em> context, not the window it is actually serving. qwen3.5
          reports 262144 and is served 4096.
        </p>
      </div>

      <p>
        Because of that, only an explicit <code>num_ctx</code> is trusted — a model
        without one is reported as unknown rather than guessed at. Otherwise the
        ladder would happily "upgrade" from an 8192 model to one it believed had
        262144 and that actually had 4096.
      </p>

      <h2>Giving a model more room</h2>
      <p>
        Create a variant with the window you want, and it appears on the ladder with
        a real limit:
      </p>
      <Term title="shell">
        <T>{"$"}</T>{" printf 'FROM qwen3.5:latest\\nPARAMETER num_ctx 32768\\n' \\\n"}
        {"    > Modelfile\n"}
        <T>{"$"}</T>{" ollama create qwen3.5-32k -f Modelfile"}
      </Term>
      <p>
        Then point <code>default</code> at it, or declare the window in{" "}
        <Link to="/docs/configuration">the config</Link> if you set it server-wide
        instead.
      </p>

      <h2>Watching the bar</h2>
      <p>
        The status row carries a context bar. When it turns amber,{" "}
        <code>/clear</code> starts fresh — and <code>/status</code> says whether any
        rung on the ladder is actually roomier than what you are on.
      </p>
      <Term title="status bar">
        <D>{"auto · ⎇ main · qwen3.5-8k:latest · ██░░░░░░░░ 22% · 1.8k"}</D>
      </Term>

      <h2>Free, and counted as such</h2>
      <p>
        Anything served from your own machine joins the escalation ladder without
        needing pricing to say so. That is what makes the local-first arrangement
        work: most turns cost nothing, and a hosted rung is there only for the ones
        that genuinely outgrow the window. See{" "}
        <Link to="/cost">what it costs</Link>.
      </p>

      <div className="pager">
        <Link to="/docs/mcp">← MCP servers</Link>
        <Link to="/cost">What it costs →</Link>
      </div>
    </>
  );
}
