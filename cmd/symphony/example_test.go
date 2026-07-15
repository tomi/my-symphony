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
	cfg, err := config.New(def.Config, filepath.Dir(path))
	if err != nil {
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

	// The example's "AI Review" state override must resolve and its prompt file
	// must render under strict mode.
	if got := cfg.ModelForState("AI Review"); got != "opus" {
		t.Fatalf("AI Review model = %q, want opus", got)
	}
	reviewTmpl := cfg.PromptForState("AI Review", def.PromptTemplate)
	if reviewTmpl == def.PromptTemplate {
		t.Fatalf("AI Review prompt should differ from the default body")
	}
	rev := iss
	rev.State = "AI Review"
	if _, err := prompt.Render(reviewTmpl, rev, nil); err != nil {
		t.Fatalf("review render: %v", err)
	}
}
