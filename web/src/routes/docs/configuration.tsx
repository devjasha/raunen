import { createFileRoute, Link } from "@tanstack/react-router";
import { Term, U, D } from "../../components/Chrome";

export const Route = createFileRoute("/docs/configuration")({
  head: () => ({ meta: [{ title: "Configuration — raunen" }] }),
  component: Configuration,
});

function Configuration() {
  return (
    <>
      <h1>Configuration</h1>
      <p className="lead">
        <code>~/.config/raunen/config.json</code>, written on first run. XDG rather
        than <code>~/Library/Application Support</code>, which is not where terminal
        tools are usually configured.
      </p>

      <Term title="~/.config/raunen/config.json">
        {"{\n"}
        {"  "}
        <U>{'"default"'}</U>
        {': "",\n'}
        {"  "}
        <U>{'"providers"'}</U>
        {": {\n"}
        {"    "}
        <U>{'"ollama"'}</U>
        {':       { "base_url": "http://localhost:11434/v1" },\n'}
        {"    "}
        <U>{'"lmstudio"'}</U>
        {':     { "base_url": "http://localhost:1234/v1" },\n'}
        {"    "}
        <U>{'"llamacpp"'}</U>
        {':     { "base_url": "http://localhost:8080/v1" },\n'}
        {"    "}
        <U>{'"openrouter"'}</U>
        {':   { "base_url": "https://openrouter.ai/api/v1",\n'}
        {'                    "api_key_env": "OPENROUTER_API_KEY" }\n'}
        {"  },\n"}
        {"  "}
        <U>{'"models"'}</U>
        {": {}\n"}
        {"}"}
      </Term>

      <p>
        <strong>Adding a provider is a base URL</strong> — no adapter code, because
        they all speak the same wire format. Models are{" "}
        <code>provider/model</code>, split on the first <code>/</code> so names
        containing slashes (<code>hf.co/user/repo</code>) still work.
      </p>
      <p>
        <code>default</code> may be left empty, in which case the first run asks the
        configured endpoints what they serve and picks one, preferring anything
        local.
      </p>
      <p>
        Providers added to the defaults in later versions are merged into an
        existing config on load, so new endpoints appear without a rewrite. Anything
        you have edited is left alone.
      </p>

      <h2>Context windows</h2>
      <p>
        There is no way to ask an OpenAI-compatible endpoint how big a model's
        window is, so it has to be declared. Per-model wins over the provider
        default.
      </p>
      <Term title="config.json">
        {"  "}
        <U>{'"models"'}</U>
        {": {\n"}
        {'    "ollama/qwen3.5-8k:latest": { '}
        <U>{'"context"'}</U>
        {": 8192 }\n"}
        {"  }"}
      </Term>
      <div className="note">
        <p>
          Getting this wrong is the most common cause of a bad answer. See{" "}
          <Link to="/docs/local-models">local models</Link> for why Ollama in
          particular reports a window it is not serving.
        </p>
      </div>

      <h2>API keys</h2>
      <p>
        <code>api_key_env</code> reads the key from the environment, which is the
        better place for a secret. A key can also be stored in the config, which is
        written <code>0600</code>.
      </p>
      <p>
        Choosing a model whose provider has no key would otherwise fail with a{" "}
        <code>401</code> a few seconds later, by which time the moment has passed.
        Instead a masked prompt opens — <code>/key &lt;provider&gt;</code> opens it
        directly.
      </p>
      <Term title="/key groq">
        {"╭────────────────────────────────────────────────╮\n"}
        {"│ api key for groq — paste it and press enter    │\n"}
        <D>{"│ saved to the config, written 0600 — or set     │\n"}</D>
        <D>{"│ GROQ_API_KEY instead                           │\n"}</D>
        {"│ › *********                                    │\n"}
        <D>{"│ enter to save  ·  esc to cancel                │\n"}</D>
        {"╰────────────────────────────────────────────────╯"}
      </Term>

      <h2>Other files</h2>
      <ul>
        <li>
          <code>~/.config/raunen/mcp.json</code> — MCP servers, kept apart so a
          definition carrying a token is not next to the model defaults. See{" "}
          <Link to="/docs/mcp">MCP</Link>.
        </li>
        <li>
          <code>~/.config/raunen/skills.json</code> — saved skills (the newer{" "}
          <code>SKILL.md</code> directories are preferred; both are merged). See{" "}
          <Link to="/docs/skills">skills &amp; instructions</Link>.
        </li>
        <li>
          <code>~/.config/raunen/AGENTS.md</code> — instructions applied to every
          project you open. See{" "}
          <Link to="/docs/skills">skills &amp; instructions</Link>.
        </li>
        <li>
          <code>~/.local/share/raunen/</code> — sessions and the companion's state.
        </li>
      </ul>
      <p>
        The <code>permissions</code> and <code>subagents</code> keys live in{" "}
        <code>config.json</code> itself. See <Link to="/docs/modes">modes</Link> for
        permission rules, and <Link to="/docs/subagents">sub-agents</Link> for
        turning delegation off.
      </p>
      <p>
        <code>raunen -config</code> prints the config path.
      </p>

      <div className="pager">
        <Link to="/docs/install">← Install</Link>
        <Link to="/docs/modes">Modes →</Link>
      </div>
    </>
  );
}
