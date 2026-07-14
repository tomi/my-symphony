package main

import (
	"encoding/json"
	"testing"

	"github.com/tomi/my-symphony/internal/tools/lineargql"
)

func TestHandle_Initialize(t *testing.T) {
	req := &rpcRequest{Method: "initialize", ID: json.RawMessage(`1`)}
	resp, isNotif := handle(req, lineargql.New("", "", nil))
	if isNotif || resp == nil {
		t.Fatalf("initialize should respond")
	}
	result := resp.Result.(map[string]any)
	if result["protocolVersion"] != protocolVersion {
		t.Errorf("protocol version = %v", result["protocolVersion"])
	}
}

func TestHandle_ToolsList(t *testing.T) {
	req := &rpcRequest{Method: "tools/list", ID: json.RawMessage(`2`)}
	resp, _ := handle(req, lineargql.New("", "", nil))
	result := resp.Result.(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "linear_graphql" {
		t.Errorf("tool name = %v", tool["name"])
	}
}

func TestHandle_UnsupportedToolNameDoesNotStall(t *testing.T) {
	params, _ := json.Marshal(map[string]any{"name": "other_tool", "arguments": map[string]any{}})
	req := &rpcRequest{Method: "tools/call", ID: json.RawMessage(`3`), Params: params}
	resp, _ := handle(req, lineargql.New("", "", nil))
	// The call returns a result (not a protocol error) with isError=true.
	result := resp.Result.(map[string]any)
	if result["isError"] != true {
		t.Errorf("unsupported tool should be a failure result, got %+v", result)
	}
	if resp.Error != nil {
		t.Errorf("should not return a JSON-RPC protocol error")
	}
}

func TestHandle_InitializedNotification(t *testing.T) {
	req := &rpcRequest{Method: "notifications/initialized"}
	resp, isNotif := handle(req, lineargql.New("", "", nil))
	if !isNotif || resp != nil {
		t.Errorf("initialized should be a no-op notification")
	}
}
