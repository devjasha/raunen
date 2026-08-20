// Package permission decides what the agent may do without asking.
//
// Modes are a blunt instrument: auto runs everything, accept asks about every
// change, plan refuses them. What is missing is the middle — "you may edit
// anything under docs/, but never push" — and without it accept mode turns into
// a key-mashing exercise where the twentieth identical prompt is approved
// without being read. That is worse than no prompt at all, because it looks
// like oversight.
//
// A rule names a tool and a pattern for what it acts on:
//
//	"permissions": {
//	  "bash": { "git *": "allow", "git push *": "deny" },
//	  "edit": { "docs/*": "allow" },
//	  "write": "ask"
//	}
//
// Rules refine modes rather than replacing them. Plan mode still refuses every
// change, whatever a rule says: a mode is a decision about this session, and a
// rule written last week should not quietly undo it.
package permission

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Decision is what a rule says about a call.
type Decision int

const (
	// Ask prompts the user. It is the outcome when nothing matched, so a tool
	// nobody has written a rule for behaves exactly as it did before.
	Ask Decision = iota
	// Allow runs without prompting.
	Allow
	// Deny refuses, in every mode. A denial is the one thing that has to hold
	// even in auto: "never push" is not advice.
	Deny
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Deny:
		return "deny"
	default:
		return "ask"
	}
}

// ParseDecision reads a decision from config, reporting an unrecognised word
// rather than guessing. Guessing here would mean silently downgrading a "deny"
// somebody typed as "denied".
func ParseDecision(s string) (Decision, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return Allow, true
	case "deny":
		return Deny, true
	case "ask":
		return Ask, true
	}
	return Ask, false
}

// Rule is one pattern and what it decides.
type Rule struct {
	// Tool is the tool the rule applies to.
	Tool string
	// Pattern matches the target — the command for bash, the path for a file
	// tool. Empty means "any call to this tool".
	Pattern string
	// Decision is what to do when it matches.
	Decision Decision
	// literal is how many characters of the pattern are not wildcards, which is
	// how specificity is measured.
	literal int
	re      *regexp.Regexp
}

// Display names a rule the way it is written in the config, so a refusal can
// quote the thing the user would edit to change it.
func (r Rule) Display() string {
	if r.Pattern == "" {
		return r.Tool
	}
	return r.Tool + " " + r.Pattern
}

// Set is the rules in force, plus the grants made during this session.
type Set struct {
	rules []Rule
	// grants are "don't ask again" answers. They are deliberately not written
	// to disk: agreeing to something once, while looking at it, is a different
	// act from writing a rule that will apply next month in a repository you
	// have not thought about yet.
	grants []Rule
}

// Rules exposes the configured rules for display, in the order they are
// matched: most specific first.
func (s *Set) Rules() []Rule { return s.rules }

// Grants exposes the session grants, most specific first.
func (s *Set) Grants() []Rule { return s.grants }

// Config is the permissions block as it appears in the config file.
//
// A tool maps either to one decision for every call — "write": "ask" — or to
// patterns with their own decisions. Both spellings are common enough to be
// worth accepting; requiring the long form for the simple case would make the
// simple case look complicated.
type Config map[string]json.RawMessage

// Parse builds a matcher from the config block, returning the problems it found
// alongside a usable set.
//
// Errors are returned rather than fatal, and the rules that did parse are kept.
// A typo in one pattern should not take the other nineteen with it, and it must
// never fail open — an unparseable rule is dropped, so what remains is a subset
// of what was asked for rather than a superset.
func Parse(cfg Config) (*Set, []string) {
	set := &Set{}
	var problems []string

	for tool, raw := range cfg {
		// The short form: one decision for every call to this tool.
		var word string
		if err := json.Unmarshal(raw, &word); err == nil {
			d, ok := ParseDecision(word)
			if !ok {
				problems = append(problems, "permissions."+tool+": unknown decision "+word)
				continue
			}
			set.add(tool, "", d)
			continue
		}

		var patterns map[string]string
		if err := json.Unmarshal(raw, &patterns); err != nil {
			problems = append(problems,
				"permissions."+tool+": expected a decision or a table of patterns")
			continue
		}
		for pattern, word := range patterns {
			d, ok := ParseDecision(word)
			if !ok {
				problems = append(problems,
					"permissions."+tool+"."+pattern+": unknown decision "+word)
				continue
			}
			set.add(tool, pattern, d)
		}
	}

	set.sort()
	return set, problems
}

// add compiles a rule into the set.
func (s *Set) add(tool, pattern string, d Decision) {
	s.rules = append(s.rules, compile(tool, pattern, d))
}

// Grant records a "don't ask again" answer for the rest of the session.
func (s *Set) Grant(tool, pattern string, d Decision) {
	s.grants = append(s.grants, compile(tool, pattern, d))
	sortRules(s.grants)
}

// compile turns a pattern into a matcher and measures how specific it is.
func compile(tool, pattern string, d Decision) Rule {
	r := Rule{Tool: tool, Pattern: pattern, Decision: d}
	for _, c := range pattern {
		if c != '*' {
			r.literal++
		}
	}
	if pattern != "" && pattern != "*" {
		r.re = patternRegexp(pattern)
	}
	return r
}

// patternRegexp translates a permission pattern into a regexp.
//
// A single * spans everything, separators included — unlike the glob tool,
// where it stops at a slash. The two are different questions: a glob is looking
// for files and wants "*.go" to mean this directory, while a rule about docs/
// means the whole of docs/, and making someone write docs/** for that would be
// a trap that fails towards granting more than intended.
func patternRegexp(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for _, part := range strings.Split(pattern, "*") {
		b.WriteString(regexp.QuoteMeta(part))
		b.WriteString(".*")
	}
	// The split leaves one trailing .* too many; trim it and close.
	s := strings.TrimSuffix(b.String(), ".*") + "$"
	re, err := regexp.Compile(s)
	if err != nil {
		// QuoteMeta makes this unreachable, but a nil here would match
		// everything, so fall back to something that matches nothing.
		return regexp.MustCompile(`$^`)
	}
	return re
}

// sort orders the rules so matching can stop at the first hit.
func (s *Set) sort() { sortRules(s.rules) }

// sortRules puts the most specific rule first, and a denial ahead of an
// allowance of equal specificity.
//
// Specificity rather than file order, because the file is a JSON object and Go
// ranges maps in a random order — "the last matching rule wins" would mean a
// different answer on different runs, which is an unacceptable property for the
// thing deciding whether a command may run.
//
// Denial winning ties is the safe direction: if two equally specific rules
// disagree, the one that refuses is the one to honour.
func sortRules(rules []Rule) {
	sort.SliceStable(rules, func(i, j int) bool {
		// Naming a tool is a more specific statement than "*", and it is a
		// stronger one than any pattern: "read": "allow" beside "*": "deny"
		// plainly means reading is the exception, and ordering on the pattern
		// first would have the wildcard swallow it.
		if a, b := rules[i].Tool != "*", rules[j].Tool != "*"; a != b {
			return a
		}
		if rules[i].literal != rules[j].literal {
			return rules[i].literal > rules[j].literal
		}
		if rules[i].Decision != rules[j].Decision {
			return rules[i].Decision == Deny
		}
		return rules[i].Pattern < rules[j].Pattern
	})
}

// Decide reports what the rules say about running tool against target, and
// which rule said so.
//
// Grants are consulted first: they were made during this session, in front of
// the exact call, which makes them newer and better informed than anything in
// the file.
func (s *Set) Decide(tool, target string) (Decision, Rule, bool) {
	if s == nil {
		return Ask, Rule{}, false
	}
	for _, list := range [][]Rule{s.grants, s.rules} {
		for _, r := range list {
			if r.matches(tool, target) {
				return r.Decision, r, true
			}
		}
	}
	return Ask, Rule{}, false
}

// matches reports whether a rule covers this call.
func (r Rule) matches(tool, target string) bool {
	if r.Tool != tool && r.Tool != "*" {
		return false
	}
	// No pattern, or a bare star: every call to the tool.
	if r.re == nil {
		return true
	}
	return r.re.MatchString(target)
}

// Target is the part of a tool call a rule matches against: the command for
// bash, the path for a file tool, the pattern for a search.
//
// The keys are tried in the order a rule is most likely to be written about,
// which is the same order the UI summarises a call in — so what a rule matches
// is what the approval prompt showed.
func Target(args json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	for _, k := range []string{"command", "path", "pattern"} {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}
