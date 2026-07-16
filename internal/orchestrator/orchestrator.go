package orchestrator

import (
	"context"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/tomi/my-symphony/internal/agent"
	"github.com/tomi/my-symphony/internal/config"
	"github.com/tomi/my-symphony/internal/domain"
	"github.com/tomi/my-symphony/internal/logging"
	"github.com/tomi/my-symphony/internal/tracker"
)

// Workspace is the subset of the workspace manager the orchestrator uses for
// cleanup and path lookup (SPEC §8.5, §8.6).
type Workspace interface {
	Cleanup(ctx context.Context, identifier string) error
	PathFor(identifier string) string
}

// Factories build effective components from the current config so a dynamic
// reload can re-apply them (SPEC §6.2). Keeping these as functions preserves the
// inward dependency direction: the orchestrator never imports concrete adapters.
type Factories struct {
	Tracker   func(cfg *config.Config) (tracker.Client, error)
	Workspace func(cfg *config.Config) Workspace
	Runner    func(cfg *config.Config, template string, ws Workspace, tr tracker.Client, issue domain.Issue) agent.Runner
}

// Orchestrator owns the single event loop and all scheduling state (SPEC §7).
type Orchestrator struct {
	cfg      *config.Config
	template string
	tracker  tracker.Client
	ws       Workspace
	factory  Factories
	logger   *logging.Logger

	events    chan Event
	state     *RuntimeState
	rootCtx   context.Context
	tickTimer *time.Timer

	notifyFn func(domain.Snapshot)
}

// New builds an Orchestrator. The tracker and workspace are constructed from the
// initial config via the factories.
func New(cfg *config.Config, template string, factory Factories, logger *logging.Logger) (*Orchestrator, error) {
	tr, err := factory.Tracker(cfg)
	if err != nil {
		return nil, err
	}
	return &Orchestrator{
		cfg:      cfg,
		template: template,
		tracker:  tr,
		ws:       factory.Workspace(cfg),
		factory:  factory,
		logger:   logger,
		events:   make(chan Event, 256),
		state:    newRuntimeState(cfg.Polling.IntervalMs, cfg.Agent.MaxConcurrent),
		rootCtx:  context.Background(),
	}, nil
}

// Events exposes the events channel for external observers (SPEC §2.4, §13.7).
func (o *Orchestrator) Events() chan<- Event { return o.events }

// SetNotifier registers an observer callback invoked with a fresh snapshot after
// state changes (used by the terminal status surface, SPEC §13.4).
func (o *Orchestrator) SetNotifier(fn func(domain.Snapshot)) { o.notifyFn = fn }

// Run executes startup and the event loop until ctx is cancelled (SPEC §16.1).
func (o *Orchestrator) Run(ctx context.Context) error {
	o.rootCtx = ctx

	// Startup validation is fatal (SPEC §6.3, §16.1).
	if err := o.cfg.ValidateDispatch(); err != nil {
		o.logger.Error("startup validation failed", "outcome", "failed", "error", err.Error())
		return err
	}

	o.startupTerminalCleanup()
	o.scheduleTick(0)

	for {
		select {
		case <-ctx.Done():
			o.shutdown()
			return nil
		case e := <-o.events:
			o.handle(e)
		}
	}
}

func (o *Orchestrator) handle(e Event) {
	switch ev := e.(type) {
	case TickEvent:
		o.onTick()
	case AgentUpdate:
		o.onAgentUpdate(ev)
	case WorkerExit:
		o.onWorkerExit(ev)
	case RetryTimerFired:
		o.onRetryTimer(ev.IssueID)
	case ReloadConfig:
		o.onReload(ev)
	case RefreshRequest:
		o.logger.Info("refresh requested", "outcome", "queued")
		o.scheduleTick(0)
	case SnapshotRequest:
		snap := o.buildSnapshot()
		select {
		case ev.Reply <- snap:
		default:
		}
	}
}

// post delivers an event to the loop, abandoning the send if the root context
// is cancelled to avoid goroutine leaks.
func (o *Orchestrator) post(e Event) {
	select {
	case o.events <- e:
	case <-o.rootCtx.Done():
	}
}

func (o *Orchestrator) scheduleTick(delay time.Duration) {
	if o.tickTimer != nil {
		o.tickTimer.Stop()
	}
	o.tickTimer = time.AfterFunc(delay, func() { o.post(TickEvent{}) })
}

// onTick implements the poll-and-dispatch tick (SPEC §16.2).
func (o *Orchestrator) onTick() {
	o.reconcile()

	defer o.scheduleTick(time.Duration(o.state.PollIntervalMs) * time.Millisecond)

	if err := o.cfg.ValidateDispatch(); err != nil {
		o.logger.Error("dispatch validation failed", "outcome", "failed", "error", err.Error())
		o.notify()
		return
	}

	issues, err := o.tracker.FetchCandidateIssues(o.rootCtx)
	if err != nil {
		o.logger.Error("candidate fetch failed", "outcome", "failed", "error", err.Error())
		o.notify()
		return
	}

	sortForDispatch(issues)
	for i := range issues {
		issue := issues[i]
		if o.state.globalAvailable() <= 0 {
			break
		}
		if _, running := o.state.Running[issue.ID]; running {
			continue
		}
		if _, claimed := o.state.Claimed[issue.ID]; claimed {
			continue
		}
		if !o.isEligible(issue) {
			continue
		}
		if o.state.runningCountByState(issue.State) >= o.cfg.PerStateLimit(issue.State) {
			continue
		}
		o.dispatchIssue(issue, nil)
	}
	o.notify()
}

// dispatchIssue spawns a worker and records the running entry (SPEC §16.4).
func (o *Orchestrator) dispatchIssue(issue domain.Issue, attempt *int) {
	ctx, cancel := context.WithCancel(o.rootCtx)
	entry := &RunningEntry{
		Identifier:    issue.Identifier,
		Issue:         issue,
		Cancel:        cancel,
		StartedAt:     time.Now().UTC(),
		RetryAttempt:  normalizeAttempt(attempt),
		WorkspacePath: o.ws.PathFor(issue.Identifier),
	}
	runner := o.factory.Runner(o.cfg, o.template, o.ws, o.tracker, issue)

	o.state.Running[issue.ID] = entry
	o.state.Claimed[issue.ID] = struct{}{}
	o.removeRetryTimer(issue.ID)

	id := issue.ID
	go func() {
		emit := func(ev agent.Event) { o.post(AgentUpdate{IssueID: id, Msg: ev}) }
		err := runner.Run(ctx, issue, attempt, emit)
		reason := ExitNormal
		if err != nil {
			reason = ExitAbnormal
		}
		o.post(WorkerExit{IssueID: id, Reason: reason, Err: err})
	}()

	o.logger.Info("dispatch", "issue_id", issue.ID, "issue_identifier", issue.Identifier,
		"outcome", "dispatched", "attempt", normalizeAttempt(attempt))
}

// onAgentUpdate updates live session fields, tokens, and rate limits (SPEC §7.3, §13.5).
func (o *Orchestrator) onAgentUpdate(ev AgentUpdate) {
	e := o.state.Running[ev.IssueID]
	if e == nil {
		return
	}
	m := ev.Msg
	ts := m.Timestamp
	e.Session.LastAgentTimestamp = &ts
	event := m.Event
	e.Session.LastAgentEvent = &event
	if m.Message != "" {
		e.Session.LastAgentMessage = logging.Truncate(m.Message)
		act := domain.AgentActivity{
			Timestamp: ts,
			Event:     event,
			TurnID:    m.TurnID,
			Message:   m.Message,
			Detail:    m.Detail,
		}
		// Per-step tokens are display-only; they are NOT accumulated into totals
		// (the terminal-result Usage below is the authoritative accounting).
		if m.StepUsage != nil {
			act.InputTokens = m.StepUsage.InputTokens
			act.OutputTokens = m.StepUsage.OutputTokens
		}
		e.Session.RecentActivity = appendActivity(e.Session.RecentActivity, act)
	}
	if m.SessionID != "" && m.SessionID != "pending" {
		e.Session.SessionID = m.SessionID
		e.Session.ThreadID = m.SessionID
	}
	if m.TurnID != "" {
		e.Session.TurnID = m.TurnID
		if n, err := strconv.Atoi(m.TurnID); err == nil && n > e.Session.TurnCount {
			e.Session.TurnCount = n
		}
	}
	if m.AgentPID != nil {
		e.Session.AgentPID = m.AgentPID
	}
	if m.Usage != nil {
		// Accumulate per-turn usage across the worker run and into global totals
		// (SPEC §13.5).
		e.Session.ClaudeInputTokens += m.Usage.InputTokens
		e.Session.ClaudeOutputTokens += m.Usage.OutputTokens
		e.Session.ClaudeTotalTokens += m.Usage.TotalTokens
		e.Session.LastReportedInputTokens = m.Usage.InputTokens
		e.Session.LastReportedOutputTokens = m.Usage.OutputTokens
		e.Session.LastReportedTotalTokens = m.Usage.TotalTokens
		o.state.ClaudeTotals.InputTokens += m.Usage.InputTokens
		o.state.ClaudeTotals.OutputTokens += m.Usage.OutputTokens
		o.state.ClaudeTotals.TotalTokens += m.Usage.TotalTokens
	}
	if m.RateLimits != nil {
		o.state.ClaudeRateLimits = m.RateLimits
	}
}

// maxRecentActivity bounds the per-session rolling agent-message history retained
// for observability. Oldest entries are dropped once the cap is reached.
const maxRecentActivity = 50

// appendActivity appends act to hist, keeping only the newest maxRecentActivity
// entries (oldest dropped). Ordering is newest-last.
func appendActivity(hist []domain.AgentActivity, act domain.AgentActivity) []domain.AgentActivity {
	hist = append(hist, act)
	if len(hist) > maxRecentActivity {
		hist = hist[len(hist)-maxRecentActivity:]
	}
	return hist
}

// onWorkerExit removes the running entry and schedules the appropriate retry
// (SPEC §16.6).
func (o *Orchestrator) onWorkerExit(ev WorkerExit) {
	entry := o.state.Running[ev.IssueID]
	if entry == nil {
		return
	}
	delete(o.state.Running, ev.IssueID)
	// Add run duration to cumulative ended-session seconds (SPEC §13.5).
	o.state.ClaudeTotals.SecondsRunning += time.Since(entry.StartedAt).Seconds()

	if entry.CleanupOnExit {
		if err := o.ws.Cleanup(o.rootCtx, entry.Identifier); err != nil {
			o.logger.Warn("workspace cleanup failed", "issue_identifier", entry.Identifier,
				"error", err.Error())
		}
	}

	if entry.ReleaseOnExit {
		delete(o.state.Claimed, ev.IssueID)
		o.logger.Info("issue released", "issue_id", ev.IssueID,
			"issue_identifier", entry.Identifier, "outcome", "released")
		o.notify()
		return
	}

	if ev.Reason == ExitNormal {
		o.state.Completed[ev.IssueID] = struct{}{}
		// Short continuation retry so we re-check whether the issue is still
		// active and needs another worker session (SPEC §7.1, §8.4).
		o.scheduleRetry(ev.IssueID, 1, entry.Identifier, entry.Issue.URL, nil, true)
		o.logger.Info("worker exit", "issue_id", ev.IssueID,
			"issue_identifier", entry.Identifier, "outcome", "completed")
	} else {
		msg := "worker exited"
		if ev.Err != nil {
			msg = "worker exited: " + ev.Err.Error()
		}
		o.scheduleRetry(ev.IssueID, entry.RetryAttempt+1, entry.Identifier, entry.Issue.URL,
			&msg, false)
		o.logger.Info("worker exit", "issue_id", ev.IssueID,
			"issue_identifier", entry.Identifier, "outcome", "retrying", "reason", logging.Truncate(msg))
	}
	o.notify()
}

// onRetryTimer re-fetches candidates and re-dispatches or releases (SPEC §16.7).
func (o *Orchestrator) onRetryTimer(id string) {
	entry := o.state.RetryAttempts[id]
	if entry == nil {
		return
	}
	delete(o.state.RetryAttempts, id)

	candidates, err := o.tracker.FetchCandidateIssues(o.rootCtx)
	if err != nil {
		msg := "retry poll failed"
		o.scheduleRetry(id, entry.Attempt+1, entry.Identifier, entry.URL, &msg, false)
		return
	}

	issue, found := findByID(candidates, id)
	if !found || !o.isEligible(issue) {
		delete(o.state.Claimed, id)
		o.notify()
		return
	}

	if o.state.globalAvailable() == 0 ||
		o.state.runningCountByState(issue.State) >= o.cfg.PerStateLimit(issue.State) {
		msg := "no available orchestrator slots"
		o.scheduleRetry(id, entry.Attempt+1, issue.Identifier, issue.URL, &msg, false)
		return
	}

	attempt := entry.Attempt
	o.dispatchIssue(issue, &attempt)
	o.notify()
}

// onReload swaps effective config, template, tracker, and workspace (SPEC §6.2).
func (o *Orchestrator) onReload(ev ReloadConfig) {
	tr, err := o.factory.Tracker(ev.Cfg)
	if err != nil {
		o.logger.Warn("config reload: tracker rebuild failed; keeping last good",
			"outcome", "failed", "error", err.Error())
		return
	}
	o.cfg = ev.Cfg
	o.template = ev.Tmpl
	o.tracker = tr
	o.ws = o.factory.Workspace(ev.Cfg)
	o.state.PollIntervalMs = ev.Cfg.Polling.IntervalMs
	o.state.MaxConcurrentAgents = ev.Cfg.Agent.MaxConcurrent
	o.logger.Info("config reloaded", "outcome", "completed",
		"poll_interval_ms", ev.Cfg.Polling.IntervalMs, "max_concurrent", ev.Cfg.Agent.MaxConcurrent)
	// Re-apply cadence to the next tick.
	o.scheduleTick(time.Duration(o.state.PollIntervalMs) * time.Millisecond)
}

// scheduleRetry creates or replaces a retry entry (SPEC §8.4).
func (o *Orchestrator) scheduleRetry(id string, attempt int, identifier string, url *string,
	errMsg *string, continuation bool) {
	o.removeRetryTimer(id)

	var delay time.Duration
	if continuation {
		delay = 1000 * time.Millisecond
	} else {
		if attempt < 1 {
			attempt = 1
		}
		ms := 10000 * math.Pow(2, float64(attempt-1))
		if capMs := float64(o.cfg.Agent.MaxRetryBackoffMs); ms > capMs {
			ms = capMs
		}
		delay = time.Duration(ms) * time.Millisecond
	}

	dueAt := time.Now().Add(delay)
	timer := time.AfterFunc(delay, func() { o.post(RetryTimerFired{IssueID: id}) })
	o.state.RetryAttempts[id] = &domain.RetryEntry{
		IssueID:    id,
		Identifier: identifier,
		Attempt:    attempt,
		DueAtMs:    dueAt.UnixMilli(),
		Timer:      timer,
		Error:      errMsg,
		URL:        url,
	}
	o.state.Claimed[id] = struct{}{}
}

func (o *Orchestrator) removeRetryTimer(id string) {
	if entry, ok := o.state.RetryAttempts[id]; ok {
		if entry.Timer != nil {
			entry.Timer.Stop()
		}
		delete(o.state.RetryAttempts, id)
	}
}

func (o *Orchestrator) shutdown() {
	if o.tickTimer != nil {
		o.tickTimer.Stop()
	}
	for _, e := range o.state.RetryAttempts {
		if e.Timer != nil {
			e.Timer.Stop()
		}
	}
	for _, e := range o.state.Running {
		e.Cancel()
	}
}

func (o *Orchestrator) notify() {
	if o.notifyFn != nil {
		o.notifyFn(o.buildSnapshot())
	}
}

func normalizeAttempt(attempt *int) int {
	if attempt == nil {
		return 0
	}
	return *attempt
}

func findByID(issues []domain.Issue, id string) (domain.Issue, bool) {
	for _, iss := range issues {
		if iss.ID == id {
			return iss, true
		}
	}
	return domain.Issue{}, false
}

// sortForDispatch sorts by priority asc (nil last), created_at oldest, then
// identifier (SPEC §8.2).
func sortForDispatch(issues []domain.Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		pa, pb := priorityKey(a.Priority), priorityKey(b.Priority)
		if pa != pb {
			return pa < pb
		}
		ca, cb := createdKey(a.CreatedAt), createdKey(b.CreatedAt)
		if ca != cb {
			return ca < cb
		}
		return a.Identifier < b.Identifier
	})
}

func priorityKey(p *int) int {
	if p == nil {
		return math.MaxInt
	}
	return *p
}

func createdKey(t *time.Time) int64 {
	if t == nil {
		return math.MaxInt64
	}
	return t.UnixNano()
}
