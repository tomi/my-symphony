// Package claude implements the Claude Code CLI stream-json client, running one
// subprocess per turn (SPEC §10.1–§10.6).
package claude

// Error is a normalized agent error category (SPEC §10.6).
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

// Normalized error categories (SPEC §10.6).
const (
	CodeClaudeNotFound    = "claude_not_found"
	CodeInvalidWorkspace  = "invalid_workspace_cwd"
	CodeResponseTimeout   = "response_timeout"
	CodeTurnTimeout       = "turn_timeout"
	CodePortExit          = "port_exit"
	CodeResponseError     = "response_error"
	CodeTurnFailed        = "turn_failed"
	CodeTurnCancelled     = "turn_cancelled"
	CodeTurnInputRequired = "turn_input_required"
	CodeStreamCorruption  = "stream_corruption"
)
