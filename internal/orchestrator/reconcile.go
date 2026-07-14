package orchestrator

import (
	"strings"
	"time"

	"github.com/tomi/my-symphony/internal/domain"
)

// reconcile runs stall detection then tracker-state refresh (SPEC §16.3, §8.5).
// It always runs first on every tick.
func (o *Orchestrator) reconcile() {
	// Part A — stall detection (SPEC §8.5).
	if o.cfg.Claude.StallTimeoutMs > 0 {
		stall := time.Duration(o.cfg.Claude.StallTimeoutMs) * time.Millisecond
		now := time.Now()
		for id, e := range o.state.Running {
			if e.Terminating {
				continue
			}
			if now.Sub(e.lastActivity()) > stall {
				o.logger.Warn("stall detected", "issue_id", id,
					"issue_identifier", e.Identifier, "outcome", "retrying")
				// Cancel; the abnormal worker exit schedules a retry.
				e.Terminating = true
				e.Cancel()
			}
		}
	}

	// Part B — tracker state refresh (SPEC §16.3, §8.5).
	ids := make([]string, 0, len(o.state.Running))
	for id, e := range o.state.Running {
		if e.Terminating {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return
	}

	refreshed, err := o.tracker.FetchIssueStatesByIDs(o.rootCtx, ids)
	if err != nil {
		// Keep workers running and retry next tick (SPEC §8.5).
		o.logger.Debug("state refresh failed; keep workers", "error", err.Error())
		return
	}

	for _, issue := range refreshed {
		e := o.state.Running[issue.ID]
		if e == nil || e.Terminating {
			continue
		}
		switch {
		case o.isTerminalState(issue.State):
			// Terminal: terminate worker and clean workspace (SPEC §8.5).
			o.terminateRunning(issue.ID, true, true)
		case o.isActiveState(issue.State):
			// Active: update the in-memory issue snapshot (SPEC §8.5).
			e.Issue.State = issue.State
			e.Issue.Labels = issue.Labels
		default:
			// Neither active nor terminal: terminate without cleanup (SPEC §8.5).
			o.terminateRunning(issue.ID, false, true)
		}
	}
}

// terminateRunning marks a running worker for termination; the actual removal,
// cleanup, and release happen when the worker exits (SPEC §16.3).
func (o *Orchestrator) terminateRunning(id string, cleanup, release bool) {
	e := o.state.Running[id]
	if e == nil || e.Terminating {
		return
	}
	e.Terminating = true
	e.CleanupOnExit = cleanup
	e.ReleaseOnExit = release
	e.Cancel()
}

// startupTerminalCleanup removes workspaces for issues already terminal (SPEC §8.6).
func (o *Orchestrator) startupTerminalCleanup() {
	issues, err := o.tracker.FetchIssuesByStates(o.rootCtx, o.cfg.Tracker.TerminalStates)
	if err != nil {
		o.logger.Warn("startup terminal cleanup fetch failed; continuing",
			"outcome", "failed", "error", err.Error())
		return
	}
	for _, iss := range issues {
		if err := o.ws.Cleanup(o.rootCtx, iss.Identifier); err != nil {
			o.logger.Warn("startup workspace cleanup failed",
				"issue_identifier", iss.Identifier, "error", err.Error())
		}
	}
}

// isEligible applies candidate selection rules except the running/claimed/slot
// checks (SPEC §8.2). Used by both tick dispatch and retry handling.
func (o *Orchestrator) isEligible(issue domain.Issue) bool {
	if issue.ID == "" || issue.Identifier == "" || issue.Title == "" || issue.State == "" {
		return false
	}
	if !o.isActiveState(issue.State) || o.isTerminalState(issue.State) {
		return false
	}
	if !o.matchesAssignee(issue) {
		return false
	}
	if !o.hasRequiredLabels(issue) {
		return false
	}
	// Blocker rule for Todo state (SPEC §8.2).
	if domain.NormalizeState(issue.State) == "todo" {
		for _, b := range issue.BlockedBy {
			if !o.isTerminalBlocker(b) {
				return false
			}
		}
	}
	return true
}

func (o *Orchestrator) matchesAssignee(issue domain.Issue) bool {
	want := strings.TrimSpace(o.cfg.Tracker.Assignee)
	if want == "" {
		return true
	}
	if issue.Assignee == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(*issue.Assignee), want)
}

func (o *Orchestrator) hasRequiredLabels(issue domain.Issue) bool {
	have := map[string]bool{}
	for _, l := range issue.Labels {
		have[strings.ToLower(strings.TrimSpace(l))] = true
	}
	for _, req := range o.cfg.Tracker.RequiredLabels {
		norm := strings.ToLower(strings.TrimSpace(req))
		// A blank configured label matches no issue (SPEC §5.3.1).
		if norm == "" {
			return false
		}
		if !have[norm] {
			return false
		}
	}
	return true
}

// isTerminalBlocker reports whether a blocker is in a terminal state. A blocker
// with unknown/nil state is treated as non-terminal (blocking) for safety.
func (o *Orchestrator) isTerminalBlocker(b domain.BlockerRef) bool {
	if b.State == nil {
		return false
	}
	return o.isTerminalState(*b.State)
}

func (o *Orchestrator) isActiveState(state string) bool {
	return containsNormalized(o.cfg.Tracker.ActiveStates, state)
}

func (o *Orchestrator) isTerminalState(state string) bool {
	return containsNormalized(o.cfg.Tracker.TerminalStates, state)
}

func containsNormalized(list []string, state string) bool {
	target := domain.NormalizeState(state)
	for _, s := range list {
		if domain.NormalizeState(s) == target {
			return true
		}
	}
	return false
}
