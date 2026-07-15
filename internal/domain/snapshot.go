package domain

import "time"

// Snapshot is an immutable projection of orchestrator runtime state built inside
// the loop for external observers (SPEC §13.3, §13.7.2). Observers never read
// live state directly; they request a Snapshot copy.
type Snapshot struct {
	GeneratedAt  time.Time      `json:"generated_at"`
	Counts       SnapshotCounts `json:"counts"`
	Running      []RunningRow   `json:"running"`
	Retrying     []RetryRow     `json:"retrying"`
	ClaudeTotals Totals         `json:"claude_totals"`
	RateLimits   any            `json:"rate_limits"`
}

// SnapshotCounts summarizes the number of running and retrying issues.
type SnapshotCounts struct {
	Running  int `json:"running"`
	Retrying int `json:"retrying"`
}

// TokenCounts is the per-row token projection.
type TokenCounts struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// RunningRow is a running-session row in a snapshot (SPEC §13.7.2).
type RunningRow struct {
	IssueID         string      `json:"issue_id"`
	IssueIdentifier string      `json:"issue_identifier"`
	IssueURL        *string     `json:"issue_url"`
	State           string      `json:"state"`
	SessionID       string      `json:"session_id"`
	TurnCount       int         `json:"turn_count"`
	LastEvent       string      `json:"last_event"`
	LastMessage     string      `json:"last_message"`
	StartedAt       time.Time   `json:"started_at"`
	LastEventAt     *time.Time  `json:"last_event_at"`
	Tokens          TokenCounts `json:"tokens"`
	// Activity is a bounded, newest-last history of recent agent messages for the
	// observability surfaces.
	Activity []AgentActivity `json:"activity"`
	// WorkspacePath is included for the per-issue detail endpoint.
	WorkspacePath string `json:"-"`
	RetryAttempt  int    `json:"-"`
}

// RetryRow is a retry-queue row in a snapshot (SPEC §13.7.2).
type RetryRow struct {
	IssueID         string    `json:"issue_id"`
	IssueIdentifier string    `json:"issue_identifier"`
	IssueURL        *string   `json:"issue_url"`
	Attempt         int       `json:"attempt"`
	DueAt           time.Time `json:"due_at"`
	Error           *string   `json:"error"`
}
