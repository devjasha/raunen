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
  ["/branch", "choose a branch from a list (/br)"],
  ["/branch <name>", "check it out; -b <name> creates it"],
  ["/status", "model, context, ladder and endpoints on one screen"],
  ["/companion", "your dragon's level and what fed it"],
  ["/prestige", "start a new climb once your dragon is fully grown"],
  ["/providers", "list configured endpoints"],
  ["/key <provider>", "add an API key"],
  ["/mcp", "list MCP servers and their tools"],
  ["/sessions", "list saved sessions"],
  ["/resume <id>", "pick up a saved session"],
  ["/compact [what to keep]", "summarise the conversation to win back context"],
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

      <h2>Switching branches</h2>
      <p>
        The status bar already says which branch you are on. <code>/branch</code>{" "}
        changes it without leaving the conversation, using the same list and the
        same keys as <code>/model</code>.
      </p>
      <Term title="raunen">
        {"╭──────────────────────────────────────────────╮\n"}
        {"│ search ›    4 branches                       │\n"}
        {"│ ❯ • main                                     │\n"}
        {"│     fix-login                                │\n"}
        {"│     spike/streaming                          │\n"}
        {"╰──────────────────────────────────────────────╯"}
      </Term>
      <p>
        Branches are ordered by their last commit rather than alphabetically —
        the one you want next is nearly always one you touched recently. Local
        branches come first, then any that exist only on a remote, under the
        short name checking one out would create. Typing a name nobody has yet
        offers to create it, always as the last entry so it is never picked by
        accident.
      </p>
      <div className="note">
        <p>
          <strong>The conversation survives the switch.</strong> The files under
          discussion are the same files. The switch is noted into the history the
          model reads, so it knows not to trust what it read a moment ago — and
          nothing is stashed, committed or forced on your behalf. A switch git
          refuses fails with git's own message.
        </p>
      </div>

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
