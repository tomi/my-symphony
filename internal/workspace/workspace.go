// Package workspace manages per-issue workspace directories, lifecycle hooks,
// and the filesystem safety invariants (SPEC §9).
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tomi/my-symphony/internal/domain"
	"github.com/tomi/my-symphony/internal/logging"
)

// Hooks holds the workspace lifecycle hook scripts (SPEC §5.3.4).
type Hooks struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	TimeoutMs    int
}

// Manager creates, reuses, and cleans per-issue workspaces (SPEC §9).
type Manager struct {
	root   string
	hooks  Hooks
	logger *logging.Logger
}

// New builds a workspace Manager. root must already be absolute (SPEC §9.1).
func New(root string, hooks Hooks, logger *logging.Logger) *Manager {
	return &Manager{root: root, hooks: hooks, logger: logger}
}

// Root returns the effective workspace root.
func (m *Manager) Root() string { return m.root }

// PathFor returns the per-issue workspace path for an identifier (SPEC §9.1).
func (m *Manager) PathFor(identifier string) string {
	return filepath.Join(m.root, domain.WorkspaceKey(identifier))
}

// CreateForIssue ensures the per-issue workspace exists, running after_create
// on first creation (SPEC §9.2).
func (m *Manager) CreateForIssue(ctx context.Context, identifier string) (domain.Workspace, error) {
	key := domain.WorkspaceKey(identifier)
	path := filepath.Join(m.root, key)

	// Enforce safety invariant 2 before any FS write (SPEC §9.5).
	if err := m.assertContained(path); err != nil {
		return domain.Workspace{}, err
	}

	createdNow := false
	info, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		if !info.IsDir() {
			// Existing non-directory at the workspace path: fail with a clear
			// error (SPEC §17.2 handle-safely policy).
			return domain.Workspace{}, fmt.Errorf("workspace path %q exists and is not a directory", path)
		}
	case errors.Is(statErr, os.ErrNotExist):
		if err := os.MkdirAll(m.root, 0o755); err != nil {
			return domain.Workspace{}, fmt.Errorf("create workspace root: %w", err)
		}
		if err := os.Mkdir(path, 0o755); err != nil {
			if errors.Is(err, os.ErrExist) {
				// Raced with another creator; treat as reuse.
				createdNow = false
			} else {
				return domain.Workspace{}, fmt.Errorf("create workspace: %w", err)
			}
		} else {
			createdNow = true
		}
	default:
		return domain.Workspace{}, fmt.Errorf("stat workspace: %w", statErr)
	}

	ws := domain.Workspace{Path: path, Key: key, CreatedNow: createdNow}

	if createdNow && strings.TrimSpace(m.hooks.AfterCreate) != "" {
		if err := m.RunHook(ctx, "after_create", path); err != nil {
			// after_create failure is fatal; remove the partially-created dir
			// (SPEC §9.3, §9.4).
			_ = os.RemoveAll(path)
			return domain.Workspace{}, fmt.Errorf("after_create hook failed: %w", err)
		}
	}
	return ws, nil
}

// RunHook executes a named hook in the workspace directory via bash -lc, bounded
// by hooks.timeout_ms (SPEC §9.4). A hook with no script is a no-op.
func (m *Manager) RunHook(ctx context.Context, name, path string) error {
	script := m.scriptFor(name)
	if strings.TrimSpace(script) == "" {
		return nil
	}

	timeout := time.Duration(m.hooks.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if m.logger != nil {
		m.logger.Info("workspace hook start", "hook", name, "workspace", path)
	}

	cmd := exec.CommandContext(hookCtx, "bash", "-lc", script)
	cmd.Dir = path
	out, err := cmd.CombinedOutput()
	if err != nil {
		if hookCtx.Err() == context.DeadlineExceeded {
			if m.logger != nil {
				m.logger.Warn("workspace hook timeout", "hook", name, "workspace", path,
					"output", logging.Truncate(string(out)))
			}
			return fmt.Errorf("hook %s timed out", name)
		}
		if m.logger != nil {
			m.logger.Warn("workspace hook failed", "hook", name, "workspace", path,
				"error", err.Error(), "output", logging.Truncate(string(out)))
		}
		return fmt.Errorf("hook %s failed: %w", name, err)
	}
	return nil
}

// Cleanup runs before_remove (best-effort) then removes the workspace directory
// (SPEC §9.4, §8.5, §8.6).
func (m *Manager) Cleanup(ctx context.Context, identifier string) error {
	path := m.PathFor(identifier)
	if err := m.assertContained(path); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		if strings.TrimSpace(m.hooks.BeforeRemove) != "" {
			// before_remove failure/timeout is logged and ignored (SPEC §9.4).
			_ = m.RunHook(ctx, "before_remove", path)
		}
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove workspace: %w", err)
	}
	return nil
}

// AssertLaunchCWD enforces safety invariants 1 & 2 right before agent launch
// (SPEC §9.5).
func (m *Manager) AssertLaunchCWD(identifier, cwd string) error {
	want := m.PathFor(identifier)
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	if filepath.Clean(absCWD) != filepath.Clean(want) {
		return fmt.Errorf("agent cwd %q must equal workspace path %q", absCWD, want)
	}
	return m.assertContained(want)
}

// assertContained enforces that path is inside the workspace root (SPEC §9.5
// invariant 2).
func (m *Manager) assertContained(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve workspace path: %w", err)
	}
	absRoot, err := filepath.Abs(m.root)
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	absPath = filepath.Clean(absPath)
	absRoot = filepath.Clean(absRoot)
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return fmt.Errorf("workspace path escapes root: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("workspace path %q escapes root %q", absPath, absRoot)
	}
	return nil
}

func (m *Manager) scriptFor(name string) string {
	switch name {
	case "after_create":
		return m.hooks.AfterCreate
	case "before_run":
		return m.hooks.BeforeRun
	case "after_run":
		return m.hooks.AfterRun
	case "before_remove":
		return m.hooks.BeforeRemove
	default:
		return ""
	}
}
