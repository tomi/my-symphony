// Package status is the OPTIONAL terminal status surface (SPEC §13.4). It is a
// read-only renderer driven purely from orchestrator snapshots and is not
// required for correctness.
package status

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tomi/my-symphony/internal/domain"
)

// Provider is the orchestrator surface the status renderer consumes.
type Provider interface {
	Snapshot(timeout time.Duration) (domain.Snapshot, error)
}

// Surface periodically renders runtime status to a writer.
type Surface struct {
	provider Provider
	out      io.Writer
	interval time.Duration
}

// New builds a status Surface rendering every interval.
func New(provider Provider, out io.Writer, interval time.Duration) *Surface {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Surface{provider: provider, out: out, interval: interval}
}

// Run renders on a ticker until ctx is cancelled.
func (s *Surface) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap, err := s.provider.Snapshot(500 * time.Millisecond)
			if err != nil {
				continue
			}
			_, _ = io.WriteString(s.out, s.Render(snap))
		}
	}
}

// Render returns a human-readable status block for a snapshot (SPEC §13.4).
func (s *Surface) Render(snap domain.Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "── Symphony status @ %s ──\n", snap.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "running=%d retrying=%d tokens=%d seconds_running=%.1f\n",
		snap.Counts.Running, snap.Counts.Retrying, snap.ClaudeTotals.TotalTokens,
		snap.ClaudeTotals.SecondsRunning)

	if len(snap.Running) > 0 {
		b.WriteString("RUNNING:\n")
		for _, row := range snap.Running {
			fmt.Fprintf(&b, "  %-12s state=%-12s turns=%d session=%s tokens=%d\n",
				row.IssueIdentifier, row.State, row.TurnCount, row.SessionID, row.Tokens.TotalTokens)
		}
	}
	if len(snap.Retrying) > 0 {
		b.WriteString("RETRYING:\n")
		now := time.Now()
		for _, row := range snap.Retrying {
			due := time.UnixMilli(row.DueAt.UnixMilli()).Sub(now)
			reason := ""
			if row.Error != nil {
				reason = *row.Error
			}
			fmt.Fprintf(&b, "  %-12s attempt=%d due_in=%s %s\n",
				row.IssueIdentifier, row.Attempt, due.Truncate(time.Second), reason)
		}
	}
	if snap.RateLimits != nil {
		fmt.Fprintf(&b, "rate_limits=%v\n", snap.RateLimits)
	}
	return b.String()
}
