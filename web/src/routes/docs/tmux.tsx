import { createFileRoute, Link } from "@tanstack/react-router";
import { Term, T, D, U } from "../../components/Chrome";

export const Route = createFileRoute("/docs/tmux")({
  head: () => ({ meta: [{ title: "Using raunen with tmux — raunen" }] }),
  component: Tmux,
});

function Tmux() {
  return (
    <>
      <h1>Using raunen with tmux</h1>
      <p className="lead">
        Running raunen in several panes at once is the natural way to work — one
        per project, say. <code>prefix + R</code> lists every instance and jumps
        to its pane.
      </p>

      <p>
        Add this to <code>~/.tmux.conf</code>:
      </p>
      <Term title="tmux.conf">
        <U>{"bind R display-popup -E -w 70% -h 50% \"raunen-picker\""}</U>
      </Term>
      <p>
        <code>prefix + R</code> opens a picker populated by{" "}
        <code>raunen --running</code> and filtered through{" "}
        <a href="https://github.com/junegunn/fzf">fzf</a>. Enter jumps to the
        chosen pane, esc cancels. The script lives at{" "}
        <a href="https://github.com/devjasha/raunen/blob/main/contrib/raunen-picker">
          <code>contrib/raunen-picker</code>
        </a>{" "}
        and is meant to run from a tmux popup.
      </p>

      <h2>Why a separate listing</h2>
      <p>
        tmux cannot be asked directly which panes are running raunen, because{" "}
        <code>pane_current_command</code> reports a pane's <em>foreground</em>{" "}
        process — which during a turn is whatever the agent happens to be running
        rather than raunen itself. A pane mid-tool would show <code>ollama</code>{" "}
        or <code>git</code>, not <code>raunen</code>.
      </p>
      <p>
        So each instance announces itself instead. On start it writes a record to{" "}
        <code>~/.local/share/raunen/running/</code> — pid, pane, directory, model
        and the session title — and removes it on exit. <code>raunen --running</code>{" "}
        reads that directory and prints one line per live instance:
      </p>
      <Term title="shell">
        <T>{"$"}</T>{" raunen --running\n"}
        <D>
          {"%3 raunen   45821  raunen   ollama/qwen3.5-8k:latest  (new session)\n"}
        </D>
        <D>
          {"%7 raunen   46002  my-thing  omniroute/auto/best-coding  fix the login\n"}
        </D>
      </Term>

      <div className="note">
        <p>
          <strong>The picker only lists instances inside tmux.</strong> An instance
          running outside a pane has no <code>TMUX_PANE</code> to record, so its
          line is skipped — <code>raunen --running</code> reports it, but the
          jump has nowhere to go. Saved sessions are still reachable with{" "}
          <code>raunen --continue</code> and <code>raunen --resume</code>.
        </p>
      </div>

      <h2>One thing to know</h2>
      <p>
        Taking the alternate screen means tmux's own scrollback and copy-mode no
        longer hold the conversation — it scrolls in-app instead, and sessions are
        saved to disk. See{" "}
        <Link to="/docs/configuration">terminal behaviour</Link> in the README
        for the trade. The picker is the way back to a particular pane when you
        have several open.
      </p>

      <div className="pager">
        <Link to="/docs/local-models">← Local models</Link>
        <Link to="/cost">What it costs →</Link>
      </div>
    </>
  );
}
