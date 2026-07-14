// Package prompt renders the WORKFLOW.md body as a strict Liquid template with
// `issue` and `attempt` inputs (SPEC §5.4, §12).
package prompt

import (
	"strings"

	"github.com/osteele/liquid"

	"github.com/tomi/my-symphony/internal/domain"
)

// DefaultPrompt is the minimal fallback used when the workflow body is empty
// (SPEC §5.4).
const DefaultPrompt = "You are working on an issue from Linear."

// Error mirrors the template error codes (SPEC §5.5).
type Error struct {
	Code    string
	Msg     string
	Wrapped error
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return e.Code
	}
	return e.Code + ": " + e.Msg
}
func (e *Error) Unwrap() error { return e.Wrapped }

const (
	CodeTemplateParseError  = "template_parse_error"
	CodeTemplateRenderError = "template_render_error"
)

var engine = liquid.NewEngine()

// Render renders the template with the given issue and attempt. It enforces
// strict semantics: an unknown top-level variable fails rendering (SPEC §5.4).
// osteele/liquid renders undefined variables as empty, so strictness is
// enforced by a pre-render reference check over the template's top-level
// variable references.
func Render(template string, issue domain.Issue, attempt *int) (string, error) {
	body := template
	if strings.TrimSpace(body) == "" {
		body = DefaultPrompt
	}

	bindings := buildBindings(issue, attempt)

	if err := checkUnknownVariables(body, bindings); err != nil {
		return "", err
	}

	tmpl, err := engine.ParseString(body)
	if err != nil {
		return "", &Error{Code: CodeTemplateParseError, Msg: err.Error(), Wrapped: err}
	}
	out, err := tmpl.Render(bindings)
	if err != nil {
		return "", &Error{Code: CodeTemplateRenderError, Msg: err.Error(), Wrapped: err}
	}
	return string(out), nil
}

// buildBindings produces the template context. Issue keys are stringified and
// nested labels/blockers preserved for iteration (SPEC §12.2).
func buildBindings(issue domain.Issue, attempt *int) map[string]any {
	blockers := make([]map[string]any, 0, len(issue.BlockedBy))
	for _, b := range issue.BlockedBy {
		blockers = append(blockers, map[string]any{
			"id":         derefString(b.ID),
			"identifier": derefString(b.Identifier),
			"state":      derefString(b.State),
		})
	}
	labels := make([]any, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		labels = append(labels, l)
	}

	issueMap := map[string]any{
		"id":          issue.ID,
		"identifier":  issue.Identifier,
		"title":       issue.Title,
		"description": derefString(issue.Description),
		"priority":    derefInt(issue.Priority),
		"state":       issue.State,
		"branch_name": derefString(issue.BranchName),
		"url":         derefString(issue.URL),
		"labels":      labels,
		"blocked_by":  blockers,
		"assignee":    derefString(issue.Assignee),
	}
	if issue.CreatedAt != nil {
		issueMap["created_at"] = issue.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	} else {
		issueMap["created_at"] = nil
	}
	if issue.UpdatedAt != nil {
		issueMap["updated_at"] = issue.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	} else {
		issueMap["updated_at"] = nil
	}

	bindings := map[string]any{"issue": issueMap}
	if attempt != nil {
		bindings["attempt"] = *attempt
	} else {
		bindings["attempt"] = nil
	}
	return bindings
}

func derefString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
func derefInt(i *int) any {
	if i == nil {
		return nil
	}
	return *i
}
