// Package domain holds the normalized data structures shared across Symphony's
// layers (SPEC §4). Types here are plain data with only normalization helpers;
// all scheduling behavior lives in the orchestrator.
package domain

import (
	"regexp"
	"strings"
	"time"
)

// BlockerRef is a reference to an issue that blocks another issue (SPEC §4.1.1).
type BlockerRef struct {
	ID         *string `json:"id"`
	Identifier *string `json:"identifier"`
	State      *string `json:"state"`
}

// Issue is the normalized issue record used by orchestration, prompt rendering,
// and observability (SPEC §4.1.1).
type Issue struct {
	ID          string       `json:"id"`
	Identifier  string       `json:"identifier"`
	Title       string       `json:"title"`
	Description *string      `json:"description"`
	Priority    *int         `json:"priority"`
	State       string       `json:"state"`
	BranchName  *string      `json:"branch_name"`
	URL         *string      `json:"url"`
	Labels      []string     `json:"labels"`
	BlockedBy   []BlockerRef `json:"blocked_by"`
	CreatedAt   *time.Time   `json:"created_at"`
	UpdatedAt   *time.Time   `json:"updated_at"`
	Assignee    *string      `json:"assignee"`
}

// Workspace is a filesystem workspace assigned to one issue identifier (SPEC §4.1.4).
type Workspace struct {
	Path       string
	Key        string
	CreatedNow bool
}

// AgentActivity is a single retained agent event kept for observability. A
// bounded, rolling history of these is surfaced on the dashboard so the agent's
// output can be watched live (SPEC §13.7).
type AgentActivity struct {
	Timestamp time.Time `json:"timestamp"`
	Event     string    `json:"event"`
	TurnID    string    `json:"turn_id"`
	Message   string    `json:"message"`
	// Detail is the expandable per-step body (tool inputs/results, thinking text)
	// folded away by default on the dashboard. Empty when there is nothing to fold.
	Detail string `json:"detail"`
	// InputTokens and OutputTokens are the per-message token counts for this step
	// (per-assistant-message granularity; zero for non-assistant steps). They are
	// for display only and are independent of the accumulated session/global totals.
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// LiveSession captures agent session metadata tracked while a subprocess runs
// (SPEC §4.1.6).
type LiveSession struct {
	SessionID                string
	ThreadID                 string
	TurnID                   string
	AgentPID                 *string
	LastAgentEvent           *string
	LastAgentTimestamp       *time.Time
	LastAgentMessage         string
	ClaudeInputTokens        int
	ClaudeOutputTokens       int
	ClaudeTotalTokens        int
	LastReportedInputTokens  int
	LastReportedOutputTokens int
	LastReportedTotalTokens  int
	TurnCount                int
	// RecentActivity is a bounded, newest-last history of agent messages retained
	// for the observability surfaces. Ephemeral: lives only while the session runs.
	RecentActivity []AgentActivity
}

// RetryEntry is scheduled retry state for an issue (SPEC §4.1.7).
type RetryEntry struct {
	IssueID    string
	Identifier string
	Attempt    int
	DueAtMs    int64
	Timer      *time.Timer
	Error      *string
	URL        *string
}

// Totals aggregates token and runtime accounting (SPEC §4.1.8, §13.5).
type Totals struct {
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	TotalTokens    int     `json:"total_tokens"`
	SecondsRunning float64 `json:"seconds_running"`
}

var wsKeyInvalid = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// WorkspaceKey derives a sanitized workspace directory name from an issue
// identifier by replacing every character outside [A-Za-z0-9._-] with "_"
// (SPEC §4.2, §9.5 invariant 3).
func WorkspaceKey(identifier string) string {
	return wsKeyInvalid.ReplaceAllString(identifier, "_")
}

// NormalizeState lowercases a tracker state for comparison (SPEC §4.2).
func NormalizeState(state string) string {
	return strings.ToLower(strings.TrimSpace(state))
}
