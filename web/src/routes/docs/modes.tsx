import { createFileRoute, Link } from "@tanstack/react-router";
import { Term, U, T, D } from "../../components/Chrome";

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

      <h2>Permission rules</h2>
      <p>
        Modes are a blunt instrument. <code>accept edits</code> asks about every
        change, and the twentieth identical prompt gets approved without being read.
        What is missing is the middle: <em>this</em> is fine, <em>that</em> never
        is. So a tool maps either to one decision for every call —{" "}
        <code>"write": "ask"</code> — or to patterns with their own:
      </p>
      <Term title="config.json">
        {"  "}
        <U>{'"permissions"'}</U>
        {": {\n"}
        {'    "bash": { "git *": "allow", "git push *": "deny" },\n'}
        {'    "edit": { "docs/*": "allow" },\n'}
        {'    "write": "ask"\n'}
        {"  }"}
      </Term>
      <table>
        <thead>
          <tr>
            <th>Decision</th>
            <th>Meaning</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td><code>allow</code></td>
            <td>runs without asking, even in <code>accept edits</code></td>
          </tr>
          <tr>
            <td><code>deny</code></td>
            <td>refused, in <strong>every</strong> mode</td>
          </tr>
          <tr>
            <td><code>ask</code></td>
            <td>prompts — the default when nothing matches</td>
          </tr>
        </tbody>
      </table>
      <p>
        <code>*</code> is the only wildcard and it spans everything, separators
        included — a rule about <code>docs/</code> means the whole of{" "}
        <code>docs/</code>. A deny holds everywhere, <code>auto</code> included,
        because "never push" is not advice about one mode. Rules refine modes
        rather than replace them, so plan mode still refuses every change whatever
        an <code>allow</code> says.
      </p>
      <p>
        The most specific rule wins — measured by how much of the pattern is not a
        wildcard, so <code>git push *</code> beats <code>git *</code>. Where two
        equally specific rules disagree, the denial wins, because if the config
        contradicts itself, refusing is the safe reading. A malformed rule is
        reported and dropped, not fatal — and dropping fails <em>closed</em>, back
        to asking.
      </p>
      <p>
        For the moment only: press <code>a</code> at an approval prompt to approve
        and stop asking for calls like that one, for the rest of the session. The
        grant is deliberately narrow — a command grants the verb, a path grants
        that file alone — and is never written to disk. <code>/permissions</code>{" "}
        lists what is in force.
      </p>

      <div className="pager">
        <Link to="/docs/configuration">← Configuration</Link>
        <Link to="/docs/commands">Commands &amp; keys →</Link>
      </div>
    </>
  );
}
