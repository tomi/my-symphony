package orchestrator

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/tomi/my-symphony/internal/agent"
	"github.com/tomi/my-symphony/internal/config"
	"github.com/tomi/my-symphony/internal/domain"
	"github.com/tomi/my-symphony/internal/logging"
	"github.com/tomi/my-symphony/internal/tracker"
)

// --- fakes ---

type fakeTracker struct {
	mu          sync.Mutex
	candidates  []domain.Issue
	candErr     error
	byIDs       map[string]domain.Issue
	byIDsErr    error
	byStates    []domain.Issue
	byStatesErr error
}

func (f *fakeTracker) FetchCandidateIssues(context.Context) ([]domain.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.candidates, f.candErr
}
func (f *fakeTracker) FetchIssuesByStates(context.Context, []string) ([]domain.Issue, error) {
	return f.byStates, f.byStatesErr
}
func (f *fakeTracker) FetchIssueStatesByIDs(_ context.Context, ids []string) ([]domain.Issue, error) {
	if f.byIDsErr != nil {
		return nil, f.byIDsErr
	}
	var out []domain.Issue
	for _, id := range ids {
		if iss, ok := f.byIDs[id]; ok {
			out = append(out, iss)
		}
	}
	return out, nil
}

type fakeWorkspace struct {
	cleaned []string
}

func (f *fakeWorkspace) Cleanup(_ context.Context, id string) error {
	f.cleaned = append(f.cleaned, id)
	return nil
}
func (f *fakeWorkspace) PathFor(id string) string { return "/ws/" + id }

// blockingRunner stays "running" until ctx is cancelled.
type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ domain.Issue, _ *int, _ func(agent.Event)) error {
	<-ctx.Done()
	return ctx.Err()
}

func testConfig(t *testing.T, raw map[string]any) *config.Config {
	t.Helper()
	if raw == nil {
		raw = map[string]any{}
	}
	tr, _ := raw["tracker"].(map[string]any)
	if tr == nil {
		tr = map[string]any{}
		raw["tracker"] = tr
	}
	tr["kind"] = "linear"
	tr["api_key"] = "k"
	tr["project_slug"] = "p"
	c, err := config.New(raw, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newTestOrch(t *testing.T, cfg *config.Config, ft *fakeTracker, fw *fakeWorkspace) *Orchestrator {
	t.Helper()
	factory := Factories{
		Tracker:   func(*config.Config) (tracker.Client, error) { return ft, nil },
		Workspace: func(*config.Config) Workspace { return fw },
		Runner: func(*config.Config, string, Workspace, tracker.Client, domain.Issue) agent.Runner {
			return blockingRunner{}
		},
	}
	o, err := New(cfg, "template", factory, logging.New())
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func iss(id, ident, state string, prio *int) domain.Issue {
	return domain.Issue{ID: id, Identifier: ident, Title: "t", State: state, Priority: prio}
}

func ptr(i int) *int { return &i }

// --- tests ---

func TestSortForDispatch(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	newer := time.Now()
	issues := []domain.Issue{
		{ID: "a", Identifier: "AB-3", Priority: nil, CreatedAt: &newer},
		{ID: "b", Identifier: "AB-2", Priority: ptr(1), CreatedAt: &newer},
		{ID: "c", Identifier: "AB-1", Priority: ptr(1), CreatedAt: &old},
		{ID: "d", Identifier: "AB-4", Priority: ptr(2), CreatedAt: &old},
	}
	sortForDispatch(issues)
	got := []string{issues[0].ID, issues[1].ID, issues[2].ID, issues[3].ID}
	want := []string{"c", "b", "d", "a"} // p1 oldest, p1 newer, p2, nil-priority last
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestBlockerRule(t *testing.T) {
	cfg := testConfig(t, nil)
	o := newTestOrch(t, cfg, &fakeTracker{}, &fakeWorkspace{})

	todo := iss("i1", "AB-1", "Todo", nil)
	todo.BlockedBy = []domain.BlockerRef{{State: sp("In Progress")}}
	if o.isEligible(todo) {
		t.Errorf("Todo with non-terminal blocker should be ineligible")
	}
	todo.BlockedBy = []domain.BlockerRef{{State: sp("Done")}}
	if !o.isEligible(todo) {
		t.Errorf("Todo with terminal blocker should be eligible")
	}
}

func TestRequiredLabels(t *testing.T) {
	cfg := testConfig(t, map[string]any{
		"tracker": map[string]any{"required_labels": []any{"ready"}},
	})
	o := newTestOrch(t, cfg, &fakeTracker{}, &fakeWorkspace{})
	i := iss("i1", "AB-1", "Todo", nil)
	if o.isEligible(i) {
		t.Errorf("missing required label should be ineligible")
	}
	i.Labels = []string{"ready"}
	if !o.isEligible(i) {
		t.Errorf("issue with required label should be eligible")
	}
}

func TestReconcile_TerminalStopsAndCleans(t *testing.T) {
	cfg := testConfig(t, nil)
	ft := &fakeTracker{byIDs: map[string]domain.Issue{
		"i1": iss("i1", "AB-1", "Done", nil),
	}}
	fw := &fakeWorkspace{}
	o := newTestOrch(t, cfg, ft, fw)
	o.rootCtx = context.Background()

	cancelled := false
	o.state.Running["i1"] = &RunningEntry{
		Identifier: "AB-1", Issue: iss("i1", "AB-1", "In Progress", nil),
		Cancel: func() { cancelled = true }, StartedAt: time.Now(),
	}
	o.reconcile()

	e := o.state.Running["i1"]
	if !e.Terminating || !e.CleanupOnExit || !cancelled {
		t.Errorf("terminal issue should terminate+cleanup, entry=%+v cancelled=%v", e, cancelled)
	}
	// Simulate the worker exit to trigger cleanup and release.
	o.onWorkerExit(WorkerExit{IssueID: "i1", Reason: ExitAbnormal})
	if len(fw.cleaned) != 1 || fw.cleaned[0] != "AB-1" {
		t.Errorf("workspace should be cleaned: %v", fw.cleaned)
	}
	if _, ok := o.state.Claimed["i1"]; ok {
		t.Errorf("claim should be released for terminal issue")
	}
}

func TestReconcile_NonActiveStopsNoCleanup(t *testing.T) {
	cfg := testConfig(t, nil)
	ft := &fakeTracker{byIDs: map[string]domain.Issue{
		"i1": iss("i1", "AB-1", "Backlog", nil), // neither active nor terminal
	}}
	fw := &fakeWorkspace{}
	o := newTestOrch(t, cfg, ft, fw)
	o.rootCtx = context.Background()
	o.state.Running["i1"] = &RunningEntry{
		Identifier: "AB-1", Issue: iss("i1", "AB-1", "In Progress", nil),
		Cancel: func() {}, StartedAt: time.Now(),
	}
	o.reconcile()
	e := o.state.Running["i1"]
	if !e.Terminating || e.CleanupOnExit {
		t.Errorf("non-active should terminate without cleanup: %+v", e)
	}
	o.onWorkerExit(WorkerExit{IssueID: "i1", Reason: ExitAbnormal})
	if len(fw.cleaned) != 0 {
		t.Errorf("no cleanup expected for non-active, got %v", fw.cleaned)
	}
}

func TestReconcile_ActiveUpdatesSnapshot(t *testing.T) {
	cfg := testConfig(t, nil)
	ft := &fakeTracker{byIDs: map[string]domain.Issue{
		"i1": iss("i1", "AB-1", "In Progress", nil),
	}}
	o := newTestOrch(t, cfg, ft, &fakeWorkspace{})
	o.rootCtx = context.Background()
	o.state.Running["i1"] = &RunningEntry{
		Identifier: "AB-1", Issue: iss("i1", "AB-1", "Todo", nil),
		Cancel: func() {}, StartedAt: time.Now(),
	}
	o.reconcile()
	if o.state.Running["i1"].Issue.State != "In Progress" {
		t.Errorf("state should be updated to In Progress")
	}
}

func TestReconcile_NoRunningIsNoop(t *testing.T) {
	cfg := testConfig(t, nil)
	ft := &fakeTracker{byIDsErr: context.Canceled}
	o := newTestOrch(t, cfg, ft, &fakeWorkspace{})
	o.rootCtx = context.Background()
	o.reconcile() // must not panic / call tracker
}

func TestStallDetection(t *testing.T) {
	cfg := testConfig(t, map[string]any{
		"claude": map[string]any{"stall_timeout_ms": 50},
	})
	o := newTestOrch(t, cfg, &fakeTracker{}, &fakeWorkspace{})
	o.rootCtx = context.Background()
	cancelled := false
	o.state.Running["i1"] = &RunningEntry{
		Identifier: "AB-1", Issue: iss("i1", "AB-1", "In Progress", nil),
		Cancel: func() { cancelled = true }, StartedAt: time.Now().Add(-time.Second),
	}
	o.reconcile()
	if !cancelled || !o.state.Running["i1"].Terminating {
		t.Errorf("stalled worker should be cancelled")
	}
}

func TestContinuationRetryAfterNormalExit(t *testing.T) {
	cfg := testConfig(t, nil)
	o := newTestOrch(t, cfg, &fakeTracker{}, &fakeWorkspace{})
	o.rootCtx = context.Background()
	o.state.Running["i1"] = &RunningEntry{
		Identifier: "AB-1", Issue: iss("i1", "AB-1", "In Progress", nil),
		Cancel: func() {}, StartedAt: time.Now(),
	}
	o.onWorkerExit(WorkerExit{IssueID: "i1", Reason: ExitNormal})
	r := o.state.RetryAttempts["i1"]
	if r == nil || r.Attempt != 1 {
		t.Fatalf("expected continuation retry attempt 1, got %+v", r)
	}
	delay := time.UnixMilli(r.DueAtMs).Sub(time.Now())
	if delay > 1500*time.Millisecond {
		t.Errorf("continuation delay should be ~1s, got %v", delay)
	}
	o.removeRetryTimer("i1")
}

func TestAbnormalRetryBackoff(t *testing.T) {
	cfg := testConfig(t, nil)
	o := newTestOrch(t, cfg, &fakeTracker{}, &fakeWorkspace{})
	o.rootCtx = context.Background()
	o.state.Running["i1"] = &RunningEntry{
		Identifier: "AB-1", Issue: iss("i1", "AB-1", "In Progress", nil),
		Cancel: func() {}, StartedAt: time.Now(), RetryAttempt: 1,
	}
	o.onWorkerExit(WorkerExit{IssueID: "i1", Reason: ExitAbnormal})
	r := o.state.RetryAttempts["i1"]
	if r == nil || r.Attempt != 2 {
		t.Fatalf("expected retry attempt 2, got %+v", r)
	}
	// attempt 2 -> 10000 * 2^(2-1) = 20000ms
	delay := time.UnixMilli(r.DueAtMs).Sub(time.Now())
	if delay < 18*time.Second || delay > 22*time.Second {
		t.Errorf("backoff for attempt 2 should be ~20s, got %v", delay)
	}
	o.removeRetryTimer("i1")
}

func TestBackoffCap(t *testing.T) {
	cfg := testConfig(t, map[string]any{
		"agent": map[string]any{"max_retry_backoff_ms": 15000},
	})
	o := newTestOrch(t, cfg, &fakeTracker{}, &fakeWorkspace{})
	o.rootCtx = context.Background()
	o.scheduleRetry("i1", 10, "AB-1", nil, sp("boom"), false)
	r := o.state.RetryAttempts["i1"]
	delay := time.UnixMilli(r.DueAtMs).Sub(time.Now())
	if delay > 16*time.Second {
		t.Errorf("backoff should be capped at 15s, got %v", delay)
	}
	o.removeRetryTimer("i1")
}

func TestRetryTimer_SlotExhaustionRequeues(t *testing.T) {
	cfg := testConfig(t, map[string]any{
		"agent": map[string]any{"max_concurrent_agents": 1},
	})
	ft := &fakeTracker{candidates: []domain.Issue{iss("i1", "AB-1", "Todo", nil)}}
	o := newTestOrch(t, cfg, ft, &fakeWorkspace{})
	o.rootCtx = context.Background()
	// Fill the single slot with another running issue.
	o.state.Running["other"] = &RunningEntry{Identifier: "AB-9", Issue: iss("other", "AB-9", "In Progress", nil), Cancel: func() {}, StartedAt: time.Now()}
	o.state.RetryAttempts["i1"] = &domain.RetryEntry{IssueID: "i1", Identifier: "AB-1", Attempt: 1}
	o.state.Claimed["i1"] = struct{}{}

	o.onRetryTimer("i1")
	r := o.state.RetryAttempts["i1"]
	if r == nil || r.Error == nil || *r.Error != "no available orchestrator slots" {
		t.Fatalf("expected slot-exhaustion requeue, got %+v", r)
	}
	if r.Attempt != 2 {
		t.Errorf("attempt should increment to 2, got %d", r.Attempt)
	}
	o.removeRetryTimer("i1")
}

func TestRetryTimer_MissingIssueReleasesClaim(t *testing.T) {
	cfg := testConfig(t, nil)
	ft := &fakeTracker{candidates: []domain.Issue{}} // issue no longer a candidate
	o := newTestOrch(t, cfg, ft, &fakeWorkspace{})
	o.rootCtx = context.Background()
	o.state.RetryAttempts["i1"] = &domain.RetryEntry{IssueID: "i1", Identifier: "AB-1", Attempt: 1}
	o.state.Claimed["i1"] = struct{}{}
	o.onRetryTimer("i1")
	if _, ok := o.state.Claimed["i1"]; ok {
		t.Errorf("claim should be released when issue absent")
	}
}

func TestDispatch_RespectsSlots(t *testing.T) {
	cfg := testConfig(t, map[string]any{
		"agent": map[string]any{"max_concurrent_agents": 2},
	})
	ft := &fakeTracker{candidates: []domain.Issue{
		iss("i1", "AB-1", "Todo", ptr(1)),
		iss("i2", "AB-2", "Todo", ptr(1)),
		iss("i3", "AB-3", "Todo", ptr(1)),
	}}
	o := newTestOrch(t, cfg, ft, &fakeWorkspace{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o.rootCtx = ctx
	o.onTick()
	if len(o.state.Running) != 2 {
		t.Fatalf("expected 2 dispatched (slot cap), got %d", len(o.state.Running))
	}
	if _, ok := o.state.Running["i3"]; ok {
		t.Errorf("i3 should not be dispatched over the slot cap")
	}
}

func TestSnapshotShape(t *testing.T) {
	cfg := testConfig(t, nil)
	o := newTestOrch(t, cfg, &fakeTracker{}, &fakeWorkspace{})
	o.rootCtx = context.Background()
	ts := time.Now()
	o.state.Running["i1"] = &RunningEntry{
		Identifier: "AB-1", Issue: iss("i1", "AB-1", "In Progress", nil),
		Cancel: func() {}, StartedAt: ts,
		Session: domain.LiveSession{SessionID: "s1", TurnCount: 3,
			ClaudeInputTokens: 10, ClaudeOutputTokens: 4, ClaudeTotalTokens: 14},
	}
	o.state.RetryAttempts["i2"] = &domain.RetryEntry{IssueID: "i2", Identifier: "AB-2", Attempt: 2, DueAtMs: time.Now().UnixMilli()}
	o.state.ClaudeTotals = domain.Totals{InputTokens: 10, OutputTokens: 4, TotalTokens: 14}

	snap := o.buildSnapshot()
	if snap.Counts.Running != 1 || snap.Counts.Retrying != 1 {
		t.Errorf("counts = %+v", snap.Counts)
	}
	if len(snap.Running) != 1 || snap.Running[0].SessionID != "s1" || snap.Running[0].TurnCount != 3 {
		t.Errorf("running row = %+v", snap.Running)
	}
	if snap.ClaudeTotals.SecondsRunning < 0 {
		t.Errorf("seconds_running should be a live aggregate")
	}
	o.removeRetryTimer("i2")
}

func TestTokenAggregationAcrossUpdates(t *testing.T) {
	cfg := testConfig(t, nil)
	o := newTestOrch(t, cfg, &fakeTracker{}, &fakeWorkspace{})
	o.rootCtx = context.Background()
	o.state.Running["i1"] = &RunningEntry{Identifier: "AB-1", Issue: iss("i1", "AB-1", "In Progress", nil), Cancel: func() {}, StartedAt: time.Now()}

	for i := 0; i < 3; i++ {
		o.onAgentUpdate(AgentUpdate{IssueID: "i1", Msg: agent.Event{
			Event: agent.EventTurnCompleted, Timestamp: time.Now(),
			Usage: &agent.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		}})
	}
	if o.state.ClaudeTotals.TotalTokens != 45 {
		t.Errorf("aggregate total = %d, want 45", o.state.ClaudeTotals.TotalTokens)
	}
	if o.state.Running["i1"].Session.ClaudeTotalTokens != 45 {
		t.Errorf("per-session total = %d, want 45", o.state.Running["i1"].Session.ClaudeTotalTokens)
	}
}

func TestRecentActivityCaptureAndCap(t *testing.T) {
	cfg := testConfig(t, nil)
	o := newTestOrch(t, cfg, &fakeTracker{}, &fakeWorkspace{})
	o.rootCtx = context.Background()
	o.state.Running["i1"] = &RunningEntry{Identifier: "AB-1", Issue: iss("i1", "AB-1", "In Progress", nil), Cancel: func() {}, StartedAt: time.Now()}

	// Events without a message add nothing to the activity feed.
	o.onAgentUpdate(AgentUpdate{IssueID: "i1", Msg: agent.Event{Event: agent.EventSessionStarted, Timestamp: time.Now()}})
	if got := len(o.state.Running["i1"].Session.RecentActivity); got != 0 {
		t.Fatalf("empty-message event should not append activity, got %d", got)
	}

	// Push more than the cap; oldest should be dropped, newest retained.
	total := maxRecentActivity + 10
	for i := 0; i < total; i++ {
		o.onAgentUpdate(AgentUpdate{IssueID: "i1", Msg: agent.Event{
			Event: agent.EventTurnCompleted, Timestamp: time.Now(), TurnID: strconv.Itoa(i),
			Message: "msg-" + strconv.Itoa(i),
		}})
	}
	act := o.state.Running["i1"].Session.RecentActivity
	if len(act) != maxRecentActivity {
		t.Fatalf("activity len = %d, want cap %d", len(act), maxRecentActivity)
	}
	// Newest-last ordering: last entry is the final message pushed.
	if want := "msg-" + strconv.Itoa(total-1); act[len(act)-1].Message != want {
		t.Errorf("last activity message = %q, want %q", act[len(act)-1].Message, want)
	}
	// Oldest surviving entry is total-cap (earlier ones dropped).
	if want := "msg-" + strconv.Itoa(total-maxRecentActivity); act[0].Message != want {
		t.Errorf("first activity message = %q, want %q", act[0].Message, want)
	}
}

func TestSnapshotActivityIsIndependentCopy(t *testing.T) {
	cfg := testConfig(t, nil)
	o := newTestOrch(t, cfg, &fakeTracker{}, &fakeWorkspace{})
	o.rootCtx = context.Background()
	o.state.Running["i1"] = &RunningEntry{Identifier: "AB-1", Issue: iss("i1", "AB-1", "In Progress", nil), Cancel: func() {}, StartedAt: time.Now()}
	o.onAgentUpdate(AgentUpdate{IssueID: "i1", Msg: agent.Event{
		Event: agent.EventTurnCompleted, Timestamp: time.Now(), Message: "hello",
	}})

	snap := o.buildSnapshot()
	if len(snap.Running) != 1 || len(snap.Running[0].Activity) != 1 || snap.Running[0].Activity[0].Message != "hello" {
		t.Fatalf("snapshot activity = %+v", snap.Running)
	}

	// Mutating the snapshot copy must not touch live orchestrator state.
	snap.Running[0].Activity[0].Message = "mutated"
	if live := o.state.Running["i1"].Session.RecentActivity[0].Message; live != "hello" {
		t.Errorf("live activity was mutated via snapshot: %q", live)
	}
}

func sp(s string) *string { return &s }
