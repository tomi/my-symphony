// Package workflow loads and splits WORKFLOW.md into front-matter config and a
// prompt template body (SPEC §5.1–§5.2).
package workflow

// Error is a typed error carrying a spec error code so callers can branch and
// operators can read a stable code (SPEC §5.5, §11.4).
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

// Spec error codes (SPEC §5.5).
const (
	CodeMissingWorkflowFile = "missing_workflow_file"
	CodeWorkflowParseError  = "workflow_parse_error"
	CodeFrontMatterNotAMap  = "workflow_front_matter_not_a_map"
	CodeTemplateParseError  = "template_parse_error"
	CodeTemplateRenderError = "template_render_error"
)
