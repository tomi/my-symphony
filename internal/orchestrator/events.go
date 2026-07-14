package orchestrator

import (
	"github.com/tomi/my-symphony/internal/agent"
	"github.com/tomi/my-symphony/internal/config"
	"github.com/tomi/my-symphony/internal/domain"
)

// Event is a message delivered on the orchestrator's single events channel
// (SPEC §16). Each handler runs to completion on the loop goroutine.
type Event interface{ isEvent() }

// ExitReason distinguishes normal from abnormal worker exits (SPEC §16.6).
type ExitReason int

const (
	ExitNormal ExitReason = iota
	ExitAbnormal
)

// TickEvent fires on the poll cadence (SPEC §8.1).
type TickEvent struct{}

// AgentUpdate carries a streamed agent event for a running issue (SPEC §16.4, §10.4).
type AgentUpdate struct {
	IssueID string
	Msg     agent.Event
}

// WorkerExit reports worker completion (SPEC §16.6).
type WorkerExit struct {
	IssueID string
	Reason  ExitReason
	Err     error
}

// RetryTimerFired signals a retry timer elapsed (SPEC §16.7).
type RetryTimerFired struct{ IssueID string }

// ReloadConfig swaps the effective config and template (SPEC §6.2).
type ReloadConfig struct {
	Cfg  *config.Config
	Tmpl string
}

// SnapshotRequest asks the loop to build an immutable snapshot (SPEC §13.3).
type SnapshotRequest struct {
	Reply chan<- domain.Snapshot
}

// RefreshRequest triggers an immediate poll+reconcile (SPEC §13.7.2).
type RefreshRequest struct{}

func (TickEvent) isEvent()       {}
func (AgentUpdate) isEvent()     {}
func (WorkerExit) isEvent()      {}
func (RetryTimerFired) isEvent() {}
func (ReloadConfig) isEvent()    {}
func (SnapshotRequest) isEvent() {}
func (RefreshRequest) isEvent()  {}
