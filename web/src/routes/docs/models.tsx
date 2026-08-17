import { createFileRoute, Link } from "@tanstack/react-router";
import { Term, U, T, D } from "../../components/Chrome";

export const Route = createFileRoute("/docs/models")({
  head: () => ({ meta: [{ title: "Models & ladders — raunen" }] }),
  component: Models,
});

function Models() {
  return (
    <>
      <h1>Models &amp; ladders</h1>
      <p className="lead">
        <code>/model</code> with no argument opens a searchable list of everything
        your endpoints actually serve.
      </p>

      <Term title="/model">
        {"╭──────────────────────────────────────────────────╮\n"}
        {"│ search › gemma free    "}<D>{"2 of 464"}</D>{"                  │\n"}
        {"│ "}<T>{"❯"}</T>{"   openrouter/google/gemma-4-26b-a4b-it:free      │\n"}
        {"│     openrouter/google/gemma-4-31b-it:free        │\n"}
        {"╰──────────────────────────────────────────────────╯"}
      </Term>

      <h2>Favourites</h2>
      <p>
        The catalogue runs into the hundreds and the ones you actually use are a
        handful. <code>/favourite</code> pins the current model so it rises to the
        top of <code>/model</code>; running it again unpins it. While the list is
        open, <code>ctrl+f</code> pins or unpins the highlighted model without
        leaving, so you can mark several in one pass.
      </p>
      <p>
        Pinning is independent of <code>default</code>: a favourite is a shortcut to
        reach a model, not a decision to use it next time.
      </p>

      <h2>Switching automatically</h2>
      <p>
        Off by default. Turn it on with a ladder of models, largest last. When the
        conversation outgrows the current model, raunen moves to the next rung
        rather than failing — and nothing already found is lost in the handover.
      </p>
      <Term title="config.json">
        {"{\n"}
        {"  "}<U>{'"auto_switch"'}</U>{": true,\n"}
        {"  "}<U>{'"fallback"'}</U>{": [\n"}
        {'    "ollama/qwen3-coder:30b",\n'}
        {'    "ollama-cloud/qwen3-coder:480b"\n'}
        {"  ]\n"}
        {"}"}
      </Term>
      <p>
        The ladder is yours to define and can mix local and hosted models freely.
        Adding <code>"free_fallback": true</code> appends every model the providers
        report as free, roomiest first — see <Link to="/cost">what it costs</Link>.
      </p>

      <h2>A ladder that remembers</h2>
      <p>
        A ladder is only useful if it remembers what just failed. Otherwise a
        rate-limited model is retried every turn, fails every turn, and each one
        pays the same tax. The full table of failures and responses is on the{" "}
        <Link to="/cost">cost page</Link>; the principles are:
      </p>
      <ul>
        <li>
          <strong>A model is only remembered once it answers.</strong> Escalating
          updates the saved default, but only after the replacement has actually
          completed a turn.
        </li>
        <li>
          <strong>Size only matters when room is the problem.</strong> After a
          refusal, a slightly smaller model that answers beats a larger one that
          will not.
        </li>
        <li>
          <strong>A success clears the record.</strong> One blip does not suppress a
          good model.
        </li>
        <li>
          <strong>Sub-agents share the tracker</strong> with their caller, so what
          one learns spares the other from finding out again.
        </li>
      </ul>
      <p>
        <code>/status</code> shows what is being held back, which is why a ladder
        can look shorter than it is:
      </p>
      <Term title="/status">
        {"held back 2 models\n"}
        <D>{"          groq/llama-3.3-70b  ·  cooling down for 1m30s\n"}</D>
        <D>{"          nvidia/typo         ·  locked out: rejected"}</D>
      </Term>

      <div className="pager">
        <Link to="/docs/commands">← Commands &amp; keys</Link>
        <Link to="/docs/mcp">MCP servers →</Link>
      </div>
    </>
  );
}
