// Package provider speaks the OpenAI /v1/chat/completions wire format, which
// Ollama, LM Studio, llama.cpp, vLLM, OpenRouter and most gateways all serve.
// One client covers every one of them.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Role string

const (
	System    Role = "system"
	User      Role = "user"
	Assistant Role = "assistant"
	ToolRole  Role = "tool"
)

// Function holds a tool call's name and its JSON-encoded arguments. Arguments
// stays a string because that is how the API transports it — the model streams
// it in fragments that only parse once complete.
type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type Message struct {
	Role Role `json:"role"`
	// Content is always serialized, even when empty. An assistant message that
	// carries only tool calls has no text, and omitting the key entirely makes
	// Ollama reject the next request with "invalid message content type: nil".
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID ties a tool result back to the call that requested it.
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
	// Finish is the server's finish_reason for this message. It is never sent
	// back; "length" means the model was cut off before it finished, which is
	// otherwise indistinguishable from it choosing to stop.
	Finish string `json:"-"`
}

// ToolSchema is a tool as advertised to the model.
type ToolSchema struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

func New(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		// No global timeout: a local model may think for minutes. Cancellation
		// is the caller's job, via ctx.
		HTTP: &http.Client{},
	}
}

type request struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	Tools         []ToolSchema   `json:"tools,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	// IncludeUsage asks the server for a final frame carrying token counts.
	// Servers that do not support it ignore it, and usage stays zero.
	IncludeUsage bool `json:"include_usage"`
}

// Usage reports token counts for one request. Prompt is what the whole
// conversation cost to send, so it doubles as the current context size.
type Usage struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
	Total      int `json:"total_tokens"`
}

// streamChunk is one SSE frame from the server.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				// Index is the position of the call being appended to. A single
				// call's arguments arrive as fragments across many frames, all
				// carrying the same index.
				Index    int      `json:"index"`
				ID       string   `json:"id"`
				Type     string   `json:"type"`
				Function Function `json:"function"`
			} `json:"tool_calls"`
			// Reasoning models emit thinking on a side channel.
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Handler receives streamed output as it arrives. Either field may be nil.
type Handler struct {
	// Text receives assistant content deltas.
	Text func(string)
	// Reasoning receives thinking deltas from reasoning models. These are not
	// part of the message and are never sent back to the model.
	Reasoning func(string)
}

// Stream sends a completion request, invoking h for each delta as it arrives.
// It returns the fully assembled assistant message, including any tool calls.
func (c *Client) Stream(ctx context.Context, msgs []Message, tools []ToolSchema, h Handler) (Message, Usage, error) {
	var usage Usage

	body, err := json.Marshal(request{
		Model:         c.Model,
		Messages:      msgs,
		Tools:         tools,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	})
	if err != nil {
		return Message{}, usage, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, usage, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// The request never landed, so this is the endpoint rather than the
		// model — unless the caller cancelled, which is not a failure at all.
		if ctx.Err() != nil {
			return Message{}, usage, err
		}
		return Message{}, usage, fmt.Errorf("%w: %s: %v", ErrUnavailable, c.BaseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		msg := strings.TrimSpace(string(b))
		switch {
		case resp.StatusCode == http.StatusTooManyRequests,
			resp.StatusCode == http.StatusPaymentRequired:
			// Free tiers refuse like this when a quota runs out. Another model
			// may well answer, so this is marked as worth retrying elsewhere.
			return Message{}, usage, fmt.Errorf("%w: %s returned %s: %s",
				ErrRateLimited, c.BaseURL, resp.Status, msg)

		case resp.StatusCode == http.StatusNotFound,
			resp.StatusCode == http.StatusBadRequest,
			resp.StatusCode == http.StatusUnprocessableEntity:
			// The model was rejected rather than the request throttled: a name
			// that does not exist, or a request it cannot serve. Waiting will
			// not help.
			return Message{}, usage, fmt.Errorf("%w: %s returned %s: %s",
				ErrModelInvalid, c.BaseURL, resp.Status, msg)

		case resp.StatusCode >= 500:
			return Message{}, usage, fmt.Errorf("%w: %s returned %s: %s",
				ErrUnavailable, c.BaseURL, resp.Status, msg)
		}
		return Message{}, usage, fmt.Errorf("%s returned %s: %s", c.BaseURL, resp.Status, msg)
	}

	out := Message{Role: Assistant}
	// Filters tool-call scaffolding that models leak into content.
	var strip stripper
	// Accumulate tool calls by their stream index rather than appending, since
	// fragments for several calls can interleave.
	partial := map[int]*ToolCall{}
	var order []int
	var text strings.Builder

	// A stream that stops early has to be distinguished from one that finished.
	// bufio.Scanner ends cleanly at EOF either way, so without this a dropped
	// connection is indistinguishable from the model choosing to say nothing.
	var sawEnd bool

	sc := bufio.NewScanner(resp.Body)
	// Tool-call arguments can be large; the default 64KB token limit is not enough.
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			sawEnd = true
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Some servers interleave keep-alive comments; skip what we can't parse.
			continue
		}
		if chunk.Error != nil {
			return out, usage, fmt.Errorf("provider error: %s", chunk.Error.Message)
		}
		// The usage frame carries no choices, so read it before skipping.
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		if r := chunk.Choices[0].FinishReason; r != "" {
			sawEnd = true
			out.Finish = r
		}
		d := chunk.Choices[0].Delta

		if d.Content != "" {
			if clean := strip.Feed(d.Content); clean != "" {
				text.WriteString(clean)
				if h.Text != nil {
					h.Text(clean)
				}
			}
		}

		// Reasoning models (qwen3, deepseek-r1, gpt-oss) emit thinking on a
		// side channel and leave Content empty until it finishes. Surface it so
		// the user isn't watching dead air, but keep it out of the transcript.
		if r := d.Reasoning + d.ReasoningContent; r != "" && h.Reasoning != nil {
			h.Reasoning(r)
		}

		for _, tc := range d.ToolCalls {
			cur, ok := partial[tc.Index]
			if !ok {
				cur = &ToolCall{Type: "function"}
				partial[tc.Index] = cur
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Type != "" {
				cur.Type = tc.Type
			}
			if tc.Function.Name != "" {
				cur.Function.Name = tc.Function.Name
			}
			// Arguments are a fragment stream; concatenate in arrival order.
			cur.Function.Arguments += tc.Function.Arguments
		}
	}
	if err := sc.Err(); err != nil {
		return out, usage, err
	}
	if !sawEnd && ctx.Err() == nil {
		// Report it rather than returning a half-built message: a truncated
		// stream otherwise surfaces as the model having replied with nothing,
		// which sends you looking at the model instead of the connection.
		return out, usage, fmt.Errorf(
			"%w: %s closed the connection mid-response (it may have run out of memory or restarted)",
			ErrUnavailable, c.BaseURL)
	}

	if rest := strip.Flush(); rest != "" {
		text.WriteString(rest)
		if h.Text != nil {
			h.Text(rest)
		}
	}

	out.Content = strings.TrimSpace(text.String())
	for _, i := range order {
		tc := partial[i]
		if tc.ID == "" {
			// Some local servers omit IDs; the loop needs one to correlate results.
			tc.ID = fmt.Sprintf("call_%d_%d", i, time.Now().UnixNano())
		}
		out.ToolCalls = append(out.ToolCalls, *tc)
	}
	return out, usage, nil
}

// ModelInfo is what an endpoint reports about a model. Context and Free are
// only as good as the endpoint: Ollama reports neither, OpenRouter reports
// both, and a zero Context means "not stated" rather than "no context".
type ModelInfo struct {
	ID      string
	Context int
	Free    bool
	// Chat reports whether the model can answer a chat completion at all.
	// Catalogues list plenty that cannot: image and music generators, and on
	// OpenRouter 61 ":batch" variants that exist only behind a separate batch
	// endpoint and answer 404 here. Offering those is offering a dead end.
	Chat bool
}

// Failures are classified because the right response differs. A rate limit
// clears on its own and is worth retrying later; a bad model name never will be;
// a server that is down affects every model behind it, not just this one.
var (
	// ErrRateLimited is a quota or throughput refusal. Wait, then retry.
	ErrRateLimited = errors.New("rate limited")
	// ErrModelInvalid means this model will not work — wrong name, unsupported
	// request. Retrying it is pointless however long you wait.
	ErrModelInvalid = errors.New("model unusable")
	// ErrUnavailable is the endpoint failing rather than the model: a refused
	// connection, a timeout, a 5xx. Everything behind it is suspect.
	ErrUnavailable = errors.New("endpoint unavailable")
)

// ListModelsDetailed asks an endpoint what it serves, keeping the pricing and
// context it reports. OpenRouter returns both; endpoints that do not simply
// leave them zero.
func ListModelsDetailed(ctx context.Context, baseURL, apiKey string) ([]ModelInfo, error) {
	body, err := fetchModels(ctx, baseURL, apiKey)
	if err != nil {
		return nil, err
	}
	out := make([]ModelInfo, 0, len(body.Data))
	for _, m := range body.Data {
		info := ModelInfo{ID: m.ID, Context: m.ContextLength, Chat: isChatModel(m.ID, m.Architecture.OutputModalities)}
		// Free means it costs nothing to send or receive. Other prices —
		// images, web search — do not decide whether a chat turn is free.
		prompt, okP := price(m.Pricing["prompt"])
		completion, okC := price(m.Pricing["completion"])
		info.Free = okP && okC && prompt == 0 && completion == 0
		out = append(out, info)
	}
	return out, nil
}

// isChatModel reports whether a model can serve a chat completion.
//
// Nothing in a catalogue states this reliably, so it is decided from what is
// stated: a model that does not output text cannot answer, and OpenRouter's
// ":batch" suffix marks a variant reachable only through its batch endpoint.
// An endpoint that says nothing about modalities — every local runtime — is
// taken at its word and assumed usable.
func isChatModel(id string, outputs []string) bool {
	if strings.HasSuffix(id, ":batch") {
		return false
	}
	if len(outputs) == 0 {
		return true
	}
	// Every output must be text. Merely including text is not enough: a music
	// model declares "text+audio" and would pass that test while being useless
	// for a conversation — one of them ended up on the fallback ladder.
	for _, o := range outputs {
		if o != "text" {
			return false
		}
	}
	return true
}

// price reads a per-token price, which arrives inconsistently: usually a
// decimal string such as "0.000000375", sometimes a bare number, and for some
// models an array of tiered prices. Anything not reducible to a single number
// reports false, so an unreadable price is never mistaken for a free one.
func price(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return 0, false
		}
		v, err := strconv.ParseFloat(s, 64)
		return v, err == nil
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f, true
	}
	return 0, false
}

type modelsBody struct {
	Data []struct {
		ID            string `json:"id"`
		ContextLength int    `json:"context_length"`
		// Held raw: the shape varies per model, and a single odd entry must not
		// fail the whole catalogue.
		Pricing      map[string]json.RawMessage `json:"pricing"`
		Architecture struct {
			OutputModalities []string `json:"output_modalities"`
		} `json:"architecture"`
	} `json:"data"`
}

func fetchModels(ctx context.Context, baseURL, apiKey string) (*modelsBody, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", baseURL, resp.Status)
	}
	var body modelsBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return &body, nil
}

// ListModels asks an endpoint what it can serve. Every OpenAI-compatible
// server implements this, so it works for local runtimes and gateways alike
// without a per-provider catalogue.
func ListModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	body, err := fetchModels(ctx, baseURL, apiKey)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		if !isChatModel(m.ID, m.Architecture.OutputModalities) {
			continue
		}
		out = append(out, m.ID)
	}
	return out, nil
}
