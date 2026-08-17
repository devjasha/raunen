package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func isRateLimited(err error) bool { return errors.Is(err, ErrRateLimited) }

func modelServer(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// "Free" has to mean the chat turn costs nothing. Other prices on a model —
// images, web search — must not decide it, or paid models get treated as free.
func TestListModelsDetailedFindsFree(t *testing.T) {
	url := modelServer(t, `{"data":[
	  {"id":"free-one","context_length":1000000,
	   "pricing":{"prompt":"0","completion":"0","web_search":"0.014"}},
	  {"id":"paid","context_length":200000,
	   "pricing":{"prompt":"0.000000375","completion":"0.000001875"}},
	  {"id":"free-decimal","context_length":262144,
	   "pricing":{"prompt":"0.0000000","completion":"0.0"}},
	  {"id":"no-pricing","context_length":8192},
	  {"id":"numeric-free","context_length":4096,
	   "pricing":{"prompt":0,"completion":0}},
	  {"id":"tiered","context_length":4096,
	   "pricing":{"prompt":[{"tier":1,"price":"0"}],"completion":"0"}}
	]}`)

	got, err := ListModelsDetailed(context.Background(), url, "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		free bool
		ctx  int
	}{
		"free-one":     {true, 1000000},
		"paid":         {false, 200000},
		"free-decimal": {true, 262144},
		// No pricing stated is not the same as free: Ollama reports none, and
		// its models must not be advertised as a free tier.
		"no-pricing": {false, 8192},
		// A bare number is a price like any other.
		"numeric-free": {true, 4096},
		// Tiered pricing arrives as an array. It cannot be reduced to one
		// number, so it is not claimed as free — and, more importantly, one odd
		// entry must not fail the whole catalogue.
		"tiered": {false, 4096},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d models, want %d", len(got), len(want))
	}
	for _, m := range got {
		w, ok := want[m.ID]
		if !ok {
			t.Errorf("unexpected model %q", m.ID)
			continue
		}
		if m.Free != w.free {
			t.Errorf("%s free = %v, want %v", m.ID, m.Free, w.free)
		}
		if m.Context != w.ctx {
			t.Errorf("%s context = %d, want %d", m.ID, m.Context, w.ctx)
		}
	}
}

// A rate limit has to be distinguishable, because the response is to move to
// another model rather than to give up.
func TestRateLimitIsDistinguishable(t *testing.T) {
	// 402 is handled separately: see TestCreditsAndRateLimitsAreDistinct.
	for _, code := range []int{http.StatusTooManyRequests} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":{"message":"quota"}}`))
		}))
		c := New(srv.URL, "", "m")
		_, _, err := c.Stream(context.Background(), nil, nil, Handler{})
		if err == nil {
			t.Fatalf("status %d returned no error", code)
		}
		if !isRateLimited(err) {
			t.Errorf("status %d not reported as a rate limit: %v", code, err)
		}
		srv.Close()
	}

	// An ordinary failure must not be mistaken for one, or a real problem
	// turns into a silent walk down the ladder.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := New(srv.URL, "", "m")
	if _, _, err := c.Stream(context.Background(), nil, nil, Handler{}); isRateLimited(err) {
		t.Error("a 500 was reported as a rate limit")
	}
}

// A catalogue lists plenty that cannot hold a conversation, and they must not
// reach a chooser or a fallback ladder.
func TestIsChatModel(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		outputs []string
		want    bool
	}{
		{"plain text model", "meta/llama-3", []string{"text"}, true},
		{"nothing stated is assumed usable", "qwen3:8b", nil, true},
		// Reachable only through a separate batch endpoint; answers 404 here.
		{"batch variant", "minimax/minimax-m3:batch", []string{"text"}, false},
		// Declares text as well as audio, so a looser test would let it
		// through — and one of these did reach the ladder.
		{"music model", "google/lyria-3-pro-preview", []string{"text", "audio"}, false},
		{"image generator", "black-forest/flux", []string{"image"}, false},
		{"multimodal output", "some/model", []string{"text", "image"}, false},
	}
	for _, c := range cases {
		if got := isChatModel(c.id, c.outputs); got != c.want {
			t.Errorf("%s: isChatModel(%q, %v) = %v, want %v", c.name, c.id, c.outputs, got, c.want)
		}
	}
}

// A refusal for want of money is not a rate limit, however similar it looks
// from the outside: waiting clears one and never clears the other.
func TestCreditsAndRateLimitsAreDistinct(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusPaymentRequired, ErrNeedsCredits},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"error":{"message":"needs more credits"}}`))
		}))
		_, _, err := New(srv.URL, "", "m").Stream(context.Background(), nil, nil, Handler{})
		if !errors.Is(err, c.want) {
			t.Errorf("status %d = %v, want %v", c.status, err, c.want)
		}
		// And not the other one, or the wrong remedy gets applied.
		other := ErrRateLimited
		if c.want == ErrRateLimited {
			other = ErrNeedsCredits
		}
		if errors.Is(err, other) {
			t.Errorf("status %d also matched %v", c.status, other)
		}
		srv.Close()
	}
}
