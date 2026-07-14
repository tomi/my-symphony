// Package watcher watches WORKFLOW.md for changes and posts validated reloads
// to the orchestrator (SPEC §6.2). Invalid reloads never crash the service.
package watcher

import (
	"context"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/tomi/my-symphony/internal/config"
	"github.com/tomi/my-symphony/internal/logging"
	"github.com/tomi/my-symphony/internal/orchestrator"
	"github.com/tomi/my-symphony/internal/workflow"
)

// Watcher reloads WORKFLOW.md on change.
type Watcher struct {
	path   string
	events chan<- orchestrator.Event
	logger *logging.Logger
}

// New builds a Watcher for the given WORKFLOW.md path.
func New(path string, events chan<- orchestrator.Event, logger *logging.Logger) *Watcher {
	return &Watcher{path: path, events: events, logger: logger}
}

// Run watches the file and its parent directory until ctx is cancelled. Editors
// often replace-on-save via rename, which fires on the directory (SPEC §6.2).
func (w *Watcher) Run(ctx context.Context) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		w.logger.Warn("workflow watch disabled", "error", err.Error())
		return
	}
	defer fsw.Close()

	abs, _ := filepath.Abs(w.path)
	dir := filepath.Dir(abs)
	_ = fsw.Add(dir)
	_ = fsw.Add(abs) // best-effort; may not exist as separate watch target

	var debounce *time.Timer
	trigger := func() {
		if debounce != nil {
			debounce.Stop()
		}
		debounce = time.AfterFunc(150*time.Millisecond, w.reload)
	}

	for {
		select {
		case <-ctx.Done():
			if debounce != nil {
				debounce.Stop()
			}
			return
		case ev, ok := <-fsw.Events:
			if !ok {
				return
			}
			evAbs, _ := filepath.Abs(ev.Name)
			if evAbs == abs {
				trigger()
			}
		case err, ok := <-fsw.Errors:
			if !ok {
				return
			}
			w.logger.Warn("workflow watch error", "error", err.Error())
		}
	}
}

// reload re-reads and re-validates the workflow, posting a ReloadConfig on
// success and keeping last-known-good on failure (SPEC §6.2).
func (w *Watcher) reload() {
	def, err := workflow.Load(w.path)
	if err != nil {
		w.logger.Warn("workflow reload failed; keeping last good config",
			"outcome", "failed", "error", err.Error())
		return
	}
	cfg, err := config.New(def.Config, filepath.Dir(w.path))
	if err != nil {
		w.logger.Warn("workflow reload failed; keeping last good config",
			"outcome", "failed", "error", err.Error())
		return
	}
	if err := cfg.ValidateDispatch(); err != nil {
		w.logger.Warn("workflow reload invalid; keeping last good config",
			"outcome", "failed", "error", err.Error())
		return
	}
	select {
	case w.events <- orchestrator.ReloadConfig{Cfg: cfg, Tmpl: def.PromptTemplate}:
	default:
		w.logger.Warn("workflow reload dropped; loop busy")
	}
}
