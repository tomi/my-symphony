// Package agent defines the Agent Runner contract and the normalized runtime
// events forwarded to the orchestrator (SPEC §10.4, §10.7).
package agent

import (
	"context"
	"time"

	"github.com/tomi/my-symphony/internal/domain"
)

// Event kinds emitted upstream to the orchestrator (SPEC §10.4).
const (
	EventSessionStarted       = "session_started"
	EventStartupFailed        = "startup_failed"
	EventTurnCompleted        = "turn_completed"
	EventTurnFailed           = "turn_failed"
	EventTurnCancelled        = "turn_cancelled"
	EventTurnEndedWithError   = "turn_ended_with_error"
	EventTurnInputRequired    = "turn_input_required"
	EventApprovalAutoApproved = "approval_auto_approved"
	EventUnsupportedToolCall  = "unsupported_tool_call"
	EventNotification         = "notification"
	EventOtherMessage         = "other_message"
	EventMalformed            = "malformed"
)

// Usage holds per-turn token usage extracted from a terminal result event
// (SPEC §13.5).
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// Event is a normalized runtime event forwarded to the orchestrator (SPEC §10.4).
type Event struct {
	Event     string
	Timestamp time.Time
	AgentPID  *string
	SessionID string
	TurnID    string
	Message   string
	// Detail is the expandable per-step body for the observability feed (tool
	// inputs/results, thinking text). Empty for events without block content.
	Detail string
	// Usage is authoritative per-turn usage from a terminal result event; it is
	// accumulated into session and global totals.
	Usage *Usage
	// StepUsage is the per-message usage from an assistant event, surfaced for the
	// feed's per-step token display only. It is NEVER accumulated into totals
	// (that would double-count the terminal-result usage).
	StepUsage  *Usage
	RateLimits any
}

// Runner wraps workspace + prompt + Claude Code CLI client (SPEC §10.7, §16.5).
type Runner interface {
	// Run executes a full worker attempt for one issue, forwarding streamed
	// events via emit. It returns nil on a normal exit and an error on an
	// abnormal exit (SPEC §16.5, §16.6).
	Run(ctx context.Context, issue domain.Issue, attempt *int, emit func(Event)) error
}
