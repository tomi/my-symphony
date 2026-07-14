package main

import (
	"path/filepath"
	"testing"

	"github.com/tomi/my-symphony/internal/config"
	"github.com/tomi/my-symphony/internal/domain"
	"github.com/tomi/my-symphony/internal/prompt"
	"github.com/tomi/my-symphony/internal/workflow"
)

// TestExampleWorkflowValid guards that the shipped WORKFLOW.example.md parses,
// builds a config, and renders under strict mode for both first-run and retry.
func TestExampleWorkflowValid(t *testing.T) {
	path := filepath.Join("..", "..", "WORKFLOW.example.md")
	def, err := workflow.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := config.New(def.Config, filepath.Dir(path)); err != nil {
		t.Fatalf("config: %v", err)
	}

	desc := "Fix the flaky test"
	prio := 2
	iss := domain.Issue{
		ID: "i1", Identifier: "AB-1", Title: "Flaky test", State: "Todo",
		Description: &desc, Priority: &prio, Labels: []string{"backend", "ci"},
	}
	if _, err := prompt.Render(def.PromptTemplate, iss, nil); err != nil {
		t.Fatalf("first-run render: %v", err)
	}
	a := 2
	if _, err := prompt.Render(def.PromptTemplate, iss, &a); err != nil {
		t.Fatalf("retry render: %v", err)
	}
}
