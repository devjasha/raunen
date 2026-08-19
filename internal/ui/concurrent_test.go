package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"raunen/internal/agent"
	"raunen/internal/provider"
)

// The bug this file is about: a message typed while the agent was working was
// held in a one-slot buffer and sent when the turn ended. Asking a second
// question meant waiting for the first, and asking a third overwrote the
// second. Delegating long work and carrying on talking was impossible — which
// is the reason to delegate in the first place.
func TestSecondQuestionIsAnsweredNotQueued(t *testing.T) {
	m := testModel(t)
	first := begin(&m)

	m.input.SetValue("what else is in here?")
	ret, _ := m.onKey(keyPress("enter"))
	mm := ret.(Model)

	if len(mm.turns) != 2 {
		t.Fatalf("%d turns in flight, want 2 — the question was queued rather than asked", len(mm.turns))
	}
	if mm.turns[0] != first {
		t.Error("asking a second question disturbed the turn already running")
	}
	if v := mm.input.Value(); v != "" {
		t.Errorf("the input still holds %q, so the message was not sent", v)
	}
	if !strings.Contains(transcript(mm), "what else is in here?") {
		t.Errorf("the second question was not echoed:\n%s", transcript(mm))
	}
}

// The second turn must not answer into the first turn's transcript: they would
// interleave tool calls into each other's messages and race on the slice.
func TestConcurrentTurnsGetTheirOwnAgent(t *testing.T) {
	m := testModel(t)
	begin(&m)
	m.input.SetValue("a second question")
	ret, _ := m.onKey(keyPress("enter"))
	mm := ret.(Model)

	second := mm.turns[len(mm.turns)-1]
	if second.ag == nil {
		t.Fatal("the second turn has no agent")
	}
	if second.ag == mm.ag {
		t.Error("the second turn shares the conversation's agent, so the two will race on its transcript")
	}
	// What the fork inherits, and how its exchange gets back, is the agent's
	// business and is tested there — see TestForkSeesTheConversationSoFar.
}

// Even a turn with nothing else running is forked. The conversation of record
// must never be the thing that runs: forking reads its transcript, and a
// question asked while an answer is streaming would read it as the running turn
// wrote to it. Only -race catches this, which is why it is asserted directly.
func TestEveryTurnRunsInAFork(t *testing.T) {
	m := testModel(t)
	m.input.SetValue("the only question")
	ret, _ := m.onKey(keyPress("enter"))
	mm := ret.(Model)

	if len(mm.turns) != 1 {
		t.Fatalf("%d turns, want 1", len(mm.turns))
	}
	if mm.turns[0].ag == mm.ag {
		t.Error("a lone turn ran on the conversation itself, so forking a second turn would race with it")
	}
}

// Two answers arriving into one transcript have to be tellable apart, or the
// reader gets two half-conversations spliced line by line.
func TestOverlappingTurnsAreMarked(t *testing.T) {
	m := testModel(t)
	m.input.SetValue("first")
	ret, _ := m.onKey(keyPress("enter"))
	m = ret.(Model)
	if tag := m.turns[0].tag; tag != "" {
		t.Errorf("a turn running alone was marked %q; there is nothing to tell it apart from", tag)
	}

	m.input.SetValue("second")
	ret, _ = m.onKey(keyPress("enter"))
	m = ret.(Model)

	for i, tn := range m.turns {
		if tn.tag == "" {
			t.Errorf("turn %d is unmarked while another runs beside it", i)
		}
	}
	if m.turns[0].tag == m.turns[1].tag {
		t.Error("both turns got the same mark, so the transcript still cannot be read apart")
	}
}

// Events carry their turn, so a reply cannot be written into the wrong answer.
func TestRepliesDoNotBleedBetweenTurns(t *testing.T) {
	m := testModel(t)
	a, b := begin(&m), begin(&m)

	m.onEvent(a, agent.TextDelta{Text: "```\n"})
	m.onEvent(b, agent.TextDelta{Text: "plain prose\n"})

	// The fence belongs to a alone. Sharing markdown state would have b's line
	// swallowed into a's code block.
	if b.md.inCode {
		t.Error("one turn's code fence put another turn's prose inside a code block")
	}
	if !a.md.inCode {
		t.Error("the turn that opened the fence lost it")
	}
}

// A turn that has ended must not have late events written into it.
func TestEventsAfterATurnEndsAreDropped(t *testing.T) {
	m := testModel(t)
	tn := begin(&m)
	m.drop(tn)

	before := len(m.entries)
	if _, cmd := m.onEvent(tn, agent.TextDelta{Text: "late\n"}); cmd != nil {
		t.Error("a dead turn was asked for another event")
	}
	if len(m.entries) != before {
		t.Error("an event from a finished turn was written to the transcript")
	}
}

// Each turn warns about a full context at most once, and one turn warning must
// not silence another.
func TestContextWarningIsPerTurn(t *testing.T) {
	m := testModel(t)
	m.ag.SetContext(1000)
	a, b := begin(&m), begin(&m)

	full := agent.Usage{Usage: provider.Usage{Total: 950}}
	m.onEvent(a, full)
	m.onEvent(a, full)
	m.onEvent(b, full)

	if n := strings.Count(transcript(m), "replies will degrade"); n != 2 {
		t.Errorf("the context warning fired %d times, want once per turn (2)", n)
	}
}

// esc is the finer instrument: it takes back the question just asked without
// abandoning the long piece of work still running underneath it.
func TestEscCancelsTheNewestTurn(t *testing.T) {
	m := testModel(t)
	var stopped []int
	for i := 1; i <= 2; i++ {
		tn := begin(&m)
		n := i
		tn.cancel = func() { stopped = append(stopped, n) }
	}

	m.onKey(keyPress("esc"))
	if len(stopped) != 1 || stopped[0] != 2 {
		t.Errorf("esc stopped %v, want only the newest turn", stopped)
	}
}

// ctrl+c is the panic key: with several turns running the one thing it
// certainly means is "stop".
func TestCtrlCCancelsEverything(t *testing.T) {
	m := testModel(t)
	stopped := 0
	for i := 0; i < 3; i++ {
		begin(&m).cancel = func() { stopped++ }
	}

	ret, _ := m.onKey(keyPress("ctrl+c"))
	if stopped != 3 {
		t.Errorf("ctrl+c stopped %d of 3 turns", stopped)
	}
	if ret.(Model).quit {
		t.Error("ctrl+c quit rather than cancelling, leaving the transcript inconsistent")
	}
}

// A sub-agent belongs to the turn that delegated it. Another turn finishing
// must not close its panel out from under it.
func TestSubAgentsOutliveOtherTurns(t *testing.T) {
	m := testModel(t)
	a, b := begin(&m), begin(&m)
	m.subs = append(m.subs,
		&subView{id: "t1", desc: "a's task", owner: a},
		&subView{id: "t2", desc: "b's task", owner: b})

	m.dropSubsOf(a)

	if len(m.subs) != 1 || m.subs[0].id != "t2" {
		t.Fatalf("subs after one turn ended: %v, want only t2", ids(m.subs))
	}
}

// Commands that rewrite the conversation cannot run while a turn is appending
// to it. They were unreachable mid-turn before, because enter did nothing.
func TestRewritingCommandsRefuseMidTurn(t *testing.T) {
	for _, cmd := range []string{"/clear", "/compact", "/resume"} {
		m := testModel(t)
		m.ag.Note("something worth keeping")
		begin(&m)

		ret, _ := m.command(cmd)
		mm := ret.(Model)
		if len(mm.ag.Messages()) < 2 {
			t.Errorf("%s wiped the conversation a turn was still writing to", cmd)
		}
		if !strings.Contains(transcript(mm), "wait for the turn to finish") {
			t.Errorf("%s did not say why it refused:\n%s", cmd, transcript(mm))
		}
	}
}

func ids(subs []*subView) []string {
	out := make([]string, len(subs))
	for i, s := range subs {
		out[i] = s.id
	}
	return out
}

// A compaction rewrites the very transcript a new turn would be forked from, so
// it is the one thing that still makes a question wait. Briefly: one model call.
func TestQuestionsWaitForACompaction(t *testing.T) {
	m := testModel(t)
	// A compaction is marked by carrying no agent of its own.
	begin(&m).ag = nil

	m.input.SetValue("meanwhile")
	ret, _ := m.onKey(keyPress("enter"))
	mm := ret.(Model)

	if len(mm.turns) != 1 {
		t.Errorf("%d turns, want 1 — a turn was forked from a transcript being rewritten", len(mm.turns))
	}
	if v := mm.input.Value(); v != "meanwhile" {
		t.Errorf("the input holds %q; the message was swallowed rather than left to retry", v)
	}
	if !strings.Contains(transcript(mm), "compacting") {
		t.Errorf("nothing said why the message did not send:\n%s", transcript(mm))
	}
}

// What interleaving actually looks like. Two answers arriving line by line into
// one transcript is the cost of not queueing, so the marks have to make it
// readable — this renders it so the result can be seen rather than assumed.
func TestInterleavedTranscriptStaysReadable(t *testing.T) {
	m := testModel(t)
	m.input.SetValue("first question")
	ret, _ := m.onKey(keyPress("enter"))
	m = ret.(Model)
	m.input.SetValue("second question")
	ret, _ = m.onKey(keyPress("enter"))
	m = ret.(Model)

	a, b := m.turns[0], m.turns[1]
	m.onEvent(a, agent.TextDelta{Text: "answering the first\n"})
	m.onEvent(b, agent.TextDelta{Text: "answering the second\n"})
	m.onEvent(a, agent.TextDelta{Text: "still the first\n"})

	out := rowsOf(m)
	line := func(want string) string {
		t.Helper()
		for _, r := range out {
			if strings.Contains(r, want) {
				return r
			}
		}
		t.Fatalf("no line containing %q in:\n%s", want, strings.Join(out, "\n"))
		return ""
	}
	// A turn's question and its answers share a gutter; the two turns' do not.
	first, second := line("first question"), line("second question")
	if gutter(first) == gutter(second) {
		t.Errorf("the two questions share a gutter %q, so they cannot be told apart", gutter(first))
	}
	if g := gutter(line("answering the first")); g != gutter(first) {
		t.Errorf("the first answer is marked %q but its question %q", g, gutter(first))
	}
	if g := gutter(line("answering the second")); g != gutter(second) {
		t.Errorf("the second answer is marked %q but its question %q", g, gutter(second))
	}
	if g := gutter(line("still the first")); g != gutter(first) {
		t.Errorf("the first turn's later line is marked %q, want %q", g, gutter(first))
	}
	t.Logf("interleaved transcript:\n%s", strings.Join(out, "\n"))
}

// gutter is the turn mark a line carries: the first glyph, when it is one of
// the marks. The question's own bar is not part of it — that says "this is a
// question", not which turn it belongs to.
func gutter(row string) string {
	for _, r := range row {
		if strings.ContainsRune("┃┆╏┇", r) {
			return string(r)
		}
		if r != ' ' {
			return ""
		}
	}
	return ""
}

// A turn that begins alone is unmarked, and acquires a mark only when a second
// joins it. Its opening lines have to be caught up, or one answer reads as an
// unattributed beginning followed by a marked rest.
func TestEarlierLinesGetTheMarkToo(t *testing.T) {
	m := testModel(t)
	m.input.SetValue("first question")
	ret, _ := m.onKey(keyPress("enter"))
	m = ret.(Model)
	m.onEvent(m.turns[0], agent.TextDelta{Text: "written before any mark existed\n"})

	m.input.SetValue("second question")
	ret, _ = m.onKey(keyPress("enter"))
	m = ret.(Model)

	want := gutter(ansi.Strip(m.turns[0].tag))
	for _, row := range rowsOf(m) {
		if !strings.Contains(row, "written before any mark existed") {
			continue
		}
		if got := gutter(row); got != want {
			t.Errorf("a line written before the mark existed reads %q, want %q:\n%s",
				got, want, row)
		}
		return
	}
	t.Error("the line is gone from the transcript")
}
