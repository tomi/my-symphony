// Package orchestrator is the single-authority event loop that owns all
// scheduling state: dispatch, reconciliation, and retries (SPEC §7, §8, §16).
package orchestrator

import (
	"context"
	"time"

	"github.com/tomi/my-symphony/internal/domain"
)

// RunningEntry is the orchestrator's record for one in-flight worker (SPEC §16.4).
type RunningEntry struct {
	Identifier    string
	Issue         domain.Issue
	Cancel        context.CancelFunc
	StartedAt     time.Time
	Session       domain.LiveSession
	RetryAttempt  int
	WorkspacePath string

	// Termination bookkeeping so reconciliation and worker-exit cooperate
	// without double-processing (SPEC §16.3, §16.6).
	Terminating   bool
	CleanupOnExit bool
	ReleaseOnExit bool
}

// lastActivity returns the timestamp used for stall detection (SPEC §8.5).
func (e *RunningEntry) lastActivity() time.Time {
	if e.Session.LastAgentTimestamp != nil {
		return *e.Session.LastAgentTimestamp
	}
	return e.StartedAt
}

// RuntimeState is the single authoritative in-memory state (SPEC §4.1.8).
type RuntimeState struct {
	PollIntervalMs      int
	MaxConcurrentAgents int
	Running             map[string]*RunningEntry
	Claimed             map[string]struct{}
	RetryAttempts       map[string]*domain.RetryEntry
	Completed           map[string]struct{}
	// ClaudeTotals holds cumulative tokens and cumulative ended-session seconds.
	// Live snapshot seconds add active-session elapsed on top (SPEC §13.5).
	ClaudeTotals     domain.Totals
	ClaudeRateLimits any
}

func newRuntimeState(pollIntervalMs, maxConcurrent int) *RuntimeState {
	return &RuntimeState{
		PollIntervalMs:      pollIntervalMs,
		MaxConcurrentAgents: maxConcurrent,
		Running:             map[string]*RunningEntry{},
		Claimed:             map[string]struct{}{},
		RetryAttempts:       map[string]*domain.RetryEntry{},
		Completed:           map[string]struct{}{},
	}
}

// runningCountByState counts running entries whose current tracked state matches
// (SPEC §8.3).
func (s *RuntimeState) runningCountByState(state string) int {
	target := domain.NormalizeState(state)
	n := 0
	for _, e := range s.Running {
		if domain.NormalizeState(e.Issue.State) == target {
			n++
		}
	}
	return n
}

// globalAvailable returns the number of free global slots (SPEC §8.3).
func (s *RuntimeState) globalAvailable() int {
	free := s.MaxConcurrentAgents - len(s.Running)
	if free < 0 {
		return 0
	}
	return free
}
