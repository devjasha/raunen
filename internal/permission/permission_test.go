package permission

import (
	"encoding/json"
	"strings"
	"testing"
)

// parse builds a set from JSON, failing the test on a problem it did not expect.
func parse(t *testing.T, src string) *Set {
	t.Helper()
	var cfg Config
	if err := json.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("bad test fixture: %v", err)
	}
	set, problems := Parse(cfg)
	if len(problems) > 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	return set
}

// decide is the question the agent asks, shortened for the table tests.
func decide(s *Set, tool, target string) Decision {
	d, _, _ := s.Decide(tool, target)
	return d
}

// TestUnmatchedCallsAsk is the compatibility guarantee: a tool nobody has
// written a rule for behaves exactly as it did before rules existed.
func TestUnmatchedCallsAsk(t *testing.T) {
	s := parse(t, `{"bash": {"git *": "allow"}}`)
	if got := decide(s, "write", "main.go"); got != Ask {
		t.Errorf("unmatched tool = %v, want Ask", got)
	}
	if got := decide(s, "bash", "rm -rf /"); got != Ask {
		t.Errorf("unmatched command = %v, want Ask", got)
	}
}

// TestNilSetAsks covers the common case of no permissions block at all.
func TestNilSetAsks(t *testing.T) {
	var s *Set
	if got := decide(s, "bash", "anything"); got != Ask {
		t.Errorf("nil set = %v, want Ask", got)
	}
}

// TestMoreSpecificRuleWins is the heart of it: "git *" allows, "git push *"
// denies, and pushing must be refused.
func TestMoreSpecificRuleWins(t *testing.T) {
	s := parse(t, `{"bash": {"git *": "allow", "git push *": "deny"}}`)

	if got := decide(s, "bash", "git status"); got != Allow {
		t.Errorf("git status = %v, want Allow", got)
	}
	if got := decide(s, "bash", "git push origin main"); got != Deny {
		t.Errorf("git push = %v, want Deny — the narrower rule must win", got)
	}
}

// TestSpecificityNotFileOrder pins why ordering is by specificity rather than by
// where a rule appears. The config is a JSON object, and Go ranges maps in a
// random order, so "last one wins" would decide differently on different runs —
// an unacceptable property for the thing gating a command.
func TestSpecificityNotFileOrder(t *testing.T) {
	src := `{"bash": {"git push *": "deny", "git *": "allow"}}`
	// Parsed repeatedly: if map order leaked into the decision, this fails
	// intermittently rather than never, so it is worth the loop.
	for i := 0; i < 50; i++ {
		if got := decide(parse(t, src), "bash", "git push origin main"); got != Deny {
			t.Fatalf("run %d: git push = %v, want Deny every time", i, got)
		}
	}
}

// TestDenyWinsATie is the safe direction: two equally specific rules that
// disagree resolve to the one that refuses.
func TestDenyWinsATie(t *testing.T) {
	s := parse(t, `{"bash": {"rm *": "deny"}, "*": {"rm *": "allow"}}`)
	if got := decide(s, "bash", "rm -rf build"); got != Deny {
		t.Errorf("tie resolved to %v, want Deny", got)
	}
}

// TestShortFormAppliesToEveryCall covers "write": "ask", which is the spelling
// for a tool that has no interesting targets.
func TestShortFormAppliesToEveryCall(t *testing.T) {
	s := parse(t, `{"write": "deny", "edit": "allow"}`)
	if got := decide(s, "write", "anything.go"); got != Deny {
		t.Errorf("write = %v, want Deny", got)
	}
	if got := decide(s, "edit", "anything.go"); got != Allow {
		t.Errorf("edit = %v, want Allow", got)
	}
}

// TestWildcardTool lets one rule speak for every tool, which is how "ask about
// everything" is written.
func TestWildcardTool(t *testing.T) {
	s := parse(t, `{"*": "deny"}`)
	for _, tool := range []string{"bash", "write", "edit", "mcp_something"} {
		if got := decide(s, tool, "x"); got != Deny {
			t.Errorf("%s = %v, want Deny", tool, got)
		}
	}
}

// TestToolRuleBeatsWildcard checks a named tool outranks "*" whatever the
// patterns say, since naming a tool is the more specific statement.
func TestToolRuleBeatsWildcard(t *testing.T) {
	s := parse(t, `{"*": "deny", "read": "allow"}`)
	if got := decide(s, "read", "main.go"); got != Allow {
		t.Errorf("read = %v, want Allow: a named tool is more specific than *", got)
	}
}

// TestPatternMatching walks the shapes a rule is actually written in.
func TestPatternMatching(t *testing.T) {
	s := parse(t, `{
		"edit":  {"docs/*": "allow", "*.lock": "deny"},
		"bash":  {"npm test*": "allow"}
	}`)

	for _, tc := range []struct {
		tool, target string
		want         Decision
	}{
		{"edit", "docs/readme.md", Allow},
		{"edit", "docs/deep/nested/file.md", Allow},
		{"edit", "src/main.go", Ask},
		{"edit", "go.lock", Deny},
		{"bash", "npm test", Allow},
		{"bash", "npm test -- --watch", Allow},
		{"bash", "npm publish", Ask},
	} {
		if got := decide(s, tc.tool, tc.target); got != tc.want {
			t.Errorf("%s %q = %v, want %v", tc.tool, tc.target, got, tc.want)
		}
	}
}

// TestStarSpansSeparators is the deliberate difference from the glob tool. A
// rule about docs/ means the whole of docs/, and requiring docs/** would be a
// trap that fails towards granting more than intended.
func TestStarSpansSeparators(t *testing.T) {
	s := parse(t, `{"edit": {"docs/*": "allow"}}`)
	if got := decide(s, "edit", "docs/a/b/c/deep.md"); got != Allow {
		t.Errorf("nested path = %v, want Allow: * spans separators in a rule", got)
	}
}

// TestBadDecisionIsReportedNotGuessed keeps a typo from silently becoming
// something permissive.
func TestBadDecisionIsReportedNotGuessed(t *testing.T) {
	var cfg Config
	_ = json.Unmarshal([]byte(`{"bash": {"git *": "alow"}}`), &cfg)
	set, problems := Parse(cfg)

	if len(problems) == 0 {
		t.Fatal("a misspelled decision was accepted silently")
	}
	if !strings.Contains(problems[0], "alow") {
		t.Errorf("problem does not name the bad value: %q", problems[0])
	}
	// And it must not have become a rule of any kind.
	if got := decide(set, "bash", "git status"); got != Ask {
		t.Errorf("misspelled rule still decided %v", got)
	}
}

// TestOneBadRuleKeepsTheRest is why problems are returned rather than fatal: a
// typo in one pattern should not take the other nineteen with it.
func TestOneBadRuleKeepsTheRest(t *testing.T) {
	var cfg Config
	_ = json.Unmarshal([]byte(`{"bash": {"git *": "allow", "rm *": "nope"}}`), &cfg)
	set, problems := Parse(cfg)

	if len(problems) != 1 {
		t.Fatalf("problems = %v, want exactly one", problems)
	}
	if got := decide(set, "bash", "git status"); got != Allow {
		t.Errorf("the good rule was lost: %v", got)
	}
	// The bad one fails closed.
	if got := decide(set, "bash", "rm -rf /"); got != Ask {
		t.Errorf("the bad rule decided %v, want Ask", got)
	}
}

// TestMalformedBlockIsReported covers a value that is neither a decision nor a
// table — a number, say, or a list.
func TestMalformedBlockIsReported(t *testing.T) {
	var cfg Config
	_ = json.Unmarshal([]byte(`{"bash": ["git"]}`), &cfg)
	if _, problems := Parse(cfg); len(problems) == 0 {
		t.Error("a malformed permissions block was accepted")
	}
}

// TestGrantsBeatFileRules: a grant was made this session, in front of the exact
// call, which makes it better informed than anything written last month.
func TestGrantsBeatFileRules(t *testing.T) {
	s := parse(t, `{"bash": {"npm *": "ask"}}`)
	s.Grant("bash", "npm test*", Allow)

	if got := decide(s, "bash", "npm test"); got != Allow {
		t.Errorf("granted call = %v, want Allow", got)
	}
	if got := decide(s, "bash", "npm publish"); got != Ask {
		t.Errorf("ungranted call = %v, want Ask", got)
	}
}

// TestDecideReportsTheRule so the UI can say why something was allowed. A
// decision nobody can explain is one nobody can trust.
func TestDecideReportsTheRule(t *testing.T) {
	s := parse(t, `{"bash": {"git push *": "deny"}}`)
	d, rule, ok := s.Decide("bash", "git push origin main")

	if !ok {
		t.Fatal("Decide reported no matching rule")
	}
	if d != Deny {
		t.Errorf("decision = %v, want Deny", d)
	}
	if rule.Pattern != "git push *" || rule.Tool != "bash" {
		t.Errorf("rule = %+v, want the bash git-push rule", rule)
	}
}

// TestTarget checks what a rule is matched against, for each tool shape.
func TestTarget(t *testing.T) {
	for _, tc := range []struct{ args, want string }{
		{`{"command":"git status"}`, "git status"},
		{`{"path":"main.go"}`, "main.go"},
		{`{"pattern":"*.go"}`, "*.go"},
		{`{"path":"a.go","content":"x"}`, "a.go"},
		{`{"unknown":"x"}`, ""},
		{`not json`, ""},
	} {
		if got := Target(json.RawMessage(tc.args)); got != tc.want {
			t.Errorf("Target(%s) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// TestEmptyPatternMatchesEveryTarget covers the short form's internals: no
// pattern means every call, including one whose target could not be read.
func TestEmptyPatternMatchesEveryTarget(t *testing.T) {
	s := parse(t, `{"bash": "allow"}`)
	if got := decide(s, "bash", ""); got != Allow {
		t.Errorf("empty target = %v, want Allow", got)
	}
}
