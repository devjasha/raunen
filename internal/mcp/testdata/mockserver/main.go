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
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "mockserver"},
			})
		case "tools/list":
			write(out, req.ID, map[string]any{
				"tools": []map[string]any{
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
				},
			})
		case "tools/call":
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

func write(out *bufio.Writer, id int, result any) {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
	fmt.Fprintln(out, string(b))
	out.Flush()
}
