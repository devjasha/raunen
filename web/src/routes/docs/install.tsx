import { createFileRoute, Link } from "@tanstack/react-router";
import { Term, CopyLine, INSTALL_CMD, D, T } from "../../components/Chrome";

export const Route = createFileRoute("/docs/install")({
  head: () => ({ meta: [{ title: "Install — raunen" }] }),
  component: Install,
});

function Install() {
  return (
    <>
      <h1>Install</h1>
      <p className="lead">
        macOS and Linux, Intel and ARM. Downloads the binary for your platform into{" "}
        <code>~/.local/bin</code>, verifies it against the release checksums, and
        tells you if that directory is not on your <code>PATH</code>.
      </p>

      <CopyLine cmd={INSTALL_CMD} />

      <p style={{ marginTop: "1.25rem" }}>
        Nothing is needed at runtime — one static binary, about 3 MB compressed.
      </p>

      <h2>Changing where it goes</h2>
      <p>
        Put the variables on the <code>sh</code> side of the pipe. In{" "}
        <code>VAR=x curl … | sh</code> they would apply to <code>curl</code>, not to
        the shell running the script:
      </p>
      <Term title="shell">
        <T>{"$"}</T>
        {" curl -fsSL https://raw.githubusercontent.com/devjasha/\\\n"}
        {"    raunen/main/install.sh \\\n"}
        {"  | RAUNEN_INSTALL_DIR=/usr/local/bin RAUNEN_VERSION=v0.6.0 sh"}
      </Term>

      <div className="note">
        <p>
          Piping a script into a shell is worth reading first:{" "}
          <code>curl -fsSL …/install.sh | less</code>.
        </p>
      </div>

      <h2>From source</h2>
      <p>Needs Go 1.25 or newer.</p>
      <Term title="shell">
        <T>{"$"}</T>
        {" go build -o ~/.local/bin/raunen ."}
      </Term>

      <h2>Then have a model to talk to</h2>
      <p>
        Anything speaking the OpenAI API will do. With{" "}
        <a href="https://ollama.com">Ollama</a>:
      </p>
      <Term title="shell">
        <T>{"$"}</T>
        {" ollama pull qwen3.5\n"}
        <T>{"$"}</T>
        {" cd ~/Projects/my-thing\n"}
        <T>{"$"}</T>
        {" raunen\n"}
        {"\n"}
        <D>{"raunen: no default model set, using ollama/qwen3.5:latest"}</D>
      </Term>
      <p>
        The first run writes <code>~/.config/raunen/config.json</code> and, since no
        model is configured yet, asks your endpoints what they have and picks one.
        Nothing here assumes a particular model is installed.
      </p>

      <div className="pager">
        <Link to="/docs">← Overview</Link>
        <Link to="/docs/configuration">Configuration →</Link>
      </div>
    </>
  );
}
