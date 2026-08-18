import { createFileRoute, Link } from "@tanstack/react-router";
import { Header, Footer, Term, U, T, D, G, REPO } from "../components/Chrome";

export const Route = createFileRoute("/cost")({
  head: () => ({
    meta: [
      { title: "What it costs — raunen" },
      {
        name: "description",
        content:
          "raunen is MIT-licensed and free. You pay only for the models you choose, and it is built to let you choose ones that cost nothing.",
      },
    ],
  }),
  component: Cost,
});

function Cost() {
  return (
    <>
      <Header />
      <main className="wrap">
        <div className="prose" style={{ padding: "2.5rem 0 1rem" }}>
          <h1>What it costs</h1>
          <p className="lead">
            raunen is free and MIT-licensed. There is no account, no server and no
            telemetry, so there is nothing to bill you for. What can cost money is
            the model you point it at — and the whole design is about letting that be
            nothing too.
          </p>
        </div>

        <div className="price-grid">
          <div className="price">
            <h3>raunen itself</h3>
            <span className="amount">£0</span>
            <p>Forever, for any use. MIT-licensed.</p>
            <ul>
              <li>every feature, no tiers</li>
              <li>no account, no sign-in</li>
              <li>no telemetry of any kind</li>
              <li>commercial use included</li>
            </ul>
          </div>
          <div className="price">
            <h3>Local models</h3>
            <span className="amount">£0</span>
            <p>Ollama, LM Studio, llama.cpp, vLLM — served from your own machine.</p>
            <ul>
              <li>unmetered, no rate limits</li>
              <li>nothing leaves the machine</li>
              <li>works offline</li>
              <li>costs electricity and RAM</li>
            </ul>
          </div>
          <div className="price">
            <h3>Hosted models</h3>
            <span className="amount">provider</span>
            <p>
              Billed by whoever serves the model, on your own key. raunen never sees
              it.
            </p>
            <ul>
              <li>free tiers are used automatically</li>
              <li>your key, your bill, your limits</li>
              <li>mixes freely with local models</li>
            </ul>
          </div>
        </div>

        <div className="prose">
          <div className="note">
            <p>
              <strong>The short version.</strong> Install raunen, run Ollama, and the
              running total is zero. Everything below is about staying there while
              still having somewhere to escalate to when a conversation gets big.
            </p>
          </div>

          <h2>Free is read from the endpoint, not from a list</h2>
          <p>
            Turning on <code>free_fallback</code> appends every model the providers
            report as free to the escalation ladder, roomiest first. On OpenRouter
            today that is 18 models, several with a 1M-token context — so a local
            model that runs out of room can hand off to something far larger without
            a bill.
          </p>
          <Term title="config.json">
            {"{ "}
            <U>{'"auto_switch"'}</U>
            {": true, "}
            <U>{'"free_fallback"'}</U>
            {": true }"}
          </Term>
          <p>
            A model counts as free when <strong>both</strong> its prompt and
            completion prices are zero, read from the endpoint's own pricing rather
            than from a maintained list, so it stays current. Prices that cannot be
            reduced to a single number — some models quote tiered pricing as an array
            — are never assumed to be free.
          </p>
          <p>
            <strong>Local models count as free too.</strong> Anything served from
            your own machine joins the ladder without needing pricing to say so.
          </p>

          <h2>Some endpoints have to be told</h2>
          <p>
            Most endpoints do not publish prices. OpenRouter does; Groq, Cerebras and
            NVIDIA simply do not — and an unstated price cannot be assumed to be
            zero. For those, mark the provider free yourself:
          </p>
          <Term title="config.json">
            <U>{'  "groq"'}</U>
            {": { "}
            <U>{'"base_url"'}</U>
            {': "https://api.groq.com/openai/v1",\n'}
            {"            "}
            <U>{'"free"'}</U>
            {": true }"}
          </Term>

          <h2>Running out is a routing decision, not a failure</h2>
          <p>
            Free tiers refuse with <code>429</code> rather than a bill. raunen treats
            that as a signal to move to the next rung and retry, and remembers what
            failed so a rate-limited model is not tried again every turn.
          </p>
          <table>
            <thead>
              <tr>
                <th>Failure</th>
                <th>What it means</th>
                <th>Response</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td>
                  <code>429</code>
                </td>
                <td>throughput quota, clears on its own</td>
                <td>cool down 30s, doubling to 15m</td>
              </tr>
              <tr>
                <td>
                  <code>402</code>
                </td>
                <td>empty balance, waiting will not help</td>
                <td>locked out for the session</td>
              </tr>
              <tr>
                <td>
                  <code>429</code> shared
                </td>
                <td>the account's daily cap, not one model's</td>
                <td>rest the whole provider 30m</td>
              </tr>
              <tr>
                <td>
                  <code>400</code>/<code>404</code>
                </td>
                <td>a model name that will never exist</td>
                <td>locked out for the session</td>
              </tr>
            </tbody>
          </table>
          <p>
            The distinctions are what keep a free setup usable. A daily cap belongs
            to the account rather than to one model, so recognising it from a single
            refusal turns eight doomed requests into one.
          </p>
          <Term title="a free tier running out">
            <D>{"✗ openrouter/nvidia/nemotron:free refused — Rate limit\n"}</D>
            <D>{"  exceeded: free-models-per-day.\n"}</D>
            <T>{"⚠ openrouter taken out of rotation"}</T>
            {" — its allowance is\n"}
            {"  used up, retrying in 30m"}
          </Term>

          <h2>The arrangement worth copying</h2>
          <p>
            Local by default, with one hosted rung for when a conversation genuinely
            outgrows it. Most turns cost nothing; the ones that cost something are
            the ones that needed the room.
          </p>
          <Term title="config.json">
            {"{\n"}
            {"  "}
            <U>{'"default"'}</U>
            {': "ollama/qwen3.5-8k:latest",\n'}
            {"  "}
            <U>{'"auto_switch"'}</U>
            {": true,\n"}
            {"  "}
            <U>{'"fallback"'}</U>
            {': ["omniroute/auto/best-coding"]\n'}
            {"}"}
          </Term>
          <p>
            <G>Nothing is lost in the handover</G> — the conversation carries across,
            so escalating does not mean starting again.
          </p>

          <h2>What you are charged for elsewhere</h2>
          <p>
            Every provider bills in context, which is why the status bar shows it.
            Two habits do more for a bill than any setting: <code>/clear</code> when
            a conversation is done, and letting{" "}
            <Link to="/docs/subagents">sub-agents</Link> do the searching — a{" "}
            <code>task</code> gets its own empty context and returns only its answer,
            so a long grep does not sit in the main conversation being re-sent every
            turn.
          </p>
          <p>
            Delegating several at once costs no more than delegating them one after
            another — the same tokens are spent either way — but they run
            concurrently, so it costs a great deal less waiting.
          </p>
          <p>
            Tool output is also cleaned before it is charged to the context — ANSI
            codes, progress bars redrawn over themselves and repeated lines are
            stripped. On ordinary output that saves close to nothing; on noisy output
            it saves a lot. See{" "}
            <a href={`${REPO}#tool-output-cleaning`}>the measured numbers</a>, which
            are deliberately unflattering.
          </p>

          <div className="pager">
            <Link to="/docs">← Docs</Link>
            <Link to="/docs/configuration">Configuration →</Link>
          </div>
        </div>
      </main>
      <Footer />
    </>
  );
}
