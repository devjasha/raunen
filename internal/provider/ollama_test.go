package provider

import "testing"

// The served window and the model's maximum are different numbers, and only
// one of them is safe to budget against.
func TestNumCtx(t *testing.T) {
	params := "num_ctx                        8192\npresence_penalty               1.5\ntemperature                    1"
	if got := numCtx(params); got != 8192 {
		t.Errorf("numCtx = %d, want 8192", got)
	}
	// Not stated means unknown. Reporting the architecture maximum here would
	// let a ladder escalate to a model with a smaller real window.
	if got := numCtx("temperature 1\ntop_k 40"); got != 0 {
		t.Errorf("numCtx with no num_ctx = %d, want 0", got)
	}
	if got := numCtx(""); got != 0 {
		t.Errorf("numCtx of empty = %d, want 0", got)
	}
	if got := numCtx("num_ctx notanumber"); got != 0 {
		t.Errorf("numCtx of garbage = %d, want 0", got)
	}
}
