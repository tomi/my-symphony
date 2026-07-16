// Package config exposes a typed view over WORKFLOW.md front matter with all
// spec defaults baked in, plus $VAR/path resolution and dispatch validation
// (SPEC §6).
package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Error codes for config/tracker validation (SPEC §6.3, §11.4).
const (
	CodeUnsupportedTrackerKind    = "unsupported_tracker_kind"
	CodeMissingTrackerAPIKey      = "missing_tracker_api_key"
	CodeMissingTrackerProjectSlug = "missing_tracker_project_slug"
	CodeMissingClaudeCommand      = "missing_claude_command"
	CodeInvalidConfigValue        = "invalid_config_value"
	CodeMissingPromptFile         = "missing_prompt_file"
)

// Error is a typed config error carrying a spec code.
type Error struct {
	Code    string
	Msg     string
	Wrapped error
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return e.Code
	}
	return e.Code + ": " + e.Msg
}
func (e *Error) Unwrap() error { return e.Wrapped }

// TrackerConfig mirrors the `tracker` front-matter object (SPEC §5.3.1).
type TrackerConfig struct {
	Kind           string
	Endpoint       string
	APIKey         string // resolved from $VAR; empty means missing
	ProjectSlug    string
	RequiredLabels []string
	ActiveStates   []string
	TerminalStates []string
	// Assignee routes issues to this worker when set (SPEC §8.2). Empty means
	// no assignee restriction.
	Assignee string
}

// PollingConfig mirrors the `polling` object (SPEC §5.3.2).
type PollingConfig struct {
	IntervalMs int
}

// WorkspaceConfig mirrors the `workspace` object (SPEC §5.3.3).
type WorkspaceConfig struct {
	Root string // absolute, normalized
}

// HooksConfig mirrors the `hooks` object (SPEC §5.3.4).
type HooksConfig struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	TimeoutMs    int
}

// AgentConfig mirrors the `agent` object (SPEC §5.3.5).
type AgentConfig struct {
	MaxConcurrent        int
	MaxTurns             int
	MaxRetryBackoffMs    int
	MaxConcurrentByState map[string]int // keys normalized (lowercase)
}

// ClaudeConfig mirrors the `claude` object (SPEC §5.3.6).
type ClaudeConfig struct {
	Command           string
	Model             string // global default model; empty means the CLI default
	ReasoningEffort   string // global default effort; empty means the CLI default
	ResumeAcrossTurns bool
	TurnTimeoutMs     int
	ReadTimeoutMs     int
	StallTimeoutMs    int
}

// StateOverride mirrors one entry of the `states` map: per-status agent
// overrides applied when an issue in that tracker state is dispatched
// (SPEC §5.3.7). Empty/zero fields fall back to the global values.
type StateOverride struct {
	Model           string
	ReasoningEffort string
	PromptTemplate  string // resolved template body; empty means use the default
	MaxTurns        int    // 0 means fall back to agent.max_turns
}

// ServerConfig mirrors the extension `server` object (SPEC §13.7).
type ServerConfig struct {
	Port    int
	PortSet bool
}

// Config is the typed runtime view (SPEC §4.1.3, §6).
type Config struct {
	Tracker   TrackerConfig
	Polling   PollingConfig
	Workspace WorkspaceConfig
	Hooks     HooksConfig
	Agent     AgentConfig
	Claude    ClaudeConfig
	Server    ServerConfig

	// States holds per-tracker-status overrides, keyed by normalized (lowercase)
	// state name (SPEC §5.3.7, §8.3).
	States map[string]StateOverride

	// workflowDir is the directory containing WORKFLOW.md, used to resolve a
	// relative workspace.root (SPEC §6.1).
	workflowDir string
}

const defaultLinearEndpoint = "https://api.linear.app/graphql"
const defaultClaudeCommand = "claude -p --output-format stream-json --verbose"

// New builds a typed Config from a raw front-matter map. workflowDir is the
// directory containing the selected WORKFLOW.md (SPEC §6.1).
func New(raw map[string]any, workflowDir string) (*Config, error) {
	if raw == nil {
		raw = map[string]any{}
	}
	c := &Config{workflowDir: workflowDir}

	tracker := subMap(raw, "tracker")
	c.Tracker.Kind = strings.TrimSpace(asString(tracker["kind"]))
	c.Tracker.Endpoint = firstNonEmpty(asString(tracker["endpoint"]), defaultLinearEndpoint)
	c.Tracker.APIKey = resolveEnv(asString(tracker["api_key"]))
	c.Tracker.ProjectSlug = strings.TrimSpace(asString(tracker["project_slug"]))
	c.Tracker.Assignee = strings.TrimSpace(asString(tracker["assignee"]))
	c.Tracker.RequiredLabels = asStringList(tracker["required_labels"])
	c.Tracker.ActiveStates = defaultStringList(asStringList(tracker["active_states"]),
		[]string{"Todo", "In Progress"})
	c.Tracker.TerminalStates = defaultStringList(asStringList(tracker["terminal_states"]),
		[]string{"Closed", "Cancelled", "Canceled", "Duplicate", "Done"})

	polling := subMap(raw, "polling")
	c.Polling.IntervalMs = asIntDefault(polling["interval_ms"], 30000)

	workspace := subMap(raw, "workspace")
	root := asString(workspace["root"])
	c.Workspace.Root = c.resolveWorkspaceRoot(root)

	hooks := subMap(raw, "hooks")
	c.Hooks.AfterCreate = asString(hooks["after_create"])
	c.Hooks.BeforeRun = asString(hooks["before_run"])
	c.Hooks.AfterRun = asString(hooks["after_run"])
	c.Hooks.BeforeRemove = asString(hooks["before_remove"])
	timeoutMs, ok := asIntStrict(hooks["timeout_ms"], 60000)
	if !ok || timeoutMs < 0 {
		return nil, &Error{Code: CodeInvalidConfigValue, Msg: "hooks.timeout_ms must be a non-negative integer"}
	}
	c.Hooks.TimeoutMs = timeoutMs

	agent := subMap(raw, "agent")
	c.Agent.MaxConcurrent = asIntDefault(agent["max_concurrent_agents"], 10)
	maxTurns, ok := asIntStrict(agent["max_turns"], 20)
	if !ok || maxTurns <= 0 {
		return nil, &Error{Code: CodeInvalidConfigValue, Msg: "agent.max_turns must be a positive integer"}
	}
	c.Agent.MaxTurns = maxTurns
	c.Agent.MaxRetryBackoffMs = asIntDefault(agent["max_retry_backoff_ms"], 300000)
	c.Agent.MaxConcurrentByState = normalizeStateMap(agent["max_concurrent_agents_by_state"])

	claude := subMap(raw, "claude")
	c.Claude.Command = firstNonEmpty(asString(claude["command"]), defaultClaudeCommand)
	c.Claude.Model = strings.TrimSpace(asString(claude["model"]))
	c.Claude.ReasoningEffort = strings.TrimSpace(asString(claude["reasoning_effort"]))
	if err := validateEffort(c.Claude.ReasoningEffort); err != nil {
		return nil, err
	}
	c.Claude.ResumeAcrossTurns = asBoolDefault(claude["resume_across_turns"], true)
	c.Claude.TurnTimeoutMs = asIntDefault(claude["turn_timeout_ms"], 3600000)
	c.Claude.ReadTimeoutMs = asIntDefault(claude["read_timeout_ms"], 5000)
	c.Claude.StallTimeoutMs = asIntDefault(claude["stall_timeout_ms"], 300000)

	states, err := c.parseStateOverrides(raw["states"])
	if err != nil {
		return nil, err
	}
	c.States = states

	server := subMap(raw, "server")
	if v, present := server["port"]; present {
		if p, ok := asIntStrict(v, 0); ok {
			c.Server.Port = p
			c.Server.PortSet = true
		}
	}

	return c, nil
}

// resolveWorkspaceRoot applies ~ and $VAR expansion (path values only),
// resolves relative paths against the workflow directory, and normalizes to an
// absolute path (SPEC §5.3.3, §6.1).
func (c *Config) resolveWorkspaceRoot(root string) string {
	if strings.TrimSpace(root) == "" {
		root = filepath.Join(os.TempDir(), "symphony_workspaces")
	} else {
		root = expandPath(root)
	}
	if !filepath.IsAbs(root) {
		base := c.workflowDir
		if base == "" {
			base, _ = os.Getwd()
		}
		root = filepath.Join(base, root)
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return filepath.Clean(root)
}

// ValidateDispatch performs the dispatch preflight checks (SPEC §6.3).
func (c *Config) ValidateDispatch() error {
	if c.Tracker.Kind == "" {
		return &Error{Code: CodeUnsupportedTrackerKind, Msg: "tracker.kind is required"}
	}
	if c.Tracker.Kind != "linear" {
		return &Error{Code: CodeUnsupportedTrackerKind, Msg: "unsupported tracker.kind: " + c.Tracker.Kind}
	}
	if c.Tracker.APIKey == "" {
		return &Error{Code: CodeMissingTrackerAPIKey, Msg: "tracker.api_key is missing or resolved empty"}
	}
	if c.Tracker.ProjectSlug == "" {
		return &Error{Code: CodeMissingTrackerProjectSlug, Msg: "tracker.project_slug is required for linear"}
	}
	if strings.TrimSpace(c.Claude.Command) == "" {
		return &Error{Code: CodeMissingClaudeCommand, Msg: "claude.command must be present and non-empty"}
	}
	return nil
}

// PerStateLimit returns the concurrency limit for a tracker state, falling back
// to the global limit when no override is present (SPEC §8.3).
func (c *Config) PerStateLimit(state string) int {
	if v, ok := c.Agent.MaxConcurrentByState[normState(state)]; ok {
		return v
	}
	return c.Agent.MaxConcurrent
}

// ModelForState returns the model for a tracker state: the per-state override if
// set, else the global claude.model (SPEC §5.3.7, §8.3).
func (c *Config) ModelForState(state string) string {
	if ov, ok := c.States[normState(state)]; ok && ov.Model != "" {
		return ov.Model
	}
	return c.Claude.Model
}

// ReasoningEffortForState returns the effort level for a tracker state: the
// per-state override if set, else the global claude.reasoning_effort.
func (c *Config) ReasoningEffortForState(state string) string {
	if ov, ok := c.States[normState(state)]; ok && ov.ReasoningEffort != "" {
		return ov.ReasoningEffort
	}
	return c.Claude.ReasoningEffort
}

// MaxTurnsForState returns the turn budget for a tracker state: the per-state
// override if set, else the global agent.max_turns.
func (c *Config) MaxTurnsForState(state string) int {
	if ov, ok := c.States[normState(state)]; ok && ov.MaxTurns > 0 {
		return ov.MaxTurns
	}
	return c.Agent.MaxTurns
}

// PromptForState returns the prompt template body for a tracker state: the
// per-state override body if set, else the provided default (the WORKFLOW.md body).
func (c *Config) PromptForState(state, def string) string {
	if ov, ok := c.States[normState(state)]; ok && ov.PromptTemplate != "" {
		return ov.PromptTemplate
	}
	return def
}

// HasPromptOverride reports whether a tracker state binds its own prompt body
// (e.g. a review status pointing at prompts/review.md) rather than falling back
// to the default WORKFLOW.md body. Used to surface the dispatched "mode" in logs
// and to keep continuation turns aligned with that mode (SPEC §5.3.7).
func (c *Config) HasPromptOverride(state string) bool {
	ov, ok := c.States[normState(state)]
	return ok && ov.PromptTemplate != ""
}

// normState normalizes a tracker state name for map lookups (SPEC §8.3).
func normState(state string) string {
	return strings.ToLower(strings.TrimSpace(state))
}

// validEffortLevels are the effort levels accepted by the Claude Code CLI
// `--effort` flag (SPEC §5.3.7).
var validEffortLevels = map[string]bool{
	"low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// validateEffort rejects a non-empty effort value outside the CLI's accepted set.
func validateEffort(e string) error {
	if e == "" || validEffortLevels[strings.ToLower(e)] {
		return nil
	}
	return &Error{Code: CodeInvalidConfigValue,
		Msg: "reasoning_effort must be one of low, medium, high, xhigh, max"}
}

// parseStateOverrides builds the per-status override map from the raw `states`
// front-matter object. Keys are normalized (lowercase); non-map entries are
// ignored. A referenced prompt file that cannot be read is a fatal config error
// (SPEC §5.3.7, §6.1).
func (c *Config) parseStateOverrides(v any) (map[string]StateOverride, error) {
	out := map[string]StateOverride{}
	m, ok := v.(map[string]any)
	if !ok {
		return out, nil
	}
	for k, val := range m {
		sm, ok := val.(map[string]any)
		if !ok {
			continue
		}
		var ov StateOverride
		ov.Model = strings.TrimSpace(asString(sm["model"]))
		ov.ReasoningEffort = strings.TrimSpace(asString(sm["reasoning_effort"]))
		if err := validateEffort(ov.ReasoningEffort); err != nil {
			return nil, err
		}
		if mt, ok := asIntStrict(sm["max_turns"], 0); ok && mt > 0 {
			ov.MaxTurns = mt
		}
		if p := strings.TrimSpace(asString(sm["prompt"])); p != "" {
			body, err := c.readPromptFile(p)
			if err != nil {
				return nil, err
			}
			ov.PromptTemplate = body
		}
		out[normState(k)] = ov
	}
	return out, nil
}

// readPromptFile resolves a prompt path (with ~/$VAR expansion, relative to the
// workflow directory) and reads its body (SPEC §5.3.7, §6.1).
func (c *Config) readPromptFile(p string) (string, error) {
	path := expandPath(p)
	if !filepath.IsAbs(path) {
		base := c.workflowDir
		if base == "" {
			base, _ = os.Getwd()
		}
		path = filepath.Join(base, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", &Error{Code: CodeMissingPromptFile, Msg: p, Wrapped: err}
	}
	return strings.TrimSpace(string(data)), nil
}
