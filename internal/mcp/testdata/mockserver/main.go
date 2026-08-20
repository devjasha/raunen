// Command mockserver is a minimal MCP server used by the mcp package's tests.
// It implements just enough of the protocol to exercise initialize, tools/list,
// tools/call, resources/list (paginated), resources/read (text + blob),
// resources/templates/list, prompts/list and prompts/get.
//
// Resources and prompts are only advertised when MCP_FEATURES=1 is set, so the
// tests that assert tools are absent without the capability can run the same
// binary with the variable unset. It is never installed; the test runs it with
// `go run`.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 8<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	features := os.Getenv("MCP_FEATURES") == "1"

	// grown records that an "announce" call has extended the toolset, so a later
	// tools/list reports the extra tool.
	grown := false

	// Known resources, in two pages so pagination is genuinely exercised. The
	// second resource is a blob: base64 of a small image, which the client must
	// not return verbatim.
	blob := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	page1 := []map[string]any{{
		"uri":         "file:///notes.txt",
		"name":        "Notes",
		"description": "Plain-text notes",
		"mimeType":    "text/plain",
	}}
	page2 := []map[string]any{{
		"uri":         "file:///diagram.png",
		"name":        "Diagram",
		"description": "A small binary image",
		"mimeType":    "image/png",
	}}

	for in.Scan() {
		line := in.Text()
		if line == "" {
			continue
		}
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				URI       string          `json:"uri"`
				Cursor    string          `json:"cursor"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			caps := map[string]any{"tools": map[string]any{"listChanged": true}}
			if features {
				caps["resources"] = map[string]any{"listChanged": true}
				caps["prompts"] = map[string]any{"listChanged": true}
			}
			write(out, req.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    caps,
				"serverInfo":      map[string]any{"name": "mockserver"},
			})
		case "ping":
			write(out, req.ID, map[string]any{})
		case "tools/list":
			tools := []map[string]any{
				{
					"name":        "ping",
					"description": "echo the message back",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"msg": map[string]any{"type": "string"}},
						"required":   []string{"msg"},
					},
				},
				{
					"name":        "lookup",
					"description": "read a value without changing anything",
					// readOnlyHint true declares the tool only reads, so the
					// client should treat it as non-mutating (plan-safe).
					"annotations": map[string]any{"readOnlyHint": true},
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"key": map[string]any{"type": "string"}},
						"required":   []string{"key"},
					},
				},
			}
			// After an "announce" call the server grows a tool, which is what the
			// client should discover when it re-lists on tools/list_changed.
			if grown {
				tools = append(tools, map[string]any{
					"name":        "extra",
					"description": "only exists after the list changed",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				})
			}
			write(out, req.ID, map[string]any{"tools": tools})
		case "tools/call":
			// "announce" grows the toolset and tells the client about it, so the
			// live-refresh path can be exercised end to end.
			if req.Params.Name == "announce" {
				grown = true
				notify(out, "notifications/tools/list_changed")
				write(out, req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": "announced"}},
				})
				continue
			}
			var args struct {
				Msg string `json:"msg"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &args)
			if args.Msg == "boom" {
				write(out, req.ID, map[string]any{
					"isError": true,
					"content": []map[string]any{{"type": "text", "text": "boom"}},
				})
				continue
			}
			write(out, req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": "pong: " + args.Msg}},
			})
		case "resources/list":
			// Two pages: the first carries a cursor, the second closes it.
			if req.Params.Cursor == "page2" {
				write(out, req.ID, map[string]any{"resources": page2})
			} else {
				write(out, req.ID, map[string]any{
					"resources":  page1,
					"nextCursor": "page2",
				})
			}
		case "resources/templates/list":
			write(out, req.ID, map[string]any{
				"resourceTemplates": []map[string]any{{
					"uriTemplate": "file:///{path}",
					"name":        "File by path",
					"description": "Read any file under the root",
					"mimeType":    "text/plain",
				}},
			})
		case "resources/read":
			switch req.Params.URI {
			case "file:///notes.txt":
				write(out, req.ID, map[string]any{
					"contents": []map[string]any{{
						"uri":      "file:///notes.txt",
						"mimeType": "text/plain",
						"text":     "the secret is 42",
					}},
				})
			case "file:///diagram.png":
				write(out, req.ID, map[string]any{
					"contents": []map[string]any{{
						"uri":      "file:///diagram.png",
						"mimeType": "image/png",
						"blob":     blob,
					}},
				})
			default:
				write(out, req.ID, map[string]any{
					"contents": []map[string]any{},
				})
			}
		case "prompts/list":
			write(out, req.ID, map[string]any{
				"prompts": []map[string]any{{
					"name":        "summarize",
					"description": "Summarize a body of text",
					"arguments": []map[string]any{
						{"name": "text", "description": "the text to summarize", "required": true},
					},
				}},
			})
		case "prompts/get":
			// The arguments object's keys are the prompt's own argument names
			// (e.g. "text"), not an "arguments" wrapper, so decode straight into
			// a string map.
			argValues := map[string]string{}
			if err := json.Unmarshal(req.Params.Arguments, &argValues); err != nil {
				write(out, req.ID, map[string]any{"error": map[string]any{"code": -32602, "message": "bad arguments"}})
				continue
			}
			text := ""
			if argValues != nil {
				text = argValues["text"]
			}
			write(out, req.ID, map[string]any{
				"description": "Summarize the text",
				"messages": []map[string]any{{
					"role": "user",
					"content": map[string]any{
						"type": "text",
						"text": fmt.Sprintf("Summarize this: %s", text),
					},
				}},
			})
		}
	}
}

// notify sends a JSON-RPC notification: no id, so the client routes it to its
// notification handler rather than to a waiting call.
func notify(out *bufio.Writer, method string) {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	})
	fmt.Fprintln(out, string(b))
	out.Flush()
}

func write(out *bufio.Writer, id int, result any) {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
	fmt.Fprintln(out, string(b))
	out.Flush()
}
