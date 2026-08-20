package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"raunen/internal/tools"
)

// listings caches the resource and prompt lists a client has fetched, so a
// model that asks to list them repeatedly does not pay a round trip per page
// each time. It is invalidated when the server says the set changed.
//
// It is a pointer held on the Client and guarded by its own mutex rather than
// the Client's, because a list_changed notification can arrive on the transport's
// read goroutine at the same moment a tool call is reading through here.
type listings struct {
	mu        sync.Mutex
	resources []Resource
	prompts   []Prompt
}

func (l *listings) get() (*listings, bool) {
	if l == nil {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l, true
}

// cachedResources returns the cached list and whether one exists. A nil cache
// reads as "no cache", so callers fall through to fetching, which is the safe
// path.
func (l *listings) cachedResources() ([]Resource, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.resources == nil {
		return nil, false
	}
	return l.resources, true
}

func (l *listings) setResources(rs []Resource) {
	l.mu.Lock()
	l.resources = rs
	l.mu.Unlock()
}

func (l *listings) cachedPrompts() ([]Prompt, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.prompts == nil {
		return nil, false
	}
	return l.prompts, true
}

func (l *listings) setPrompts(ps []Prompt) {
	l.mu.Lock()
	l.prompts = ps
	l.mu.Unlock()
}

// Resource is one entry from resources/list: a piece of read-only data the
// server will hand over, addressed by URI. The URI is opaque to raunen — a
// server may use file://, postgres:// or a scheme of its own invention — so it
// is carried through to resources/read unchanged.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

// ResourceTemplate is an entry from resources/templates/list: an RFC 6570 URI
// template such as file:///{path} that describes a family of resources rather
// than one. raunen does not expand templates itself; it shows them to the model,
// which fills them in and reads the result.
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

// maxListPages bounds how far a paginated listing will follow nextCursor.
//
// The protocol puts no limit on the number of pages, and a server that always
// returns a cursor — whether from a bug or from a genuinely endless listing —
// would otherwise hold the turn open until its context expired. Twenty pages is
// far more than any listing the model can usefully be shown.
const maxListPages = 20

// ListResources returns every resource the server offers, following nextCursor
// to the end of the listing. The result is cached until the server says its
// resources changed, because a listing is re-read on every mcp_list_resources
// call and a large server makes that a round trip per page each time.
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	if c.caps.Resources == nil {
		return nil, fmt.Errorf("mcp %q: server does not offer resources", c.name)
	}
	if cached, ok := c.listings.cachedResources(); ok {
		return cached, nil
	}
	var out []Resource
	cursor := ""
	for page := 0; page < maxListPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		// call has already peeled the {"result": ...} envelope, so these fields
		// are the result body itself. Nesting them under another "result" would
		// decode cleanly and leave the listing empty.
		var res struct {
			Error      *rpcError  `json:"error"`
			Resources  []Resource `json:"resources"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := c.call(ctx, "resources/list", params, &res); err != nil {
			return nil, err
		}
		if res.Error != nil {
			return nil, fmt.Errorf("mcp %q: resources/list: [%d] %s", c.name, res.Error.Code, res.Error.Message)
		}
		out = append(out, res.Resources...)
		// A server that repeats the cursor it was given is paging in place; stop
		// rather than fetch the same page until the page bound runs out.
		if res.NextCursor == "" || res.NextCursor == cursor {
			break
		}
		cursor = res.NextCursor
	}
	c.listings.setResources(out)
	return out, nil
}

// ListResourceTemplates returns the URI templates the server offers. Templates
// are a separate listing in the protocol and are not paginated by every server,
// but the cursor is followed the same way when one is sent.
func (c *Client) ListResourceTemplates(ctx context.Context) ([]ResourceTemplate, error) {
	if c.caps.Resources == nil {
		return nil, fmt.Errorf("mcp %q: server does not offer resources", c.name)
	}
	var out []ResourceTemplate
	cursor := ""
	for page := 0; page < maxListPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var res struct {
			Error      *rpcError          `json:"error"`
			Templates  []ResourceTemplate `json:"resourceTemplates"`
			NextCursor string             `json:"nextCursor"`
		}
		if err := c.call(ctx, "resources/templates/list", params, &res); err != nil {
			return nil, err
		}
		if res.Error != nil {
			return nil, fmt.Errorf("mcp %q: resources/templates/list: [%d] %s",
				c.name, res.Error.Code, res.Error.Message)
		}
		out = append(out, res.Templates...)
		if res.NextCursor == "" || res.NextCursor == cursor {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// ReadResource fetches one resource and renders it as text.
//
// Binary contents arrive base64-encoded, and are deliberately not returned:
// base64 is a third larger than the bytes it encodes, is of no use to a model
// that cannot see images, and would evict the conversation from a local 8k
// window in a single read. A note of the size and type is enough for the model
// to decide what to do next.
func (c *Client) ReadResource(ctx context.Context, uri string) (string, error) {
	if c.caps.Resources == nil {
		return "", fmt.Errorf("mcp %q: server does not offer resources", c.name)
	}
	if strings.TrimSpace(uri) == "" {
		return "", fmt.Errorf("uri is required")
	}
	var res struct {
		Error    *rpcError `json:"error"`
		Contents []struct {
			URI      string `json:"uri"`
			MimeType string `json:"mimeType"`
			Text     string `json:"text"`
			Blob     string `json:"blob"`
		} `json:"contents"`
	}
	if err := c.call(ctx, "resources/read", map[string]any{"uri": uri}, &res); err != nil {
		return "", err
	}
	if res.Error != nil {
		return "", fmt.Errorf("mcp %q: resources/read %s: [%d] %s", c.name, uri, res.Error.Code, res.Error.Message)
	}
	if len(res.Contents) == 0 {
		return "", fmt.Errorf("mcp %q: resource %s returned no contents", c.name, uri)
	}
	var parts []string
	for _, part := range res.Contents {
		if part.Blob != "" {
			mime := part.MimeType
			if mime == "" {
				mime = "application/octet-stream"
			}
			// Report the decoded size, not the length of the encoding: the
			// encoded form is an artefact of the wire and saying "16 bytes" of a
			// 12-byte image is just wrong.
			n := len(part.Blob)
			if raw, err := base64.StdEncoding.DecodeString(part.Blob); err == nil {
				n = len(raw)
			}
			parts = append(parts, fmt.Sprintf("[binary content, %d bytes, %s]", n, mime))
			continue
		}
		parts = append(parts, part.Text)
	}
	return strings.Join(parts, "\n"), nil
}

// resourceTools returns the tools through which the model reaches this server's
// resources. They exist only when the server advertised the capability, so a
// model is never shown a tool whose only possible answer is "method not found".
//
// Reading is read-only, so both tools carry Mutates:false and stay usable in
// plan mode — which is where they are most wanted, since investigating is
// exactly what plan mode is for.
func (c *Client) resourceTools() []tools.Tool {
	if c.caps.Resources == nil {
		return nil
	}
	return []tools.Tool{
		{
			Name: "mcp_list_resources",
			Description: "[" + c.name + "] List the resources (read-only data addressed by URI) " +
				"offered by the " + c.name + " MCP server. Returns URIs with a short description, " +
				"not their content — read one with mcp_read_resource.",
			Params: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
				"required":   []string{},
			},
			Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
				rs, err := c.ListResources(ctx)
				if err != nil {
					return "", err
				}
				// Templates describe resources that have no fixed URI, so a
				// listing without them looks empty on servers that only offer
				// them. A failure here is not fatal: the concrete listing is
				// still worth returning.
				ts, _ := c.ListResourceTemplates(ctx)
				return renderResources(c.name, rs, ts), nil
			},
		},
		{
			Name: "mcp_read_resource",
			Description: "[" + c.name + "] Read one resource from the " + c.name + " MCP server by URI. " +
				"Use mcp_list_resources first to find a URI. Binary resources report their " +
				"size and type rather than their bytes.",
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"uri": map[string]any{
						"type":        "string",
						"description": "Resource URI exactly as listed, e.g. \"file:///log.txt\".",
					},
				},
				"required": []string{"uri"},
			},
			Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					URI string `json:"uri"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				out, err := c.ReadResource(ctx, a.URI)
				if err != nil {
					return "", err
				}
				return clampOutput(out), nil
			},
		},
	}
}

// renderResources turns a listing into the lines the model sees. It is bounded
// the same way the tool catalogue is: a server with five thousand resources must
// not spend the whole context describing them before anything is read.
func renderResources(server string, rs []Resource, ts []ResourceTemplate) string {
	if len(rs) == 0 && len(ts) == 0 {
		return fmt.Sprintf("The %s MCP server offers no resources.", server)
	}
	var sb strings.Builder
	shown := min(len(rs), maxResults)
	fmt.Fprintf(&sb, "%d of %d resources from %s", shown, len(rs), server)
	if shown < len(rs) {
		sb.WriteString(" (listing truncated)")
	}
	sb.WriteString(":\n")
	for _, r := range rs[:shown] {
		sb.WriteString("  " + resourceLine(r.URI, r.Name, r.Description) + "\n")
	}
	if len(ts) > 0 {
		tshown := min(len(ts), maxResults)
		fmt.Fprintf(&sb, "%d of %d URI templates (fill in the {placeholders} and read the result):\n",
			tshown, len(ts))
		for _, t := range ts[:tshown] {
			sb.WriteString("  " + resourceLine(t.URITemplate, t.Name, t.Description) + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// resourceLine is one line of a listing: the URI to read by, and just enough
// prose to choose with.
func resourceLine(uri, name, desc string) string {
	const maxURI = 200
	if len(uri) > maxURI {
		uri = uri[:maxURI] + "…"
	}
	label := strings.TrimSpace(name)
	if d := shortDesc(desc); d != "" {
		if label != "" {
			label += " — " + d
		} else {
			label = d
		}
	}
	if label == "" {
		return uri
	}
	return uri + "  " + label
}

// clampOutput bounds one resource or prompt body. MCP tool results are handed to
// the model verbatim, so nothing else stands between a 40MB log file and a local
// model's context window.
func clampOutput(s string) string {
	const maxBody = 30 << 10
	if len(s) <= maxBody {
		return s
	}
	return s[:maxBody] + fmt.Sprintf(
		"\n... [truncated, %d bytes total]", len(s))
}

// ResourcePromptTools returns the tools through which the model reaches this
// server's resources and prompts, in one slice. It is empty unless the server
// advertised the relevant capability, so callers can append it blindly.
func (c *Client) ResourcePromptTools() []tools.Tool {
	return append(c.resourceTools(), c.promptTools()...)
}
