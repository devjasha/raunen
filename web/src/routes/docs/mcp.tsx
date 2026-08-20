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

      <h2>Servers connect in the background</h2>
      <p>
        Connecting to a server is a round trip, and waiting for it before drawing
        anything is what used to make raunen slow to start. So servers connect
        alongside the terminal rather than in front of it: the prompt appears
        immediately, and the tools arrive a moment later. The first turn waits for
        them, which in practice has already happened by the time anything is typed.
      </p>
      <p>
        A server is optional this way by default. Set <code>required</code> when a
        turn is not worth starting without it — a workflow built around one
        particular toolset — and accept that its handshake becomes part of how long
        raunen takes to start.
      </p>
      <Term title="~/.config/raunen/mcp.json">
        {"  "}<U>{'"filesystem"'}</U>{": {\n"}
        {"    "}<U>{'"command"'}</U>{': "npx",\n'}
        {"    "}<U>{'"args"'}</U>{': ["-y", "@modelcontextprotocol/server-filesystem", "."],\n'}
        {"    "}<U>{'"required"'}</U>{": true\n"}
        {"  }"}
      </Term>
      <p>
        A server that does not start says why in <code>/mcp</code>, in the words the
        endpoint used, rather than only on the way past.
      </p>

      <h2>Large servers cost nothing until used</h2>
      <p>
        A tool is charged to the context of every request, whether or not it is ever
        called: its name, description and full JSON Schema travel with each turn.
        That is affordable for a handful and ruinous for a server advertising a
        hundred, which can be more schema than a local model has window.
      </p>
      <p>
        So past a handful of tools they are held back and reached through two small
        ones instead — <code>mcp_search_tools</code> to find a tool by keyword, and{" "}
        <code>mcp_select_tool</code> to load it. Only what a task actually needs is
        paid for. A hundred-tool server costs about 215 tokens a request rather than
        11,000, and the model calls the tool normally once it is loaded.
      </p>
      <p>
        Nothing is configured. A small server is registered directly, since the two
        extra tools would cost more than the schemas they save.
      </p>

      <h2>A server that logs in</h2>
      <p>
        A remote server that uses OAuth connects like any other: deferred, so the
        terminal draws first and the handshake finishes in the background. What was
        never right was blocking for it — its login prints a URL and waits for a
        browser, and once the terminal has taken over the screen that instruction
        has nowhere to go. So a server that needs a login simply fails to connect
        and says so in <code>/mcp</code>, in the server's own words.
      </p>
      <p>
        Log in deliberately, when there is a terminal to read the URL, with{" "}
        <code>/mcp auth</code>. It opens a browser and, if one cannot be, shows the
        URL in the transcript to finish by hand. The stored token is what makes the
        next run connect quietly.
      </p>
      <Term title="terminal">
        {"/mcp auth github"}
      </Term>
      <p>
        <code>/mcp logout</code> drops a server's stored token. The server keeps
        running on the token it already holds, so logout decides the next run, not
        this one.
      </p>

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
        <Link to="/docs/subagents">← Sub-agents</Link>
        <Link to="/docs/local-models">Local models →</Link>
      </div>
    </>
  );
}
