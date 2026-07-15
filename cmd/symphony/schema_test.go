package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tomi/my-symphony/internal/workflow"
)

// schemaNode is the subset of JSON Schema we walk to check documentation coverage.
type schemaNode struct {
	Properties           map[string]schemaNode `json:"properties"`
	AdditionalProperties json.RawMessage       `json:"additionalProperties"`
}

// TestWorkflowSchemaCoversExample guards that schema/workflow.schema.json is valid
// JSON and documents every key used by the shipped WORKFLOW.example.md. It is a
// cheap drift guard: adding a field to the example (or config) without documenting
// it in the schema fails here.
func TestWorkflowSchemaCoversExample(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "schema", "workflow.schema.json"))
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var root schemaNode
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}

	def, err := workflow.Load(filepath.Join("..", "..", "WORKFLOW.example.md"))
	if err != nil {
		t.Fatalf("load example: %v", err)
	}

	assertCovered(t, "", def.Config, root)
}

// assertCovered checks every key in the config object is declared in the schema
// node's properties, recursing into nested objects. Map-style nodes (those using
// `additionalProperties` for arbitrary keys, e.g. `states`) accept any key, so we
// recurse into the additionalProperties subschema instead of requiring each key.
func assertCovered(t *testing.T, path string, cfg map[string]any, node schemaNode) {
	t.Helper()
	for k, v := range cfg {
		here := k
		if path != "" {
			here = path + "." + k
		}
		prop, ok := node.Properties[k]
		if !ok {
			// A node with an object-schema additionalProperties accepts any key.
			if isObjectSchema(node.AdditionalProperties) {
				var sub schemaNode
				_ = json.Unmarshal(node.AdditionalProperties, &sub)
				if child, isMap := v.(map[string]any); isMap {
					assertCovered(t, here, child, sub)
				}
				continue
			}
			t.Errorf("front-matter key %q is not documented in workflow.schema.json", here)
			continue
		}
		if child, isMap := v.(map[string]any); isMap {
			assertCovered(t, here, child, prop)
		}
	}
}

// isObjectSchema reports whether an additionalProperties value is a schema object
// (arbitrary-key map) rather than absent or a boolean.
func isObjectSchema(rawMsg json.RawMessage) bool {
	if len(rawMsg) == 0 {
		return false
	}
	var obj map[string]any
	return json.Unmarshal(rawMsg, &obj) == nil
}
