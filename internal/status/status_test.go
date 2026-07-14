package status

import (
	"strings"
	"testing"
	"time"

	"github.com/tomi/my-symphony/internal/domain"
)

func TestRender(t *testing.T) {
	s := New(nil, nil, time.Second)
	snap := domain.Snapshot{
		GeneratedAt: time.Now().UTC(),
		Counts:      domain.SnapshotCounts{Running: 1, Retrying: 1},
		Running: []domain.RunningRow{{
			IssueIdentifier: "AB-1", State: "In Progress", TurnCount: 2, SessionID: "s1",
			Tokens: domain.TokenCounts{TotalTokens: 8},
		}},
		Retrying: []domain.RetryRow{{
			IssueIdentifier: "AB-2", Attempt: 1, DueAt: time.Now().Add(5 * time.Second),
			Error: strptr("no available orchestrator slots"),
		}},
		ClaudeTotals: domain.Totals{TotalTokens: 8, SecondsRunning: 12.5},
	}
	out := s.Render(snap)
	for _, want := range []string{"AB-1", "In Progress", "AB-2", "no available orchestrator slots", "running=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}
}

func strptr(s string) *string { return &s }
