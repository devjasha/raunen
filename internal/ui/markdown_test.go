package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// plain strips styling so tests assert on structure, not colour.
func plain(s string) string { return ansi.Strip(s) }

func TestMarkdownLine(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "heading loses its hashes",
			in:   []string{"## Setup"},
			want: []string{"Setup"},
		},
		{
			name: "bullets become dots",
			in:   []string{"- one", "* two", "+ three"},
			want: []string{"• one", "• two", "• three"},
		},
		{
			name: "numbered lists keep their numbers",
			in:   []string{"1. first"},
			want: []string{"1. first"},
		},
		{
			name: "fenced block gets a gutter",
			in:   []string{"```bash", "ls -la", "```"},
			want: []string{"│ bash", "│ ls -la", "│"},
		},
		{
			name: "indented fence still opens the block",
			in:   []string{"  ```", "code", "```"},
			want: []string{"│", "│ code", "│"},
		},
		{
			name: "inline markers are removed",
			in:   []string{"run `npm install` and **stop**"},
			want: []string{"run npm install and stop"},
		},
		{
			name: "blockquote",
			in:   []string{"> quoted"},
			want: []string{"│ quoted"},
		},
		{
			name: "rule",
			in:   []string{"---"},
			want: []string{strings.Repeat("─", 24)},
		},
		{
			name: "plain text untouched",
			in:   []string{"just a sentence"},
			want: []string{"just a sentence"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var md markdown
			for i, in := range tt.in {
				got := plain(md.line(in))
				if got != tt.want[i] {
					t.Errorf("line %d: md.line(%q) = %q, want %q", i, in, got, tt.want[i])
				}
			}
		})
	}
}

// Code inside a fence must survive verbatim: it is not prose, and styling its
// asterisks or backticks would corrupt what the model wrote.
func TestMarkdownCodeBlockIsVerbatim(t *testing.T) {
	var md markdown
	md.line("```go")
	got := plain(md.line("x := a * b // `note`"))
	if want := "│ x := a * b // `note`"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
