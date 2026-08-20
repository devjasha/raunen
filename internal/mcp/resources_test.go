package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"raunen/internal/tools"
)

// newMockFeatures starts the mock with resources and prompts enabled, so the
// capability-gated paths can be exercised. The default newMock runs without
// them, which is what the "absent without the capability" tests rely on.
func newMockFeatures(t *testing.T) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Start(ctx, "mock", Server{
		Command: "go", Args: []string{"run", "./testdata/mockserver"},
		Env: map[string]string{"MCP_FEATURES": "1"},
	})
	if err != nil {
		t.Fatalf("start feature mock server: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func withCtx(t *testing.T) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// Listing resources must follow nextCursor across pages and return entries from
// both of them — a server that always returns a cursor is the case pagination
// exists for, so the two-page listing exercises the real code path.
func TestListResourcesFollowsPagination(t *testing.T) {
	c := newMockFeatures(t)
	ctx, cancel := withCtx(t)
	defer cancel()

	rs, err := c.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	var names []string
	for _, r := range rs {
		names = append(names, r.Name)
	}
	if !contains(names, "Notes") || !contains(names, "Diagram") {
		t.Errorf("both pages should be listed, got %v", names)
	}
}

// Reading a text resource must return its text, which is what the model then
// sees as a tool result.
func TestReadTextResource(t *testing.T) {
	c := newMockFeatures(t)
	ctx, cancel := withCtx(t)
	defer cancel()

	out, err := c.ReadResource(ctx, "file:///notes.txt")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if !strings.Contains(out, "the secret is 42") {
		t.Errorf("expected the resource text, got %q", out)
	}
}

// Reading a blob resource must NOT return the base64 — dumping encoding into a
// local model's context is exactly the failure the endpoint design avoids. The
// visible result should be a size/mime note instead.
func TestReadBlobResourceHidesBase64(t *testing.T) {
	c := newMockFeatures(t)
	ctx, cancel := withCtx(t)
	defer cancel()

	out, err := c.ReadResource(ctx, "file:///diagram.png")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if strings.Contains(out, "iVBORw0KGgo") {
		t.Errorf("base64 leaked into the result: %q", out)
	}
	if !strings.Contains(out, "binary content") || !strings.Contains(out, "image/png") {
		t.Errorf("expected a size/mime note, got %q", out)
	}
}

// Getting a prompt with an argument must render text that contains the value,
// so the supplied argument actually reached the server.
func TestGetPromptRendersArguments(t *testing.T) {
	c := newMockFeatures(t)
	ctx, cancel := withCtx(t)
	defer cancel()

	out, err := c.GetPrompt(ctx, "summarize", map[string]string{"text": "hello world"})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("expected the argument value in the rendered prompt, got %q", out)
	}
}

// Resource and prompt tools must be absent when the server does not advertise
// the capability, so the model is never shown a tool that can only answer
// "method not found". The default mock has no resources/prompts enabled.
func TestResourcePromptToolsAbsentWithoutCapability(t *testing.T) {
	c := newMock(t)
	if len(c.ResourcePromptTools()) != 0 {
		t.Errorf("expected no resource/prompt tools without the capability, got %d",
			len(c.ResourcePromptTools()))
	}
}

// With the capability, the four model-facing tools must appear, and the read
// tools must be read-only so plan mode can use them.
func TestResourcePromptToolsPresentWithCapability(t *testing.T) {
	c := newMockFeatures(t)
	got := map[string]bool{}
	for _, tool := range c.ResourcePromptTools() {
		got[tool.Name] = true
		if (tool.Name == "mcp_list_resources" || tool.Name == "mcp_read_resource" ||
			tool.Name == "mcp_list_prompts" || tool.Name == "mcp_get_prompt") && tool.Mutates {
			t.Errorf("%s should be read-only (Mutates:false) to work in plan mode", tool.Name)
		}
	}
	for _, want := range []string{"mcp_list_resources", "mcp_read_resource", "mcp_list_prompts", "mcp_get_prompt"} {
		if !got[want] {
			t.Errorf("expected tool %q to be present", want)
		}
	}
}

// Calling a resource tool through the registry must round-trip to a real read,
// including the blob-avoidance behaviour. This is the path the model actually
// uses.
func TestReadResourceToolViaRegistry(t *testing.T) {
	c := newMockFeatures(t)
	ctx, cancel := withCtx(t)
	defer cancel()

	reg := tools.Default(t.TempDir(), 4096)
	for _, tool := range c.ResourcePromptTools() {
		reg.Add(tool)
	}
	tool, ok := reg.Get("mcp_read_resource")
	if !ok {
		t.Fatal("mcp_read_resource not in registry")
	}
	out, err := tool.Run(ctx, json.RawMessage(`{"uri":"file:///diagram.png"}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(out, "iVBORw0KGgo") {
		t.Errorf("base64 leaked through the tool: %q", out)
	}

	out, err = tool.Run(ctx, json.RawMessage(`{"uri":"file:///notes.txt"}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "the secret is 42") {
		t.Errorf("text resource not read through registry: %q", out)
	}
}
