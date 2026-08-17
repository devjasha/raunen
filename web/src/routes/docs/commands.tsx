import { createFileRoute, Link } from "@tanstack/react-router";
import { Term, T, D } from "../../components/Chrome";

export const Route = createFileRoute("/docs/commands")({
  head: () => ({ meta: [{ title: "Commands & keys — raunen" }] }),
  component: Commands,
});

const COMMANDS: [string, string][] = [
  ["/model", "choose a model from a list"],
  ["/model <provider/model>", "switch directly"],
  ["/favourite", "pin or unpin the current model (/fav)"],
  ["/status", "model, context, ladder and endpoints on one screen"],
  ["/companion", "your dragon's level and what fed it"],
  ["/providers", "list configured endpoints"],
  ["/key <provider>", "add an API key"],
  ["/mcp", "list MCP servers and their tools"],
  ["/sessions", "list saved sessions"],
  ["/resume <id>", "pick up a saved session"],
  ["/clear", "start a new session, keeping the old one"],
  ["/help", "show all of this"],
  ["/quit", "exit"],
];

const KEYS: [string, string][] = [
  ["enter", "send"],
  ["shift+enter / alt+enter", "newline without sending"],
  ["tab", "cycle mode, or take the highlighted completion"],
  ["@", "mark a file or folder in the prompt"],
  ["↑ / ↓", "move through the completions while typing / or @"],
  ["esc", "cancel the running turn, or drop a pending reply"],
  ["ctrl+c", "cancel if working, otherwise quit"],
  ["pgup / pgdn", "scroll the transcript"],
  ["shift+↑ / shift+↓", "scroll by a line"],
  ["y / n", "answer an approval prompt"],
  ["click a reply", "quote it in the input"],
];

function Commands() {
  return (
    <>
      <h1>Commands &amp; keys</h1>
      <p className="lead">
        Typing <code>/</code> opens the command list above the input and narrows it
        as you go, so these are discoverable without having to remember them.
      </p>

      <h2>Command line</h2>
      <Term title="shell">
        <T>{"$"}</T>{" raunen                          "}<D>{"# TUI in the current directory"}</D>{"\n"}
        <T>{"$"}</T>{" raunen -m ollama/qwen3:8b       "}<D>{"# pick a model for this run"}</D>{"\n"}
        <T>{"$"}</T>{" raunen 'what does main.go do?'  "}<D>{"# one-shot; clean stdout"}</D>{"\n"}
        <T>{"$"}</T>{" raunen --continue               "}<D>{"# resume the last session"}</D>{"\n"}
        <T>{"$"}</T>{" raunen --sessions               "}<D>{"# list saved sessions"}</D>{"\n"}
        <T>{"$"}</T>{" raunen --running                "}<D>{"# list running instances"}</D>{"\n"}
        <T>{"$"}</T>{" raunen -config                  "}<D>{"# print the config path"}</D>
      </Term>

      <h2>In the TUI</h2>
      <table>
        <tbody>
          {COMMANDS.map(([cmd, desc]) => (
            <tr key={cmd}>
              <td><code>{cmd}</code></td>
              <td>{desc}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2>Keys</h2>
      <table>
        <tbody>
          {KEYS.map(([key, desc]) => (
            <tr key={key}>
              <td><code>{key}</code></td>
              <td>{desc}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2>Pointing at files with @</h2>
      <p>
        <code>@</code> completes the files and folders around you, so a question can
        point at what it is about — <code>explain @internal/ui/ui.go</code> — rather
        than describing it and hoping the agent looks in the right place. A bare{" "}
        <code>@</code> lists the top of the tree and <code>tab</code> on a folder
        steps into it.
      </p>
      <p>
        The list is what git considers part of the project: tracked files plus
        anything untracked that is not ignored, so a <code>.gitignore</code>d build
        directory never appears. Outside a repository it is the tree itself, minus
        the usual heavy directories.
      </p>
      <div className="note">
        <p>
          <strong>Nothing is inlined.</strong> The mention goes to the model as the
          path it names — the agent has tools to read what it was pointed at, and
          choosing what to read out of a folder is exactly the sort of thing it is
          for.
        </p>
      </div>

      <div className="pager">
        <Link to="/docs/modes">← Modes</Link>
        <Link to="/docs/models">Models &amp; ladders →</Link>
      </div>
    </>
  );
}
