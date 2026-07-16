package httpserver

import (
	"github.com/tomi/my-symphony/internal/domain"
)

// buildIssueDetail assembles the per-issue detail response from a snapshot
// (SPEC §13.7.2). Returns false when the identifier is unknown.
func buildIssueDetail(snap domain.Snapshot, identifier string) (map[string]any, bool) {
	for _, row := range snap.Running {
		if row.IssueIdentifier != identifier {
			continue
		}
		running := map[string]any{
			"session_id":   row.SessionID,
			"turn_count":   row.TurnCount,
			"state":        row.State,
			"started_at":   row.StartedAt,
			"last_event":   row.LastEvent,
			"last_message": row.LastMessage,
			"activity":     activityList(row.Activity),
			"tokens": map[string]any{
				"input_tokens":  row.Tokens.InputTokens,
				"output_tokens": row.Tokens.OutputTokens,
				"total_tokens":  row.Tokens.TotalTokens,
			},
		}
		if row.LastEventAt != nil {
			running["last_event_at"] = *row.LastEventAt
		} else {
			running["last_event_at"] = nil
		}
		return map[string]any{
			"issue_identifier": row.IssueIdentifier,
			"issue_id":         row.IssueID,
			"status":           "running",
			"workspace":        map[string]any{"path": row.WorkspacePath},
			"attempts": map[string]any{
				"current_retry_attempt": row.RetryAttempt,
			},
			"running":    running,
			"retry":      nil,
			"last_error": nil,
			"tracked":    map[string]any{},
		}, true
	}

	for _, row := range snap.Retrying {
		if row.IssueIdentifier != identifier {
			continue
		}
		retry := map[string]any{
			"attempt": row.Attempt,
			"due_at":  row.DueAt,
			"error":   row.Error,
		}
		return map[string]any{
			"issue_identifier": row.IssueIdentifier,
			"issue_id":         row.IssueID,
			"status":           "retrying",
			"running":          nil,
			"retry":            retry,
			"last_error":       row.Error,
			"tracked":          map[string]any{},
		}, true
	}

	return nil, false
}

// activityList projects retained agent activity into a JSON-friendly slice for
// the detail endpoint. Returns an empty (non-nil) slice so the field serializes
// as [] rather than null.
func activityList(activity []domain.AgentActivity) []map[string]any {
	out := make([]map[string]any, 0, len(activity))
	for _, a := range activity {
		out = append(out, map[string]any{
			"timestamp":     a.Timestamp,
			"event":         a.Event,
			"turn_id":       a.TurnID,
			"message":       a.Message,
			"detail":        a.Detail,
			"input_tokens":  a.InputTokens,
			"output_tokens": a.OutputTokens,
		})
	}
	return out
}
