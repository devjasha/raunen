package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"raunen/internal/tools"
)

// tool builds a catalogue entry for the tests.
func tool(name, desc string) tools.Tool {
	return tools.Tool{
		Name:        name,
		Description: desc,
		Params:      map[string]any{"type": "object", "properties": map[string]any{}},
		Run: func(context.Context, json.RawMessage) (string, error) {
			return "ran " + name, nil
		},
	}
}

// newCat returns a catalogue and the tools selection has loaded so far.
func newCat(t *testing.T, entries ...tools.Tool) (*Catalog, *[]tools.Tool) {
	t.Helper()
	var loaded []tools.Tool
	c := NewCatalog(func(x tools.Tool) { loaded = append(loaded, x) })
	for _, e := range entries {
		c.Add(e)
	}
	return c, &loaded
}

func run(t *testing.T, tl tools.Tool, args string) string {
	t.Helper()
	out, err := tl.Run(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s(%s): %v", tl.Name, args, err)
	}
	return out
}

// Searching must narrow on every term, so a two-word query does not return
// everything that matched only one of them.
func TestSearchRequiresEveryTerm(t *testing.T) {
	c, _ := newCat(t,
		tool("fs_read_file", "Read a file from disk"),
		tool("fs_write_file", "Write a file to disk"),
		tool("db_query", "Run a database query"),
	)
	out := run(t, c.SearchTool(), `{"query":"read file"}`)
	if !strings.Contains(out, "fs_read_file") {
		t.Errorf("search should find fs_read_file:\n%s", out)
	}
	if strings.Contains(out, "db_query") {
		t.Errorf("search should not return unrelated tools:\n%s", out)
	}
	if strings.Contains(out, "fs_write_file") {
		t.Errorf("every term must match; write does not match \"read\":\n%s", out)
	}
}

// An empty query lists the catalogue, which is how a model discovers what is
// there without having to guess a keyword first.
func TestSearchWithoutQueryListsAll(t *testing.T) {
	c, _ := newCat(t, tool("a_one", "first"), tool("b_two", "second"))
	out := run(t, c.SearchTool(), `{}`)
	for _, want := range []string{"a_one", "b_two"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing should contain %q:\n%s", want, out)
		}
	}
}

// A search result must not carry the full JSON Schema: the whole point is that
// the expensive part is deferred until a tool is selected.
func TestSearchOmitsSchema(t *testing.T) {
	c, _ := newCat(t, tools.Tool{
		Name:        "big",
		Description: "A tool",
		Params: map[string]any{"type": "object", "properties": map[string]any{
			"distinctive_parameter_name": map[string]any{"type": "string"},
		}},
	})
	out := run(t, c.SearchTool(), `{}`)
	if strings.Contains(out, "distinctive_parameter_name") {
		t.Errorf("search result leaked the schema, which defeats lazy loading:\n%s", out)
	}
}

// Selecting must hand the tool to the registry, which is what actually makes it
// callable by the model.
func TestSelectLoadsTool(t *testing.T) {
	c, loaded := newCat(t, tool("fs_read_file", "Read a file"))
	run(t, c.SelectTool(), `{"name":"fs_read_file"}`)
	if len(*loaded) != 1 || (*loaded)[0].Name != "fs_read_file" {
		t.Fatalf("select should load exactly the chosen tool, got %v", *loaded)
	}
	// The loaded tool must still work, not just carry the right name.
	if out := run(t, (*loaded)[0], `{}`); out != "ran fs_read_file" {
		t.Errorf("loaded tool did not run: %q", out)
	}
}

// Selecting the same tool twice must not register it again, or the model would
// see a duplicate and the schema would be paid for twice.
func TestSelectIsIdempotent(t *testing.T) {
	c, loaded := newCat(t, tool("one", "first"))
	run(t, c.SelectTool(), `{"name":"one"}`)
	out := run(t, c.SelectTool(), `{"name":"one"}`)
	if len(*loaded) != 1 {
		t.Errorf("selecting twice loaded %d tools, want 1", len(*loaded))
	}
	if !strings.Contains(out, "already loaded") {
		t.Errorf("second select should say it is already loaded, got %q", out)
	}
}

// An unknown name must fail with a usable message rather than silently doing
// nothing, and should point at near misses since a model usually mistypes a
// prefix rather than inventing a tool.
func TestSelectUnknownSuggests(t *testing.T) {
	c, _ := newCat(t, tool("fs_read_file", "Read a file"))
	_, err := c.SelectTool().Run(context.Background(), json.RawMessage(`{"name":"read_file"}`))
	if err == nil {
		t.Fatal("selecting an unknown tool should fail")
	}
	if !strings.Contains(err.Error(), "fs_read_file") {
		t.Errorf("error should suggest the near miss, got %v", err)
	}
}

// A search must mark what is already loaded, so the model calls it instead of
// selecting it a second time.
func TestSearchMarksLoaded(t *testing.T) {
	c, _ := newCat(t, tool("one", "first"), tool("two", "second"))
	run(t, c.SelectTool(), `{"name":"one"}`)
	out := run(t, c.SearchTool(), `{}`)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "one") && !strings.Contains(line, "[loaded]") {
			t.Errorf("selected tool should be marked loaded:\n%s", out)
		}
		if strings.Contains(line, "two") && strings.Contains(line, "[loaded]") {
			t.Errorf("unselected tool must not be marked loaded:\n%s", out)
		}
	}
}

// Replace models a server revising its toolset: tools it no longer offers must
// disappear, and other servers must be left alone.
func TestReplaceDropsWithdrawnTools(t *testing.T) {
	c, _ := newCat(t,
		tool("srv_a", "from srv"),
		tool("srv_b", "from srv"),
		tool("other_x", "from other"),
	)
	c.Replace("srv", []tools.Tool{tool("srv_a", "from srv")})

	out := run(t, c.SearchTool(), `{}`)
	if strings.Contains(out, "srv_b") {
		t.Errorf("a withdrawn tool should be gone:\n%s", out)
	}
	if !strings.Contains(out, "srv_a") {
		t.Errorf("a retained tool should survive:\n%s", out)
	}
	if !strings.Contains(out, "other_x") {
		t.Errorf("another server's tools must not be touched:\n%s", out)
	}
}

// A search that matches nothing must say so and point the model at the listing,
// rather than returning an empty result it cannot act on.
func TestSearchNoMatchExplains(t *testing.T) {
	c, _ := newCat(t, tool("one", "first"))
	out := run(t, c.SearchTool(), `{"query":"nonexistent"}`)
	if !strings.Contains(out, "mcp_search_tools") {
		t.Errorf("a miss should tell the model how to list everything:\n%s", out)
	}
}
