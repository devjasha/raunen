import { createFileRoute, Link } from "@tanstack/react-router";
import { Term, U, T, D } from "../../components/Chrome";

export const Route = createFileRoute("/docs/scripting")({
  head: () => ({ meta: [{ title: "Scripting — raunen" }] }),
  component: Scripting,
});

function Scripting() {
  return (
    <>
      <h1>Scripting</h1>
      <p className="lead">
        A prompt as an argument runs one turn and exits. That is enough to drop
        raunen into a pipeline — or, with <code>--json</code>, into a program.
      </p>

      <h2>Plain text</h2>
      <p>
        The answer goes to stdout and everything else — thinking, tool calls,
        switches, warnings — to stderr, so a pipe gets the reply and nothing
        else:
      </p>
      <Term title="shell">
        <T>{"$"}</T>{" raunen 'summarise the diff on this branch' | pbcopy"}
      </Term>

      <h2>Machine-readable</h2>
      <p>
        When a <em>program</em> consumes the result, <code>--json</code> prints one
        document instead:
      </p>
      <Term title="shell">
        <T>{"$"}</T>{" raunen --json 'what does main.go do?'\n"}
        <U>{"{"}</U>
        {"\n"}
        {"  "}
        <U>{'"output"'}</U>
        {': "It parses flags, loads the config, and starts either the TUI or a\\n"\n'}
        {"              \"single one-shot turn.\",\n"}
        {"  "}
        <U>{'"exit_code"'}</U>
        {": 0,\n"}
        {"  "}
        <U>{'"model"'}</U>
        {': "ollama/qwen3.5:latest",\n'}
        {"  "}
        <U>{'"session_id"'}</U>
        {': "20260820-091144-2ee0",\n'}
        {"  "}
        <U>{'"steps"'}</U>
        {": 2,\n"}
        {"  "}
        <U>{'"tool_calls"'}</U>
        {": [\n"}
        {'    { "name": "read", "status": "success" },\n'}
        {'    { "name": "grep", "status": "success" }\n'}
        {"  ],\n"}
        {"  "}
        <U>{'"usage"'}</U>
        {": { "}
        <U>{'"prompt"'}</U>
        {": 4192, "}
        <U>{'"completion"'}</U>
        {": 210, "}
        <U>{'"total"'}</U>
        {": 4402 }\n"}
        <U>{"}"}</U>
      </Term>
      <p>
        <code>raunen --json 'review this branch' | jq -r .output</code> for the
        prose, or <code>jq -e '.tool_calls[] | select(.status=="error")'</code> to
        fail a pipeline when a tool did.
      </p>

      <h2>Failed runs still print JSON</h2>
      <p>
        The document is printed even when the run fails, with the reason in{" "}
        <code>error</code> — a consumer parsing stdout should never have to handle
        "sometimes there is JSON and sometimes there is not":
      </p>
      <Term title="failure">
        {"{\n"}
        {'  "output": "",\n'}
        {"  "}
        <U>{'"error"'}</U>
        {': "This request requires more credits, or fewer max_tokens.",\n'}
        {"  "}
        <U>{'"exit_code"'}</U>
        {": 1,\n"}
        {"  "}
        <U>{'"model"'}</U>
        {': "openrouter/moonshotai/kimi-k3",\n'}
        {"  "}
        <U>{'"tool_calls"'}</U>
        {": [],\n"}
        {"  "}
        <U>{'"usage"'}</U>
        {": { "}
        <U>{'"total"'}</U>
        {": 0 }\n"}
        {"}"}
      </Term>
      <p>
        <code>tool_calls</code> is always an array, never <code>null</code>, so it
        can be measured on a turn that called nothing. A tool that failed is
        reported as <code>"status": "error"</code> with the message, but{" "}
        <em>does not fail the run</em> — the model is told and usually recovers.
        And <code>model</code> is the model that answered, which is not always the
        one asked for, since escalation can move up the ladder mid-turn.
      </p>

      <table>
        <thead>
          <tr>
            <th>Exit</th>
            <th>Meaning</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td>
              <code>0</code>
            </td>
            <td>the turn finished</td>
          </tr>
          <tr>
            <td>
              <code>1</code>
            </td>
            <td>it failed; <code>error</code> says why</td>
          </tr>
          <tr>
            <td>
              <code>130</code>
            </td>
            <td>interrupted with ctrl+c</td>
          </tr>
        </tbody>
      </table>
      <p>
        130 is the shell's convention for SIGINT, there so a script can tell "the
        user stopped this" from "the model failed". An interrupted run still
        prints its document, with whatever the model had produced in{" "}
        <code>output</code>.
      </p>

      <h2>The session is saved either way</h2>
      <p>
        A one-shot turn and an interactive one produce the same kind of
        conversation, so <code>raunen 'question'</code> followed by{" "}
        <code>raunen --continue</code> picks it up. <code>--no-save</code> opts out
        for a throwaway question, and then <code>session_id</code> is empty rather
        than naming a session that was never written. The conversation is saved
        before the result is reported, so a turn that ran out of context is still
        resumable.
      </p>

      <div className="pager">
        <Link to="/docs/skills">← Skills &amp; instructions</Link>
        <Link to="/docs/subagents">Sub-agents →</Link>
      </div>
    </>
  );
}
