package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Ollama's OpenAI-compatible endpoint reports no context length, which is
// unfortunate because Ollama is exactly where it matters: it serves a 4096
// token window by default no matter what the model supports, and a window
// nobody declared is the single most common cause of bad answers.
//
// Its native API does report it, so this one place steps outside the shared
// wire format. Everything else in this package stays provider-agnostic.

// OllamaContext returns the window Ollama actually serves a model with, or
// zero when the endpoint is not Ollama or does not say.
//
// The served window is what matters, not the model's maximum. qwen3.5
// advertises 262144 and is served 4096 unless told otherwise; budgeting
// against the larger number would be worse than having no number at all.
func OllamaContext(ctx context.Context, baseURL, model string) int {
	// The native API sits alongside /v1 rather than under it.
	base := strings.TrimSuffix(strings.TrimSuffix(baseURL, "/"), "/v1")

	body, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/show", bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}

	var out struct {
		// Parameters is the Modelfile text, where num_ctx appears if it was set.
		Parameters string `json:"parameters"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return 0
	}

	// Only num_ctx is trustworthy: it is what the server was told to serve.
	//
	// model_info reports the architecture's maximum, which is a different
	// number entirely and a dangerous one to use. qwen3.5 reports 262144 there
	// and is served 4096 unless configured. Treating the maximum as the window
	// would make a ladder escalate *down* — leaving an 8192 model for one it
	// believed had 262144 and actually had 4096 — so an unstated window is
	// reported as unknown rather than guessed at.
	return numCtx(out.Parameters)
}

// numCtx reads "num_ctx <n>" out of the parameter block.
func numCtx(params string) int {
	for _, line := range strings.Split(params, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "num_ctx" {
			if n, err := strconv.Atoi(fields[1]); err == nil {
				return n
			}
		}
	}
	return 0
}
