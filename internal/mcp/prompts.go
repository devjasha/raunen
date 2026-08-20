package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"raunen/internal/tools"
)

// Prompt is one entry from prompts/list: a template the server will render, with
// the arguments it expects. raunen does not run prompts as prompts — it fetches
// the rendered text and hands it to the model as a tool result, which is the
// only way a server-supplied template can reach a conversation raunen already
// owns.
type Prompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Arguments   []PromptArgument `json:"arguments"`
}

// PromptArgument describes one placeholder a prompt expects.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// ListPrompts returns every prompt the server offers, following nextCursor to
// the end of the listing. Like resources, the result is cached until the server
// says its prompts changed.
func (c *Client) ListPrompts(ctx context.Context) ([]Prompt, error) {
	if c.caps.Prompts == nil {
		return nil, fmt.Errorf("mcp %q: server does not offer prompts", c.name)
	}
	if cached, ok := c.listings.cachedPrompts(); ok {
		return cached, nil
	}
	var out []Prompt
	cursor := ""
	for page := 0; page < maxListPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		// call peels the {"result": ...} envelope already; these are the result
		// body's own fields.
		var res struct {
			Error      *rpcError `json:"error"`
			Prompts    []Prompt  `json:"prompts"`
			NextCursor string    `json:"nextCursor"`
		}
		if err := c.call(ctx, "prompts/list", params, &res); err != nil {
			return nil, err
		}
		if res.Error != nil {
			return nil, fmt.Errorf("mcp %q: prompts/list: [%d] %s", c.name, res.Error.Code, res.Error.Message)
		}
		out = append(out, res.Prompts...)
		if res.NextCursor == "" || res.NextCursor == cursor {
			break
		}
		cursor = res.NextCursor
	}
	c.listings.setPrompts(out)
	return out, nil
}

// GetPrompt renders one prompt with the given arguments and returns it as plain
// text, one "role: text" line per message. The message structure is flattened
// because the result becomes a single tool result in raunen's own conversation;
// there is nowhere to put a second set of roles.
func (c *Client) GetPrompt(ctx context.Context, name string, args map[string]string) (string, error) {
	if c.caps.Prompts == nil {
		return "", fmt.Errorf("mcp %q: server does not offer prompts", c.name)
	}
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("name is required")
	}
	params := map[string]any{"name": name}
	// The protocol wants string values; an omitted arguments object is valid, but
	// sending an empty one is what most servers are tested against.
	if args == nil {
		args = map[string]string{}
	}
	params["arguments"] = args

	var res struct {
		Error       *rpcError `json:"error"`
		Description string    `json:"description"`
		Messages    []struct {
			Role    string `json:"role"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := c.call(ctx, "prompts/get", params, &res); err != nil {
		return "", err
	}
	if res.Error != nil {
		return "", fmt.Errorf("mcp %q: prompts/get %s: [%d] %s", c.name, name, res.Error.Code, res.Error.Message)
	}
	var sb strings.Builder
	if d := strings.TrimSpace(res.Description); d != "" {
		sb.WriteString(d + "\n\n")
	}
	for i, m := range res.Messages {
		// Only text content is rendered. An image or an embedded resource in a
		// prompt would arrive base64-encoded, and dumping that into the context
		// is the same mistake ReadResource refuses to make.
		if m.Content.Type != "" && m.Content.Type != "text" {
			continue
		}
		if i > 0 {
			sb.WriteByte('\n')
		}
		role := m.Role
		if role == "" {
			role = "user"
		}
		sb.WriteString(role + ": " + m.Content.Text + "\n")
	}
	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		return "", fmt.Errorf("mcp %q: prompt %s rendered no text", c.name, name)
	}
	return out, nil
}

// promptTools returns the tools through which the model reaches this server's
// prompts, and nothing when the server does not advertise the capability.
// Fetching a prompt only reads, so both are Mutates:false and usable in plan
// mode.
func (c *Client) promptTools() []tools.Tool {
	if c.caps.Prompts == nil {
		return nil
	}
	return []tools.Tool{
		{
			Name: "mcp_list_prompts",
			Description: "[" + c.name + "] List the prompt templates offered by the " + c.name +
				" MCP server, with the arguments each one takes. Fetch one with mcp_get_prompt.",
			Params: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
				"required":   []string{},
			},
			Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
				ps, err := c.ListPrompts(ctx)
				if err != nil {
					return "", err
				}
				return renderPrompts(c.name, ps), nil
			},
		},
		{
			Name: "mcp_get_prompt",
			Description: "[" + c.name + "] Fetch a prompt template from the " + c.name +
				" MCP server, rendered with the arguments you supply. Returns its text; " +
				"use mcp_list_prompts to find a name and its arguments.",
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Exact prompt name as returned by mcp_list_prompts.",
					},
					"arguments": map[string]any{
						"type": "object",
						"description": "Values for the prompt's arguments, as an object of " +
							"string values, e.g. {\"language\": \"go\"}.",
					},
				},
				"required": []string{"name"},
			},
			Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Name string `json:"name"`
					// Arguments are decoded loosely: models routinely send a
					// number or a bool where the protocol wants a string, and
					// refusing the call over that is worse than converting it.
					Arguments map[string]any `json:"arguments"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				args := map[string]string{}
				for k, v := range a.Arguments {
					args[k] = stringify(v)
				}
				out, err := c.GetPrompt(ctx, a.Name, args)
				if err != nil {
					return "", err
				}
				return clampOutput(out), nil
			},
		},
	}
}

// stringify renders a JSON value as the string the protocol asks for, leaving a
// string untouched rather than re-quoting it.
func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
}

// renderPrompts turns a prompt listing into the lines the model sees, bounded
// the same way the tool catalogue is so a server with hundreds of prompts cannot
// flood the context before one is fetched.
func renderPrompts(server string, ps []Prompt) string {
	if len(ps) == 0 {
		return fmt.Sprintf("The %s MCP server offers no prompts.", server)
	}
	shown := min(len(ps), maxResults)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d of %d prompts from %s", shown, len(ps), server)
	if shown < len(ps) {
		sb.WriteString(" (listing truncated)")
	}
	sb.WriteString(":\n")
	for _, p := range ps[:shown] {
		line := p.Name
		if d := shortDesc(p.Description); d != "" {
			line += " — " + d
		}
		if names := argNames(p.Arguments); names != "" {
			line += "  (" + names + ")"
		}
		sb.WriteString("  " + line + "\n")
	}
	sb.WriteString("\nCall mcp_get_prompt with a name to fetch one.")
	return sb.String()
}

// argNames lists a prompt's argument names, marking the required ones, so the
// model can call mcp_get_prompt without a second round trip for the schema.
func argNames(args []PromptArgument) string {
	if len(args) == 0 {
		return ""
	}
	var out []string
	for _, a := range args {
		if a.Required {
			out = append(out, a.Name+"*")
			continue
		}
		out = append(out, a.Name)
	}
	// Stable order: a listing that reshuffles between turns is one the model
	// cannot refer back to.
	sort.Strings(out)
	return strings.Join(out, ", ")
}
