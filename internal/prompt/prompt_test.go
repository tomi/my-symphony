package prompt

import (
	"strings"
	"testing"

	"github.com/tomi/my-symphony/internal/domain"
)

func sampleIssue() domain.Issue {
	desc := "Fix the bug"
	prio := 2
	return domain.Issue{
		ID:          "id1",
		Identifier:  "ABC-1",
		Title:       "Bug",
		Description: &desc,
		Priority:    &prio,
		State:       "Todo",
		Labels:      []string{"backend", "urgent"},
		BlockedBy: []domain.BlockerRef{
			{Identifier: strptr("ABC-9"), State: strptr("Done")},
		},
	}
}

func strptr(s string) *string { return &s }

func TestRender_IssueFields(t *testing.T) {
	out, err := Render("Work {{ issue.identifier }}: {{ issue.title }} [{{ issue.state }}]", sampleIssue(), nil)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if out != "Work ABC-1: Bug [Todo]" {
		t.Errorf("got %q", out)
	}
}

func TestRender_LabelsAndBlockers(t *testing.T) {
	tmpl := "{% for l in issue.labels %}{{ l }},{% endfor %}|{% for b in issue.blocked_by %}{{ b.identifier }}={{ b.state }}{% endfor %}"
	out, err := Render(tmpl, sampleIssue(), nil)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if out != "backend,urgent,|ABC-9=Done" {
		t.Errorf("got %q", out)
	}
}

func TestRender_Attempt(t *testing.T) {
	a := 3
	out, err := Render("attempt={{ attempt }}", sampleIssue(), &a)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if out != "attempt=3" {
		t.Errorf("got %q", out)
	}
	// nil attempt renders empty
	out, err = Render("attempt={{ attempt }}", sampleIssue(), nil)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if strings.TrimSpace(out) != "attempt=" {
		t.Errorf("got %q", out)
	}
}

func TestRender_UnknownVariableFails(t *testing.T) {
	_, err := Render("Hello {{ bogus }}", sampleIssue(), nil)
	if err == nil {
		t.Fatalf("expected strict-mode failure for unknown variable")
	}
	var e *Error
	if !asErr(err, &e) || e.Code != CodeTemplateRenderError {
		t.Fatalf("want template_render_error, got %v", err)
	}
}

func TestRender_UnknownVariableInTagFails(t *testing.T) {
	_, err := Render("{% if mystery %}x{% endif %}", sampleIssue(), nil)
	if err == nil {
		t.Fatalf("expected failure for unknown variable in tag")
	}
}

func TestRender_KnownConditionalPasses(t *testing.T) {
	out, err := Render("{% if attempt %}retry{% else %}first{% endif %}", sampleIssue(), nil)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if out != "first" {
		t.Errorf("got %q", out)
	}
}

func TestRender_EmptyUsesDefault(t *testing.T) {
	out, err := Render("   ", sampleIssue(), nil)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if out != DefaultPrompt {
		t.Errorf("got %q", out)
	}
}

func TestRender_AssignLocalAllowed(t *testing.T) {
	out, err := Render("{% assign greeting = issue.title %}{{ greeting }}", sampleIssue(), nil)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if out != "Bug" {
		t.Errorf("got %q", out)
	}
}

func asErr(err error, target **Error) bool {
	if e, ok := err.(*Error); ok {
		*target = e
		return true
	}
	return false
}
