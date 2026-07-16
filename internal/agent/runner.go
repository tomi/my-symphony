package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/tomi/my-symphony/internal/domain"
	"github.com/tomi/my-symphony/internal/logging"
	"github.com/tomi/my-symphony/internal/prompt"
)

// WorkspaceManager is the subset of the workspace manager the runner needs
// (SPEC §16.5).
type WorkspaceManager interface {
	CreateForIssue(ctx context.Context, identifier string) (domain.Workspace, error)
	RunHook(ctx context.Context, name, path string) error
	AssertLaunchCWD(identifier, cwd string) error
}

// TrackerRefresher re-fetches issue state between turns (SPEC §16.5).
type TrackerRefresher interface {
	FetchIssueStatesByIDs(ctx context.Context, ids []string) ([]domain.Issue, error)
}

// TurnResult reports per-turn token usage (SPEC §13.5).
type TurnResult struct {
	Usage *Usage
}

// Backend abstracts the Claude Code CLI client so the runner can be tested with
// a fake (SPEC §10.7). The session handle is opaque to the runner.
type Backend interface {
	StartSession(workspace, identifier, title string) any
	RunTurn(ctx context.Context, session any, prompt string, emit func(Event)) (*TurnResult, error)
	StopSession(session any)
}

// DefaultRunner implements Runner over a workspace manager, agent backend, and
// tracker refresher (SPEC §16.5).
type DefaultRunner struct {
	ws             WorkspaceManager
	backend        Backend
	tracker        TrackerRefresher
	template       string
	promptOverride bool
	activeStates   map[string]bool
	maxTurns       int
	logger         *logging.Logger
}

// RunnerConfig configures a DefaultRunner.
type RunnerConfig struct {
	Workspace WorkspaceManager
	Backend   Backend
	Tracker   TrackerRefresher
	Template  string
	// PromptOverride is true when Template is a per-state prompt override (e.g. a
	// review prompt) rather than the default implementation body. Continuation
	// turns stay aligned with that mode instead of falling back to
	// implementation-flavored guidance (SPEC §5.3.7, §16.5).
	PromptOverride bool
	ActiveStates   []string
	MaxTurns       int
	Logger         *logging.Logger
}

// NewRunner builds a DefaultRunner.
func NewRunner(cfg RunnerConfig) *DefaultRunner {
	active := map[string]bool{}
	for _, s := range cfg.ActiveStates {
		active[domain.NormalizeState(s)] = true
	}
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 1
	}
	return &DefaultRunner{
		ws:             cfg.Workspace,
		backend:        cfg.Backend,
		tracker:        cfg.Tracker,
		template:       cfg.Template,
		promptOverride: cfg.PromptOverride,
		activeStates:   active,
		maxTurns:       maxTurns,
		logger:         cfg.Logger,
	}
}

// Run executes the worker attempt (SPEC §16.5). It returns nil on normal exit.
func (r *DefaultRunner) Run(ctx context.Context, issue domain.Issue, attempt *int, emit func(Event)) error {
	ws, err := r.ws.CreateForIssue(ctx, issue.Identifier)
	if err != nil {
		return fmt.Errorf("workspace error: %w", err)
	}

	// before_run failure is fatal to the attempt (SPEC §9.4).
	if err := r.ws.RunHook(ctx, "before_run", ws.Path); err != nil {
		return fmt.Errorf("before_run hook error: %w", err)
	}

	// Enforce cwd/root invariants right before agent launch (SPEC §9.5).
	if err := r.ws.AssertLaunchCWD(issue.Identifier, ws.Path); err != nil {
		r.afterRun(ctx, ws.Path)
		return fmt.Errorf("workspace safety error: %w", err)
	}

	session := r.backend.StartSession(ws.Path, issue.Identifier, issue.Title)

	// The per-state prompt/effort/turns are bound to the state this worker was
	// dispatched for. Continuation turns reuse that binding, so once the issue
	// moves to a *different* active state we must stop and let the orchestrator
	// re-dispatch a fresh worker with that state's prompt (SPEC §5.3.7, §16.5).
	dispatchState := domain.NormalizeState(issue.State)

	turn := 1
	for {
		p, err := r.buildTurnPrompt(issue, attempt, turn)
		if err != nil {
			r.backend.StopSession(session)
			r.afterRun(ctx, ws.Path)
			return fmt.Errorf("prompt error: %w", err)
		}

		if _, err := r.backend.RunTurn(ctx, session, p, emit); err != nil {
			r.backend.StopSession(session)
			r.afterRun(ctx, ws.Path)
			return fmt.Errorf("agent turn error: %w", err)
		}

		// Re-fetch tracker state to decide whether to continue (SPEC §16.5).
		refreshed, err := r.tracker.FetchIssueStatesByIDs(ctx, []string{issue.ID})
		if err != nil {
			r.backend.StopSession(session)
			r.afterRun(ctx, ws.Path)
			return fmt.Errorf("issue state refresh error: %w", err)
		}
		if len(refreshed) > 0 {
			issue = mergeState(issue, refreshed[0])
		}

		if !r.isActive(issue.State) {
			break
		}
		// A transition to another active state needs a different prompt than the
		// one bound to this worker; exit so a fresh dispatch picks it up.
		if domain.NormalizeState(issue.State) != dispatchState {
			break
		}
		if turn >= r.maxTurns {
			break
		}
		turn++
	}

	r.backend.StopSession(session)
	r.afterRun(ctx, ws.Path)
	return nil
}

// buildTurnPrompt renders the full task prompt on the first turn and concise
// continuation guidance on later turns (SPEC §7.1, §10.2).
func (r *DefaultRunner) buildTurnPrompt(issue domain.Issue, attempt *int, turn int) (string, error) {
	if turn == 1 {
		return prompt.Render(r.template, issue, attempt)
	}
	var b strings.Builder
	if r.promptOverride {
		// This worker is bound to a per-state prompt (e.g. review). Keep the
		// continuation aligned with that task rather than implying implementation
		// work (SPEC §5.3.7).
		fmt.Fprintf(&b, "Continue with %s: %s\n\n", issue.Identifier, issue.Title)
		b.WriteString("The issue is still active. Keep following your original task instructions above. ")
		b.WriteString("Do not restart from scratch; build on what you have already done in this session.")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "Continue working on %s: %s\n\n", issue.Identifier, issue.Title)
	b.WriteString("The issue is still active. Review what remains to be done and continue. ")
	b.WriteString("Do not restart from scratch; build on the work already in this session.")
	return b.String(), nil
}

func (r *DefaultRunner) isActive(state string) bool {
	return r.activeStates[domain.NormalizeState(state)]
}

// afterRun runs the after_run hook best-effort (failure logged and ignored,
// SPEC §9.4).
func (r *DefaultRunner) afterRun(ctx context.Context, path string) {
	if err := r.ws.RunHook(ctx, "after_run", path); err != nil && r.logger != nil {
		r.logger.Warn("after_run hook failed", "workspace", path, "error", err.Error())
	}
}

// mergeState updates the issue's state and labels from a refreshed minimal
// issue while keeping the richer original fields.
func mergeState(orig, refreshed domain.Issue) domain.Issue {
	if refreshed.State != "" {
		orig.State = refreshed.State
	}
	orig.Labels = refreshed.Labels
	return orig
}

var _ Runner = (*DefaultRunner)(nil)
