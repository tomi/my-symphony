package orchestrator

import (
	"sort"
	"time"

	"github.com/tomi/my-symphony/internal/domain"
)

// buildSnapshot produces an immutable projection of runtime state for observers
// (SPEC §13.3, §13.7.2). Runtime seconds are a live aggregate: cumulative
// ended-session seconds plus active-session elapsed (SPEC §13.5).
func (o *Orchestrator) buildSnapshot() domain.Snapshot {
	now := time.Now().UTC()
	snap := domain.Snapshot{
		GeneratedAt:  now,
		ClaudeTotals: o.state.ClaudeTotals,
		RateLimits:   o.state.ClaudeRateLimits,
	}

	live := o.state.ClaudeTotals.SecondsRunning
	for id, e := range o.state.Running {
		live += now.Sub(e.StartedAt).Seconds()
		row := domain.RunningRow{
			IssueID:         id,
			IssueIdentifier: e.Identifier,
			IssueURL:        e.Issue.URL,
			State:           e.Issue.State,
			SessionID:       e.Session.SessionID,
			TurnCount:       e.Session.TurnCount,
			StartedAt:       e.StartedAt,
			LastMessage:     e.Session.LastAgentMessage,
			WorkspacePath:   e.WorkspacePath,
			RetryAttempt:    e.RetryAttempt,
			Tokens: domain.TokenCounts{
				InputTokens:  e.Session.ClaudeInputTokens,
				OutputTokens: e.Session.ClaudeOutputTokens,
				TotalTokens:  e.Session.ClaudeTotalTokens,
			},
		}
		if e.Session.LastAgentEvent != nil {
			row.LastEvent = *e.Session.LastAgentEvent
		}
		if e.Session.LastAgentTimestamp != nil {
			t := *e.Session.LastAgentTimestamp
			row.LastEventAt = &t
		}
		if len(e.Session.RecentActivity) > 0 {
			// Copy into a fresh slice so observers never share the live backing array.
			row.Activity = append([]domain.AgentActivity(nil), e.Session.RecentActivity...)
		}
		snap.Running = append(snap.Running, row)
	}
	snap.ClaudeTotals.SecondsRunning = live

	for id, r := range o.state.RetryAttempts {
		snap.Retrying = append(snap.Retrying, domain.RetryRow{
			IssueID:         id,
			IssueIdentifier: r.Identifier,
			IssueURL:        r.URL,
			Attempt:         r.Attempt,
			DueAt:           time.UnixMilli(r.DueAtMs).UTC(),
			Error:           r.Error,
		})
	}

	// Deterministic ordering for stable rendering/tests.
	sort.Slice(snap.Running, func(i, j int) bool {
		return snap.Running[i].IssueIdentifier < snap.Running[j].IssueIdentifier
	})
	sort.Slice(snap.Retrying, func(i, j int) bool {
		return snap.Retrying[i].IssueIdentifier < snap.Retrying[j].IssueIdentifier
	})

	snap.Counts = domain.SnapshotCounts{
		Running:  len(snap.Running),
		Retrying: len(snap.Retrying),
	}
	return snap
}
