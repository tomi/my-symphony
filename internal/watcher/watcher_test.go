package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomi/my-symphony/internal/logging"
	"github.com/tomi/my-symphony/internal/orchestrator"
)

func writeWorkflow(t *testing.T, path, interval string) {
	t.Helper()
	content := "---\ntracker:\n  kind: linear\n  api_key: k\n  project_slug: p\npolling:\n  interval_ms: " + interval + "\n---\nbody"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWatcher_ReloadsValidChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	writeWorkflow(t, path, "1000")

	events := make(chan orchestrator.Event, 4)
	w := New(path, events, logging.New())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	time.Sleep(200 * time.Millisecond)
	writeWorkflow(t, path, "5000")

	select {
	case ev := <-events:
		reload, ok := ev.(orchestrator.ReloadConfig)
		if !ok {
			t.Fatalf("expected ReloadConfig, got %T", ev)
		}
		if reload.Cfg.Polling.IntervalMs != 5000 {
			t.Errorf("interval = %d, want 5000", reload.Cfg.Polling.IntervalMs)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no reload event received")
	}
}

func TestWatcher_InvalidReloadKeptSilent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	writeWorkflow(t, path, "1000")

	events := make(chan orchestrator.Event, 4)
	w := New(path, events, logging.New())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	time.Sleep(200 * time.Millisecond)
	// Missing api_key/project_slug -> validation fails -> no reload event.
	if err := os.WriteFile(path, []byte("---\ntracker:\n  kind: linear\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-events:
		t.Fatalf("invalid reload should not emit an event, got %T", ev)
	case <-time.After(800 * time.Millisecond):
		// expected: no event
	}
}
