package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"raunen/internal/tools"
)

// Catalog holds the tools an MCP server advertised without putting them in
// front of the model.
//
// A tool costs context on every single request, whether or not it is ever
// called: its name, description and full JSON Schema are re-sent with each
// turn. That is affordable for the five built-ins and ruinous for a server
// like @modelcontextprotocol/server-everything, which alone can advertise more
// schema than a local 8k model has room for. Connecting two such servers used
// to leave no space for the conversation.
//
// So the tools are held here instead, and the model is given two small tools to
// reach them with: one to search the catalogue by keyword, one to load a chosen
// tool's schema. Only what a task actually needs is ever paid for. This mirrors
// what other agents settled on, and it matters more here than for most, because
// raunen's whole reason to exist is models with small windows.
type Catalog struct {
	mu sync.Mutex
	// entries are every tool held back, keyed by the name the model would call.
	entries map[string]tools.Tool
	// order preserves discovery order so listings are stable between turns —
	// a catalogue that reshuffles itself is one the model cannot refer back to.
	order []string
	// selected names the tools already loaded into the registry, so selecting
	// one twice is a no-op rather than a duplicate.
	selected map[string]bool

	// onSelect is called when a tool is chosen, to add it to the live registry.
	// It is how the catalogue reaches the agent without importing it.
	onSelect func(tools.Tool)
}

// NewCatalog returns an empty catalogue. onSelect is called with each tool as
// it is chosen, and is what actually puts it in front of the model.
func NewCatalog(onSelect func(tools.Tool)) *Catalog {
	return &Catalog{
		entries:  map[string]tools.Tool{},
		selected: map[string]bool{},
		onSelect: onSelect,
	}
}

// Add files a tool under the name the model will call it by. Adding the same
// name twice replaces the entry, which is what a server refreshing its toolset
// should do.
func (c *Catalog) Add(t tools.Tool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, seen := c.entries[t.Name]; !seen {
		c.order = append(c.order, t.Name)
	}
	c.entries[t.Name] = t
}

// Len reports how many tools are held back.
func (c *Catalog) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Replace swaps the whole set of tools contributed by one server, dropping any
// it no longer advertises. It is what a tools/list_changed notification drives.
//
// A tool already selected stays selected if the server still offers it, so a
// refresh does not silently pull a tool out from under a turn that is using it.
func (c *Catalog) Replace(server string, ts []tools.Tool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	prefix := server + "_"
	keep := map[string]bool{}
	for _, t := range ts {
		keep[t.Name] = true
	}
	// Drop this server's entries that are gone, leaving other servers alone.
	var order []string
	for _, name := range c.order {
		mine := name == server || strings.HasPrefix(name, prefix)
		if mine && !keep[name] {
			delete(c.entries, name)
			delete(c.selected, name)
			continue
		}
		order = append(order, name)
	}
	c.order = order
	for _, t := range ts {
		if _, seen := c.entries[t.Name]; !seen {
			c.order = append(c.order, t.Name)
		}
		c.entries[t.Name] = t
	}
}

// match scores a catalogue entry against search terms. Every term must appear
// somewhere in the name or description, which keeps a two-word query from
// returning everything that matched only one of them.
func match(t tools.Tool, terms []string) bool {
	hay := strings.ToLower(t.Name + " " + t.Description)
	for _, term := range terms {
		if !strings.Contains(hay, term) {
			return false
		}
	}
	return true
}

// summary is one line of a search result: enough to choose by, and no schema.
// The schema is the expensive part and is what select is for.
func summary(t tools.Tool) string {
	desc := shortDesc(t.Description)
	if desc == "" {
		return t.Name
	}
	return t.Name + " — " + desc
}

// shortDesc cuts a description down to the first sentence or line, capped.
// Descriptions from a server can run to paragraphs, and a listing that repeats
// them in full costs more context than the thing being listed. Resource and
// prompt listings reuse it for the same reason.
func shortDesc(desc string) string {
	desc = strings.TrimSpace(desc)
	if i := strings.IndexAny(desc, ".\n"); i > 0 {
		desc = desc[:i]
	}
	const maxDesc = 140
	if len(desc) > maxDesc {
		desc = strings.TrimSpace(desc[:maxDesc]) + "…"
	}
	return desc
}

// maxResults caps a search listing. A model that asks for "file" against a
// large catalogue should get the most plausible handful, not four hundred
// lines that cost more than the schemas would have.
const maxResults = 25

// SearchTool returns the tool the model uses to find what a server offers.
func (c *Catalog) SearchTool() tools.Tool {
	return tools.Tool{
		Name: "mcp_search_tools",
		Description: "Search the tools available from connected MCP servers. " +
			"Returns matching tool names with a short description. Call this " +
			"first to find a tool, then mcp_select_tool to load it before use. " +
			"Omit the query to list everything available.",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type": "string",
					"description": "Words to match against tool names and descriptions, " +
						"e.g. \"read file\". Omit to list all tools.",
				},
			},
			"required": []string{},
		},
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Query string `json:"query"`
			}
			// A model may send no arguments at all when listing everything.
			_ = json.Unmarshal(raw, &a)

			var terms []string
			for _, f := range strings.Fields(strings.ToLower(a.Query)) {
				terms = append(terms, f)
			}

			c.mu.Lock()
			defer c.mu.Unlock()

			var hits []string
			truncated := false
			for _, name := range c.order {
				t := c.entries[name]
				if len(terms) > 0 && !match(t, terms) {
					continue
				}
				if len(hits) == maxResults {
					truncated = true
					break
				}
				line := summary(t)
				if c.selected[name] {
					// Saying so stops the model selecting it again, and tells it
					// the tool is callable right now.
					line += "  [loaded]"
				}
				hits = append(hits, line)
			}

			if len(hits) == 0 {
				if len(c.entries) == 0 {
					return "No MCP servers are connected, so there are no tools to search.", nil
				}
				return fmt.Sprintf("No MCP tool matches %q. %d tools are available; "+
					"call mcp_search_tools with no query to list them.", a.Query, len(c.entries)), nil
			}

			var sb strings.Builder
			fmt.Fprintf(&sb, "%d of %d MCP tools", len(hits), len(c.entries))
			if truncated {
				sb.WriteString(" (more matched; narrow the query)")
			}
			sb.WriteString(":\n")
			for _, h := range hits {
				sb.WriteString("  " + h + "\n")
			}
			sb.WriteString("\nCall mcp_select_tool with a name to load it before using it.")
			return sb.String(), nil
		},
	}
}

// SelectTool returns the tool that loads a catalogue entry so the model can
// call it. Selecting is what moves a tool's schema into the request.
func (c *Catalog) SelectTool() tools.Tool {
	return tools.Tool{
		Name: "mcp_select_tool",
		Description: "Load an MCP tool found with mcp_search_tools so it can be " +
			"called. Pass the exact tool name. Once loaded, call the tool directly.",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Exact tool name as returned by mcp_search_tools.",
				},
			},
			"required": []string{"name"},
		},
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			name := strings.TrimSpace(a.Name)
			if name == "" {
				return "", fmt.Errorf("name is required")
			}

			c.mu.Lock()
			t, ok := c.entries[name]
			if !ok {
				// Suggest near misses rather than just refusing: the model has
				// usually mistyped a prefix or dropped the server name.
				near := c.nearLocked(name)
				c.mu.Unlock()
				if len(near) > 0 {
					return "", fmt.Errorf("no MCP tool named %q. Did you mean: %s?",
						name, strings.Join(near, ", "))
				}
				return "", fmt.Errorf("no MCP tool named %q; use mcp_search_tools to find one", name)
			}
			already := c.selected[name]
			c.selected[name] = true
			onSelect := c.onSelect
			c.mu.Unlock()

			if already {
				return fmt.Sprintf("%s is already loaded — call it directly.", name), nil
			}
			if onSelect != nil {
				onSelect(t)
			}
			// The schema is now in the request, so the model can see the
			// parameters itself; repeating them here would pay for them twice.
			return fmt.Sprintf("Loaded %s. Its parameters are now available — call it directly.", name), nil
		},
	}
}

// nearLocked returns catalogue names close to what was asked for. The caller
// holds mu.
func (c *Catalog) nearLocked(want string) []string {
	want = strings.ToLower(want)
	var out []string
	for _, name := range c.order {
		l := strings.ToLower(name)
		if strings.Contains(l, want) || strings.Contains(want, l) {
			out = append(out, name)
		}
		if len(out) == 3 {
			break
		}
	}
	sort.Strings(out)
	return out
}

// Advertised reports whether name is a tool this catalogue is holding back
// rather than one the model can already call. It is what lets dispatch tell a
// model "load it first" instead of "no such tool": the name is known to raunen
// but lives behind mcp_select_tool, so the right reply is the recovery, not a
// dead end. fx calls these "advertised dynamic tool names" and gives them the
// same recovery path.
func (c *Catalog) Advertised(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[name]
	return ok
}
