package provider

import (
	"strings"
	"testing"
)

// feedAll streams s through the stripper one rune at a time, which is the
// worst case: every tag arrives split across many deltas.
func feedAll(chunks []string) string {
	var s stripper
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(s.Feed(c))
	}
	out.WriteString(s.Flush())
	return out.String()
}

func runes(s string) []string {
	var out []string
	for _, r := range s {
		out = append(out, string(r))
	}
	return out
}

func TestStripper(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text untouched", "The file has 2 lines.", "The file has 2 lines."},
		{"trailing tool markup", "Done.\n</parameter>\n</function>\n</tool_call>", "Done.\n\n\n"},
		{"open and close", "<tool_call>ignored-name</tool_call>", "ignored-name"},
		{"pipe wrapped", "hi<|tool_call|>there", "hithere"},
		{"function equals form", "a<function=read>b", "ab"},
		{"think tags", "<think>hmm</think>answer", "hmmanswer"},
		{"html preserved", "Use <div> and </div> here.", "Use <div> and </div> here."},
		{"generic tag preserved", "<span class=\"x\">y</span>", "<span class=\"x\">y</span>"},
		{"bare less-than kept", "if a < b then", "if a < b then"},
		{"comparison chain kept", "a < b < c", "a < b < c"},
		{"unterminated tag kept", "trailing <notatag", "trailing <notatag"},
		{"long non-tag kept", "x <" + strings.Repeat("y", 40), "x <" + strings.Repeat("y", 40)},
		{"case insensitive", "a</TOOL_CALL>b", "ab"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Whole string at once.
			if got := feedAll([]string{tt.in}); got != tt.want {
				t.Errorf("whole:\n got %q\nwant %q", got, tt.want)
			}
			// Rune by rune, to catch tags split across deltas.
			if got := feedAll(runes(tt.in)); got != tt.want {
				t.Errorf("split:\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestIsMarkup(t *testing.T) {
	markup := []string{"<tool_call>", "</tool_call>", "<|tool_call|>", "<function=read>", "</parameter>", "<think>"}
	for _, s := range markup {
		if !isMarkup(s) {
			t.Errorf("isMarkup(%q) = false, want true", s)
		}
	}
	content := []string{"<div>", "</p>", "<span class=\"a\">", "<>", "<br/>"}
	for _, s := range content {
		if isMarkup(s) {
			t.Errorf("isMarkup(%q) = true, want false", s)
		}
	}
}
