import { createFileRoute, Link } from "@tanstack/react-router";
import { Term, U, D, G } from "../../components/Chrome";

export const Route = createFileRoute("/docs/mcp")({
  head: () => ({ meta: [{ title: "MCP servers — raunen" }] }),
  component: Mcp,
});

function Mcp() {
  return (
    <>
      <h1>MCP servers</h1>
      <p className="lead">
        Model Context Protocol servers add tools from outside. Each one is started
        on launch and its tools are registered alongside the built-ins.
      </p>

      <p>
        They live in <code>~/.config/raunen/mcp.json</code> rather than the main
        config, so a server that needs a secret in its env is not shoulder-to-
        shoulder with the model defaults, and can be shared without dragging the
        rest of the config along.
      </p>

      <h2>Two transports</h2>
      <p>
        <code>stdio</code> runs a local subprocess and speaks JSON-RPC over its
        stdin and stdout. <code>http</code> posts to a remote Streamable-HTTP
        endpoint. Empty means stdio.
      </p>
      <Term title="~/.config/raunen/mcp.json">
        {"{\n"}
        {"  "}<U>{'"filesystem"'}</U>{": {\n"}
        {"    "}<U>{'"command"'}</U>{': "npx",\n'}
        {"    "}<U>{'"args"'}</U>{': ["-y", "@modelcontextprotocol/server-filesystem", "."]\n'}
        {"  },\n"}
        {"  "}<U>{'"example"'}</U>{": {\n"}
        {"    "}<U>{'"type"'}</U>{': "http",\n'}
        {"    "}<U>{'"url"'}</U>{': "https://example.com/mcp/",\n'}
        {"    "}<U>{'"headers"'}</U>{': { "Authorization": "Bearer …" }\n'}
        {"  }\n"}
        {"}"}
      </Term>

      <div className="note">
        <p>
          <strong>Headers are forwarded verbatim and nothing expands variables.</strong>{" "}
          A token written here sits in the file in plain text — it is written{" "}
          <code>0600</code>, but an env-var-based server (<code>env</code> on a stdio
          server) is the better place for a secret.
        </p>
      </div>

      <h2>Defined but idle</h2>
      <p>
        A server can be defined and left out of <code>mcp_enabled</code> in the main
        config, so it stays configured but does not start. An empty list means start
        every defined server.
      </p>
      <Term title="config.json">
        {"  "}<U>{'"mcp_enabled"'}</U>{': ["filesystem"]'}
      </Term>

      <h2>Seeing what loaded</h2>
      <p>
        <code>/mcp</code> lists what is defined, what is active, and how many tools
        each one provided.
      </p>
      <Term title="/mcp">
        <D>{"──────────────────────────────────────────────── mcp\n"}</D>
        {"  filesystem       "}<G>{"11 tools"}</G>{"\n"}
        {"  example          "}<D>{"off"}</D>{"\n"}
        <D>{"  1 servers · 11 tools"}</D>
      </Term>
      <p>
        A server that is active but shows <code>not started</code> failed to launch
        or did not complete the handshake — <code>/status</code> and{" "}
        <code>RAUNEN_DEBUG=1</code> say more.
      </p>

      <div className="pager">
        <Link to="/docs/models">← Models &amp; ladders</Link>
        <Link to="/docs/local-models">Local models →</Link>
      </div>
    </>
  );
}
