import { createFileRoute, Link } from "@tanstack/react-router";

export const Route = createFileRoute("/docs/wispr-flow")({
  head: () => ({ meta: [{ title: "Using raunen with Wispr Flow — raunen" }] }),
  component: WisprFlow,
});

function WisprFlow() {
  return (
    <>
      <h1>Using raunen with Wispr Flow</h1>
      <p className="lead">
        Wispr Flow is a voice dictation app for macOS and Windows. raunen has no
        built-in voice input, so Wispr Flow is simply a fast way to type your
        prompts — the dictated text lands in raunen's normal input box.
      </p>

      <h2>How it fits together</h2>
      <p>
        raunen's prompt box is an ordinary text input. Wispr Flow does not talk
        to raunen at all; it sits at the OS level and types what you say
        wherever the cursor is. So when raunen is focused, your spoken words
        appear in its input exactly as if you had typed them — and raunen does
        nothing special to make that happen.
      </p>

      <h2>Getting started</h2>
      <ul>
        <li>
          Install <a href="https://wisprflow.ai">Wispr Flow</a> and grant it
          microphone and accessibility access.
        </li>
        <li>
          Launch raunen in a project (<Link to="/docs/install">install</Link> if
          you have not yet).
        </li>
        <li>
          Click into raunen's prompt box, then start speaking — the dictated
          text shows up as you talk. Press <code>Enter</code> to send it like any
          other prompt.
        </li>
      </ul>

      <div className="note">
        <p>
          <strong>It also works in one-shot mode.</strong> Start dictation and
          speak <code>raunen 'fix the failing test in auth.go'</code> into a
          shell, and the quoted text is what Wispr Flow types — useful for a
          hands-off kickoff.
        </p>
      </div>

      <h2>Tips</h2>
      <ul>
        <li>
          <strong>Dictating code.</strong> Wispr Flow handles code reasonably
          well; speak punctuation and it will type it. For anything fiddly, say
          the words and fix the symbols in the editor afterwards.
        </li>
        <li>
          <strong>Auto-insert and paste.</strong> Use Wispr Flow's usual
          auto-insert / paste behaviour to drop longer passages into the input
          without holding the mic open.
        </li>
        <li>
          <strong>Both interfaces.</strong> The interactive TUI and one-shot{" "}
          <code>raunen '...'</code> mode both take typed text, so dictation works
          the same in either.
        </li>
      </ul>

      <h2>A small caveat</h2>
      <p>
        This is an unofficial integration — a community tip, not a feature.
        raunen itself has no awareness of Wispr Flow; the combination is just two
        tools doing their own jobs next to each other.
      </p>

      <div className="pager">
        <Link to="/docs/subagents">← Sub-agents</Link>
        <Link to="/docs/mcp">MCP servers →</Link>
      </div>
    </>
  );
}
