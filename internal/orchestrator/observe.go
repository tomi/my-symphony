package orchestrator

import (
	"errors"
	"time"

	"github.com/tomi/my-symphony/internal/domain"
)

// Snapshot error modes (SPEC §13.3).
var (
	ErrSnapshotTimeout     = errors.New("snapshot timeout")
	ErrSnapshotUnavailable = errors.New("snapshot unavailable")
)

// Snapshot requests an immutable runtime snapshot from the loop, returning the
// §13.3 timeout/unavailable error modes. Safe to call from other goroutines.
func (o *Orchestrator) Snapshot(timeout time.Duration) (domain.Snapshot, error) {
	reply := make(chan domain.Snapshot, 1)
	select {
	case o.events <- SnapshotRequest{Reply: reply}:
	case <-o.rootCtx.Done():
		return domain.Snapshot{}, ErrSnapshotUnavailable
	default:
		return domain.Snapshot{}, ErrSnapshotUnavailable
	}
	select {
	case snap := <-reply:
		return snap, nil
	case <-time.After(timeout):
		return domain.Snapshot{}, ErrSnapshotTimeout
	case <-o.rootCtx.Done():
		return domain.Snapshot{}, ErrSnapshotUnavailable
	}
}

// RequestRefresh queues an immediate poll+reconcile (SPEC §13.7.2). Returns
// false if the loop is not accepting events.
func (o *Orchestrator) RequestRefresh() bool {
	select {
	case o.events <- RefreshRequest{}:
		return true
	case <-o.rootCtx.Done():
		return false
	default:
		return false
	}
}
