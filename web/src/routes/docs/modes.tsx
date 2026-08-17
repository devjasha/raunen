import { createFileRoute, Link } from "@tanstack/react-router";
import { Term, T, D } from "../../components/Chrome";

export const Route = createFileRoute("/docs/modes")({
  head: () => ({ meta: [{ title: "Modes — raunen" }] }),
  component: Modes,
});

function Modes() {
  return (
    <>
      <h1>Modes</h1>
      <p className="lead">
        <code>tab</code> cycles three modes, shown at the left of the status bar.
        Start in <code>plan</code> if you would rather it proposed than acted.
      </p>

      <table>
        <thead>
          <tr>
            <th>Mode</th>
            <th>Behaviour</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td><code>auto</code></td>
            <td>every tool runs immediately</td>
          </tr>
          <tr>
            <td><code>accept edits</code></td>
            <td>read-only tools run freely; anything that changes state asks first</td>
          </tr>
          <tr>
            <td><code>plan</code></td>
            <td>anything that changes state is refused, so the model investigates and proposes</td>
          </tr>
        </tbody>
      </table>

      <p>
        In <code>accept edits</code> a prompt takes over the status row and the
        agent is blocked until answered:
      </p>
      <Term title="approval">
        <T>{"? run write main.go"}</T>
        {"   "}
        <D>{"y approve  ·  n decline"}</D>
      </Term>

      <p>
        The mode is also written into the system prompt, so the model knows the
        rules instead of learning them by collecting refusals.
      </p>

      <h2>How "changes state" is decided</h2>
      <p>
        <code>read</code> and <code>list</code> never do; <code>write</code> and{" "}
        <code>edit</code> always do. <code>bash</code> is the awkward one: it can do
        anything, so it counts as mutating unless the command matches a conservative
        allowlist — <code>ls</code>, <code>cat</code>, <code>grep</code>,{" "}
        <code>rg</code>, <code>find</code>,{" "}
        <code>git status|log|diff|show</code> and similar.
      </p>
      <p>
        Redirection, command substitution, env-var prefixes, absolute paths and
        anything unrecognised all count as mutating.
      </p>

      <div className="note">
        <p>
          <strong>That is an allowlist, not a denylist, deliberately.</strong> There
          is no way to enumerate every way a shell can change something, and a wrong
          "this is safe" writes to your disk. The cost is that plan mode sometimes
          refuses a harmless command it does not recognise.
        </p>
      </div>

      <div className="pager">
        <Link to="/docs/configuration">← Configuration</Link>
        <Link to="/docs/commands">Commands &amp; keys →</Link>
      </div>
    </>
  );
}
