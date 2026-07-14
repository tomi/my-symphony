// Command symphony-lineargql-mcp is a minimal MCP stdio server that advertises
// the linear_graphql tool to a Claude Code session (SPEC §10.5). It is wired
// into claude.command (for example via --mcp-config) so the session can execute
// Linear GraphQL using Symphony's configured auth, which flows through the
// process environment (LINEAR_API_KEY) rather than being read from disk.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/tomi/my-symphony/internal/tools/lineargql"
)

const protocolVersion = "2024-11-05"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	endpoint := os.Getenv("LINEAR_ENDPOINT")
	apiKey := os.Getenv("LINEAR_API_KEY")
	tool := lineargql.New(endpoint, apiKey, nil)

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	for in.Scan() {
		line := in.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		resp, isNotification := handle(&req, tool)
		if isNotification {
			continue
		}
		buf, _ := json.Marshal(resp)
		out.Write(buf)
		out.WriteByte('\n')
		out.Flush()
	}
}

func handle(req *rpcRequest, tool *lineargql.Tool) (*rpcResponse, bool) {
	switch req.Method {
	case "initialize":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "symphony-lineargql", "version": "1.0.0"},
		}}, false

	case "notifications/initialized":
		return nil, true

	case "tools/list":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"tools": []any{toolSchema()},
		}}, false

	case "tools/call":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: callTool(req.Params, tool)}, false

	default:
		if len(req.ID) == 0 {
			return nil, true // unknown notification
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{
			Code: -32601, Message: "method not found: " + req.Method,
		}}, false
	}
}

func toolSchema() map[string]any {
	return map[string]any{
		"name": "linear_graphql",
		"description": "Execute a single Linear GraphQL query or mutation using " +
			"Symphony's configured tracker auth.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":     map[string]any{"type": "string", "description": "One GraphQL operation."},
				"variables": map[string]any{"type": "object", "description": "Optional variables."},
			},
			"required": []string{"query"},
		},
	}
}

// callTool executes the requested tool. Unsupported tool names still return a
// failure result (isError) rather than a protocol error so the session does not
// stall (SPEC §10.5).
func callTool(params json.RawMessage, tool *lineargql.Tool) map[string]any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return toolResult(fmt.Sprintf(`{"success":false,"error":{"code":"invalid_input","message":%q}}`, err.Error()), true)
	}
	if p.Name != "linear_graphql" {
		return toolResult(fmt.Sprintf(`{"success":false,"error":{"code":"unsupported_tool","message":"unsupported tool: %s"}}`, p.Name), true)
	}
	input, err := lineargql.ParseInput(p.Arguments)
	if err != nil {
		return toolResult(fmt.Sprintf(`{"success":false,"error":{"code":"invalid_input","message":%q}}`, err.Error()), true)
	}
	res := tool.Execute(context.Background(), input)
	buf, _ := json.Marshal(res)
	return toolResult(string(buf), !res.Success)
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": isError,
	}
}
