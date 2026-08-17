package ui

import (
	"testing"

	"raunen/internal/config"
)

func withItems(items ...string) *picker {
	p := &picker{}
	p.setModels(items)
	return p
}

// Pinned models belong at the top of the chooser, where they are actually
// useful, and everything else keeps its order below them.
func TestPickerFloatsFavouritesToTop(t *testing.T) {
	c := &config.Config{Favourites: []string{"groq/llama-3.3-70b-versatile"}}
	p := &picker{cfg: c}
	p.setModels([]string{
		"ollama/qwen3:8b",
		"openrouter/google/gemma-4-31b-it:free",
		"groq/llama-3.3-70b-versatile",
	})
	want := []string{
		"groq/llama-3.3-70b-versatile",
		"ollama/qwen3:8b",
		"openrouter/google/gemma-4-31b-it:free",
	}
	if len(p.all) != len(want) {
		t.Fatalf("all = %v, want %v", p.all, want)
	}
	for i := range want {
		if p.all[i] != want[i] {
			t.Errorf("position %d = %q, want %q", i, p.all[i], want[i])
		}
	}
}

// A pin that is not in the list leaves the rest untouched.
func TestPickerFavouriteNotPresentIsIgnored(t *testing.T) {
	c := &config.Config{Favourites: []string{"openrouter/absent"}}
	p := &picker{cfg: c}
	p.setModels([]string{"ollama/qwen3:8b", "groq/llama-3.3-70b-versatile"})
	if len(p.all) != 2 {
		t.Fatalf("all = %v, want the two models untouched", p.all)
	}
}

// Model names are long and structured, so matching has to be more forgiving
// than a substring test — otherwise finding one means typing most of it.
func TestPickerSearch(t *testing.T) {
	p := withItems(
		"ollama/qwen3.5-8k:latest",
		"ollama/qwen3:8b",
		"openrouter/nvidia/nemotron-3.5-lightning:free",
		"openrouter/nvidia/nemotron-3-ultra-550b-a55b:free",
		"openrouter/google/gemma-4-31b-it:free",
		"groq/llama-3.3-70b-versatile",
	)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "empty shows everything",
			query: "",
			want:  p.all,
		},
		{
			name:  "plain substring",
			query: "groq",
			want:  []string{"groq/llama-3.3-70b-versatile"},
		},
		{
			// Every term has to match, which is what makes two words useful.
			name:  "terms are anded",
			query: "nvidia lightning",
			want:  []string{"openrouter/nvidia/nemotron-3.5-lightning:free"},
		},
		{
			// An abbreviation should find a long name.
			name:  "subsequence",
			query: "nemlight",
			want:  []string{"openrouter/nvidia/nemotron-3.5-lightning:free"},
		},
		{
			name:  "no match",
			query: "mistral",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p.filter = tt.query
			p.apply()
			if len(p.filtered) != len(tt.want) {
				t.Fatalf("query %q matched %d (%v), want %d (%v)",
					tt.query, len(p.filtered), p.filtered, len(tt.want), tt.want)
			}
			for i := range tt.want {
				if p.filtered[i] != tt.want[i] {
					t.Errorf("query %q result %d = %q, want %q", tt.query, i, p.filtered[i], tt.want[i])
				}
			}
		})
	}
}

// A substring match is what someone typing a name expects to see first; a
// subsequence match is the helpful surprise and belongs below it.
func TestPickerRanksSubstringsFirst(t *testing.T) {
	p := withItems("aaa-free-bbb", "f-r-e-e-model")
	p.filter = "free"
	p.apply()

	if len(p.filtered) != 2 {
		t.Fatalf("matched %v, want both", p.filtered)
	}
	if p.filtered[0] != "aaa-free-bbb" {
		t.Errorf("first result = %q, want the substring match", p.filtered[0])
	}
}

// Narrowing the list must not leave the cursor pointing past the end of it.
func TestPickerCursorStaysInRange(t *testing.T) {
	p := withItems("one", "two", "three")
	p.cursor = 2

	p.filter = "one"
	p.apply()
	if p.cursor != 0 {
		t.Errorf("cursor = %d after narrowing to one result, want 0", p.cursor)
	}
	if got := p.selected(); got != "one" {
		t.Errorf("selected = %q, want one", got)
	}

	// And an empty result set must not be selectable.
	p.filter = "nothing"
	p.apply()
	if got := p.selected(); got != "" {
		t.Errorf("selected = %q with no matches, want empty", got)
	}
}
