package config

import (
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	c, err := New(map[string]any{}, "/tmp/wf")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if c.Polling.IntervalMs != 30000 {
		t.Errorf("interval = %d", c.Polling.IntervalMs)
	}
	if c.Agent.MaxConcurrent != 10 {
		t.Errorf("max concurrent = %d", c.Agent.MaxConcurrent)
	}
	if c.Agent.MaxTurns != 20 {
		t.Errorf("max turns = %d", c.Agent.MaxTurns)
	}
	if c.Agent.MaxRetryBackoffMs != 300000 {
		t.Errorf("backoff = %d", c.Agent.MaxRetryBackoffMs)
	}
	if c.Claude.Command != defaultClaudeCommand {
		t.Errorf("command = %q", c.Claude.Command)
	}
	if !c.Claude.ResumeAcrossTurns {
		t.Errorf("resume should default true")
	}
	if c.Claude.TurnTimeoutMs != 3600000 || c.Claude.ReadTimeoutMs != 5000 || c.Claude.StallTimeoutMs != 300000 {
		t.Errorf("claude timeouts wrong: %+v", c.Claude)
	}
	if c.Hooks.TimeoutMs != 60000 {
		t.Errorf("hooks timeout = %d", c.Hooks.TimeoutMs)
	}
	if got := []string{"Todo", "In Progress"}; !eq(c.Tracker.ActiveStates, got) {
		t.Errorf("active states = %v", c.Tracker.ActiveStates)
	}
	if len(c.Tracker.TerminalStates) != 5 {
		t.Errorf("terminal states = %v", c.Tracker.TerminalStates)
	}
}

func TestEnvResolution(t *testing.T) {
	t.Setenv("MY_KEY", "secret-token")
	c, _ := New(map[string]any{
		"tracker": map[string]any{"kind": "linear", "api_key": "$MY_KEY", "project_slug": "p"},
	}, "/tmp")
	if c.Tracker.APIKey != "secret-token" {
		t.Errorf("api key = %q", c.Tracker.APIKey)
	}
}

func TestEnvResolution_EmptyIsMissing(t *testing.T) {
	c, _ := New(map[string]any{
		"tracker": map[string]any{"kind": "linear", "api_key": "$UNSET_VAR_XYZ", "project_slug": "p"},
	}, "/tmp")
	if c.Tracker.APIKey != "" {
		t.Errorf("expected empty api key, got %q", c.Tracker.APIKey)
	}
	if err := c.ValidateDispatch(); err == nil {
		t.Errorf("expected validation failure for missing key")
	}
}

func TestWorkspaceRoot_RelativeResolvedToWorkflowDir(t *testing.T) {
	c, _ := New(map[string]any{
		"workspace": map[string]any{"root": "ws"},
	}, "/home/proj")
	want := filepath.Clean("/home/proj/ws")
	if c.Workspace.Root != want {
		t.Errorf("root = %q, want %q", c.Workspace.Root, want)
	}
}

func TestWorkspaceRoot_HomeExpansion(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	c, _ := New(map[string]any{
		"workspace": map[string]any{"root": "~/spaces"},
	}, "/tmp")
	if c.Workspace.Root != "/home/tester/spaces" {
		t.Errorf("root = %q", c.Workspace.Root)
	}
}

func TestPerStateMap_NormalizesAndFilters(t *testing.T) {
	c, _ := New(map[string]any{
		"agent": map[string]any{
			"max_concurrent_agents_by_state": map[string]any{
				"In Progress": 3,
				"Todo":        0,   // ignored (non-positive)
				"Bad":         "x", // ignored (non-numeric)
			},
		},
	}, "/tmp")
	if c.Agent.MaxConcurrentByState["in progress"] != 3 {
		t.Errorf("in progress = %d", c.Agent.MaxConcurrentByState["in progress"])
	}
	if _, ok := c.Agent.MaxConcurrentByState["todo"]; ok {
		t.Errorf("todo should be ignored")
	}
	if _, ok := c.Agent.MaxConcurrentByState["bad"]; ok {
		t.Errorf("bad should be ignored")
	}
}

func TestPerStateLimit_FallsBackToGlobal(t *testing.T) {
	c, _ := New(map[string]any{
		"agent": map[string]any{"max_concurrent_agents": 7,
			"max_concurrent_agents_by_state": map[string]any{"todo": 2}},
	}, "/tmp")
	if c.PerStateLimit("Todo") != 2 {
		t.Errorf("todo limit = %d", c.PerStateLimit("Todo"))
	}
	if c.PerStateLimit("In Progress") != 7 {
		t.Errorf("fallback limit = %d", c.PerStateLimit("In Progress"))
	}
}

func TestClaudeCommandPreserved(t *testing.T) {
	cmd := "claude -p --output-format stream-json --dangerously-skip-permissions"
	c, _ := New(map[string]any{"claude": map[string]any{"command": cmd}}, "/tmp")
	if c.Claude.Command != cmd {
		t.Errorf("command mangled: %q", c.Claude.Command)
	}
}

func TestValidateDispatch(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		code string
	}{
		{"missing kind", map[string]any{"tracker": map[string]any{"api_key": "k", "project_slug": "p"}}, CodeUnsupportedTrackerKind},
		{"bad kind", map[string]any{"tracker": map[string]any{"kind": "jira", "api_key": "k", "project_slug": "p"}}, CodeUnsupportedTrackerKind},
		{"missing key", map[string]any{"tracker": map[string]any{"kind": "linear", "project_slug": "p"}}, CodeMissingTrackerAPIKey},
		{"missing slug", map[string]any{"tracker": map[string]any{"kind": "linear", "api_key": "k"}}, CodeMissingTrackerProjectSlug},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(tc.raw, "/tmp")
			if err != nil {
				t.Fatalf("build error: %v", err)
			}
			err = c.ValidateDispatch()
			var e *Error
			if err == nil || !asError(err, &e) || e.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
		})
	}
}

func TestInvalidMaxTurns(t *testing.T) {
	_, err := New(map[string]any{"agent": map[string]any{"max_turns": 0}}, "/tmp")
	if err == nil {
		t.Fatalf("expected error for max_turns 0")
	}
}

func asError(err error, target **Error) bool {
	if e, ok := err.(*Error); ok {
		*target = e
		return true
	}
	return false
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
