package tools

import (
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "colour codes go",
			in:   "\x1b[31merror\x1b[0m: bad thing",
			want: "error: bad thing",
		},
		{
			name: "progress keeps only the final state",
			in:   "downloading 10%\rdownloading 50%\rdownloading 100%",
			want: "downloading 100%",
		},
		{
			name: "runs of blank lines collapse to one",
			in:   "a\n\n\n\n\nb",
			want: "a\n\nb",
		},
		{
			name: "repeated lines are counted, not repeated",
			in:   "warn: x\nwarn: x\nwarn: x\ndone",
			want: "warn: x  (×3)\ndone",
		},
		{
			name: "trailing whitespace goes",
			in:   "text   \nmore\t",
			want: "text\nmore",
		},
		{
			name: "surrounding blank lines go",
			in:   "\n\nreal output\n\n\n",
			want: "real output",
		},
		{
			name: "ordinary output is untouched",
			in:   "line one\nline two\n\nline three",
			want: "line one\nline two\n\nline three",
		},
		{
			name: "empty stays empty",
			in:   "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Clean(tt.in); got != tt.want {
				t.Errorf("Clean(%q) =\n %q\nwant\n %q", tt.in, got, tt.want)
			}
		})
	}
}

// Cleaning must never alter the substance of a result. A model reasoning about
// paraphrased evidence is worse than one reasoning about less of it.
func TestCleanPreservesContent(t *testing.T) {
	in := strings.Join([]string{
		"FAIL: TestThing (0.12s)",
		"    want 42, got 41",
		"exit status 1",
	}, "\n")
	if got := Clean(in); got != in {
		t.Errorf("Clean altered meaningful output:\n got %q\nwant %q", got, in)
	}

	// Indentation is meaningful in diffs, stack traces and code.
	code := "func main() {\n\tif x {\n\t\treturn\n\t}\n}"
	if got := Clean(code); got != code {
		t.Errorf("Clean altered indented code:\n got %q\nwant %q", got, code)
	}
}
