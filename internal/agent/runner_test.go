package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/tomi/my-symphony/internal/domain"
)

type fakeWS struct {
	beforeRunErr error
	hooks        []string
	launchErr    error
}

func (f *fakeWS) CreateForIssue(_ context.Context, id string) (domain.Workspace, error) {
	return domain.Workspace{Path: "/ws/" + id, Key: id}, nil
}
func (f *fakeWS) RunHook(_ context.Context, name, _ string) error {
	f.hooks = append(f.hooks, name)
	if name == "before_run" {
		return f.beforeRunErr
	}
	return nil
}
func (f *fakeWS) AssertLaunchCWD(_, _ string) error { return f.launchErr }

type fakeBackend struct {
	prompts []string
	runErr  error
	started int
	stopped int
}

func (f *fakeBackend) StartSession(_, _, _ string) any { f.started++; return &struct{}{} }
func (f *fakeBackend) RunTurn(_ context.Context, _ any, prompt string, emit func(Event)) (*TurnResult, error) {
	f.prompts = append(f.prompts, prompt)
	emit(Event{Event: EventTurnCompleted})
	return &TurnResult{Usage: &Usage{InputTokens: 1, TotalTokens: 1}}, f.runErr
}
func (f *fakeBackend) StopSession(any) { f.stopped++ }

type fakeRefresher struct {
	states []string // returned per successive call
	call   int
	err    error
}

func (f *fakeRefresher) FetchIssueStatesByIDs(_ context.Context, ids []string) ([]domain.Issue, error) {
	if f.err != nil {
		return nil, f.err
	}
	st := "Done"
	if f.call < len(f.states) {
		st = f.states[f.call]
	}
	f.call++
	return []domain.Issue{{ID: ids[0], Identifier: "AB-1", State: st}}, nil
}

func newRunner(ws WorkspaceManager, be Backend, tr TrackerRefresher, tmpl string, maxTurns int) *DefaultRunner {
	return NewRunner(RunnerConfig{
		Workspace: ws, Backend: be, Tracker: tr, Template: tmpl,
		ActiveStates: []string{"Todo", "In Progress"}, MaxTurns: maxTurns,
	})
}

func TestRunner_SingleTurnWhenIssueBecomesTerminal(t *testing.T) {
	ws := &fakeWS{}
	be := &fakeBackend{}
	tr := &fakeRefresher{states: []string{"Done"}}
	r := newRunner(ws, be, tr, "Task {{ issue.identifier }}", 5)

	err := r.Run(context.Background(), domain.Issue{ID: "i1", Identifier: "AB-1", Title: "t", State: "In Progress"}, nil, func(Event) {})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(be.prompts) != 1 {
		t.Errorf("expected 1 turn, got %d", len(be.prompts))
	}
	if be.prompts[0] != "Task AB-1" {
		t.Errorf("first prompt = %q", be.prompts[0])
	}
	if be.stopped != 1 {
		t.Errorf("session should be stopped")
	}
}

func TestRunner_ContinuesUpToMaxTurns(t *testing.T) {
	ws := &fakeWS{}
	be := &fakeBackend{}
	tr := &fakeRefresher{states: []string{"In Progress", "In Progress", "In Progress"}}
	r := newRunner(ws, be, tr, "Task {{ issue.identifier }}", 3)

	err := r.Run(context.Background(), domain.Issue{ID: "i1", Identifier: "AB-1", Title: "t", State: "In Progress"}, nil, func(Event) {})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(be.prompts) != 3 {
		t.Fatalf("expected 3 turns (max), got %d", len(be.prompts))
	}
	// First turn full prompt; continuation turns differ.
	if be.prompts[0] != "Task AB-1" {
		t.Errorf("turn 1 = %q", be.prompts[0])
	}
	if be.prompts[1] == be.prompts[0] {
		t.Errorf("continuation turn should not resend original prompt")
	}
}

func TestRunner_BeforeRunFailureAborts(t *testing.T) {
	ws := &fakeWS{beforeRunErr: errors.New("hook boom")}
	be := &fakeBackend{}
	r := newRunner(ws, be, &fakeRefresher{}, "x", 1)
	err := r.Run(context.Background(), domain.Issue{ID: "i1", Identifier: "AB-1", Title: "t", State: "Todo"}, nil, func(Event) {})
	if err == nil {
		t.Fatalf("expected before_run failure")
	}
	if be.started != 0 {
		t.Errorf("session should not start after before_run failure")
	}
}

func TestRunner_PromptErrorAborts(t *testing.T) {
	ws := &fakeWS{}
	be := &fakeBackend{}
	r := newRunner(ws, be, &fakeRefresher{}, "{{ unknown_var }}", 1)
	err := r.Run(context.Background(), domain.Issue{ID: "i1", Identifier: "AB-1", Title: "t", State: "Todo"}, nil, func(Event) {})
	if err == nil {
		t.Fatalf("expected prompt render error")
	}
	if be.stopped != 1 {
		t.Errorf("session should be stopped on prompt error")
	}
}

func TestRunner_TurnErrorAborts(t *testing.T) {
	ws := &fakeWS{}
	be := &fakeBackend{runErr: errors.New("turn boom")}
	r := newRunner(ws, be, &fakeRefresher{}, "x", 3)
	err := r.Run(context.Background(), domain.Issue{ID: "i1", Identifier: "AB-1", Title: "t", State: "Todo"}, nil, func(Event) {})
	if err == nil {
		t.Fatalf("expected turn error")
	}
}

func TestRunner_RefreshErrorAborts(t *testing.T) {
	ws := &fakeWS{}
	be := &fakeBackend{}
	tr := &fakeRefresher{err: errors.New("refresh boom")}
	r := newRunner(ws, be, tr, "x", 3)
	err := r.Run(context.Background(), domain.Issue{ID: "i1", Identifier: "AB-1", Title: "t", State: "Todo"}, nil, func(Event) {})
	if err == nil {
		t.Fatalf("expected refresh error")
	}
}
