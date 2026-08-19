// Command mockserver is a minimal MCP server used by the mcp package's tests.
// It implements just enough of the protocol to exercise initialize, tools/list
// and tools/call: it advertises one tool, "ping", and echoes its argument.
//
// It is never installed; the test runs it with `go run`.
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

	// grown records that an "announce" call has extended the toolset, so a later
	// tools/list reports the extra tool.
	grown := false

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
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			write(out, req.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				// Declaring listChanged is what makes the client subscribe to
				// tools/list_changed, which the "announce" tool below triggers.
				"capabilities": map[string]any{"tools": map[string]any{"listChanged": true}},
				"serverInfo":   map[string]any{"name": "mockserver"},
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
