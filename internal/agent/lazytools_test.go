package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"raunen/internal/provider"
	"raunen/internal/tools"
)

// selector stands in for a model that discovers a tool and then uses it: it
// calls a selecting tool on the first step, the tool that appeared on the
// second, and stops. It records the tool names each request advertised, which is
// what the assertions are actually about.
func selector(t *testing.T, selectName, thenCall string) (*httptest.Server, func() [][]string) {
	t.Helper()

	var mu sync.Mutex
	var advertised [][]string
	var step int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Record the tools this request carried, so the test can prove the
		// selected one actually reached the model rather than only the registry.
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var names []string
		for _, tl := range body.Tools {
			names = append(names, tl.Function.Name)
		}

		mu.Lock()
		advertised = append(advertised, names)
		step++
		n := step
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")

		call := func(name, args string) []byte {
			b, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{
					"delta": map[string]any{"tool_calls": []map[string]any{{
						"index":    0,
						"id":       fmt.Sprintf("c%d", n),
						"type":     "function",
						"function": map[string]any{"name": name, "arguments": args},
					}}},
					"finish_reason": "tool_calls",
				}},
			})
			return b
		}

		var frame []byte
		switch n {
		case 1:
			frame = call("mcp_select_tool", fmt.Sprintf(`{"name":%q}`, selectName))
		case 2:
			frame = call(thenCall, `{}`)
		default:
			frame, _ = json.Marshal(map[string]any{
				"choices": []map[string]any{{
					"delta":         map[string]any{"content": "done"},
					"finish_reason": "stop",
				}},
			})
		}
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
	}))
	t.Cleanup(srv.Close)

	return srv, func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		out := make([][]string, len(advertised))
		copy(out, advertised)
		return out
	}
}

// has reports whether a request advertised a tool by name.
func has(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// The point of lazy loading: a tool selected partway through a turn has to be
// advertised on the very next request, and be callable.
//
// This is the seam where the whole feature can silently fail. The registry is
// read once per step, so if the schema list were captured at the top of the turn
// instead the model would be told the tool was ready while the request still did
// not carry it — and it would call a tool the endpoint says does not exist.
func TestSelectedToolReachesTheNextRequest(t *testing.T) {
	srv, requests := selector(t, "srv_hidden", "srv_hidden")

	reg := tools.Default(t.TempDir(), 4096)

	var ran bool
	hidden := tools.Tool{
		Name:        "srv_hidden",
		Description: "a tool held back until selected",
		Params:      map[string]any{"type": "object", "properties": map[string]any{}},
		Run: func(_ context.Context, _ json.RawMessage) (string, error) {
			ran = true
			return "hidden ran", nil
		},
	}
	// Stand in for the catalogue's select tool: adding to the registry is
	// exactly what selecting does.
	reg.Add(tools.Tool{
		Name:        "mcp_select_tool",
		Description: "load a tool",
		Params: map[string]any{"type": "object", "properties": map[string]any{
			"name": map[string]any{"type": "string"},
		}},
		Run: func(_ context.Context, _ json.RawMessage) (string, error) {
			reg.Add(hidden)
			return "loaded srv_hidden", nil
		},
	})

	a := New(provider.New(srv.URL+"/v1", "", "m"), reg, "")
	a.SetMode(ModeAuto)

	if _, _, failed := collect(t, a, "use the hidden tool"); failed != nil {
		t.Fatalf("turn failed: %v", failed)
	}

	reqs := requests()
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(reqs))
	}
	if has(reqs[0], "srv_hidden") {
		t.Error("the held-back tool was advertised before it was selected — nothing was saved")
	}
	if !has(reqs[1], "srv_hidden") {
		t.Errorf("after selecting, the tool was not advertised on the next request: %v", reqs[1])
	}
	if !ran {
		t.Error("the selected tool was never actually called")
	}
}

// The schemas a request carries must shrink to the meta-tools when a catalogue
// is in use. This is the saving the feature exists for, so it is worth asserting
// directly rather than inferring it.
func TestHeldBackToolsAreNotAdvertised(t *testing.T) {
	srv, requests := selector(t, "srv_hidden", "list")

	reg := tools.Default(t.TempDir(), 4096)
	reg.Add(tools.Tool{
		Name:        "mcp_select_tool",
		Description: "load a tool",
		Params:      map[string]any{"type": "object", "properties": map[string]any{}},
		Run: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "nothing selected", nil
		},
	})

	a := New(provider.New(srv.URL+"/v1", "", "m"), reg, "")
	a.SetMode(ModeAuto)
	if _, _, failed := collect(t, a, "hello"); failed != nil {
		t.Fatalf("turn failed: %v", failed)
	}

	for i, names := range requests() {
		for _, n := range names {
			if strings.HasPrefix(n, "srv_") {
				t.Errorf("request %d advertised a held-back tool %q", i, n)
			}
		}
	}
}
