package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParse_FrontMatterAndBody(t *testing.T) {
	content := "---\ntracker:\n  kind: linear\n  project_slug: proj\n---\nDo the work for {{ issue.identifier }}.\n"
	def, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracker, ok := def.Config["tracker"].(map[string]any)
	if !ok {
		t.Fatalf("tracker not a map: %#v", def.Config["tracker"])
	}
	if tracker["kind"] != "linear" {
		t.Errorf("kind = %v, want linear", tracker["kind"])
	}
	if def.PromptTemplate != "Do the work for {{ issue.identifier }}." {
		t.Errorf("body = %q", def.PromptTemplate)
	}
}

func TestParse_NoFrontMatter(t *testing.T) {
	def, err := Parse("Just a prompt body\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(def.Config) != 0 {
		t.Errorf("expected empty config, got %#v", def.Config)
	}
	if def.PromptTemplate != "Just a prompt body" {
		t.Errorf("body = %q", def.PromptTemplate)
	}
}

func TestParse_NonMapFrontMatter(t *testing.T) {
	_, err := Parse("---\n- a\n- b\n---\nbody\n")
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeFrontMatterNotAMap {
		t.Fatalf("want front_matter_not_a_map, got %v", err)
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	_, err := Parse("---\nkey: : bad\n\t- x\n---\nbody\n")
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeWorkflowParseError {
		t.Fatalf("want workflow_parse_error, got %v", err)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.md"))
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeMissingWorkflowFile {
		t.Fatalf("want missing_workflow_file, got %v", err)
	}
}

func TestLoad_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(p, []byte("---\ntracker: {kind: linear}\n---\nhi"), 0o644); err != nil {
		t.Fatal(err)
	}
	def, err := Load(p)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if def.PromptTemplate != "hi" {
		t.Errorf("body = %q", def.PromptTemplate)
	}
}
