package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func newMgr(t *testing.T, hooks Hooks) *Manager {
	t.Helper()
	root := t.TempDir()
	return New(root, hooks, nil)
}

func TestDeterministicPathAndSanitization(t *testing.T) {
	m := newMgr(t, Hooks{TimeoutMs: 1000})
	p := m.PathFor("AB/C 1")
	if filepath.Base(p) != "AB_C_1" {
		t.Errorf("sanitized base = %q", filepath.Base(p))
	}
}

func TestCreateAndReuse(t *testing.T) {
	m := newMgr(t, Hooks{TimeoutMs: 1000})
	ctx := context.Background()

	ws1, err := m.CreateForIssue(ctx, "AB-1")
	if err != nil {
		t.Fatal(err)
	}
	if !ws1.CreatedNow {
		t.Errorf("first create should set CreatedNow")
	}
	if _, err := os.Stat(ws1.Path); err != nil {
		t.Errorf("dir not created: %v", err)
	}

	ws2, err := m.CreateForIssue(ctx, "AB-1")
	if err != nil {
		t.Fatal(err)
	}
	if ws2.CreatedNow {
		t.Errorf("reuse should not set CreatedNow")
	}
}

func TestAfterCreateHook_OnlyOnNew(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "created.txt")
	m := New(filepath.Join(dir, "ws"), Hooks{
		AfterCreate: "echo hi > " + marker,
		TimeoutMs:   2000,
	}, nil)
	ctx := context.Background()

	if _, err := m.CreateForIssue(ctx, "AB-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("after_create did not run: %v", err)
	}
	os.Remove(marker)

	if _, err := m.CreateForIssue(ctx, "AB-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("after_create should not run on reuse")
	}
}

func TestAfterCreateFailure_RemovesDir(t *testing.T) {
	root := t.TempDir()
	m := New(root, Hooks{AfterCreate: "exit 1", TimeoutMs: 2000}, nil)
	_, err := m.CreateForIssue(context.Background(), "AB-1")
	if err == nil {
		t.Fatalf("expected after_create failure")
	}
	if _, statErr := os.Stat(filepath.Join(root, "AB-1")); statErr == nil {
		t.Errorf("failed workspace should be removed")
	}
}

func TestBeforeRunHook_FailureReturnsError(t *testing.T) {
	m := newMgr(t, Hooks{BeforeRun: "exit 3", TimeoutMs: 2000})
	ws, err := m.CreateForIssue(context.Background(), "AB-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RunHook(context.Background(), "before_run", ws.Path); err == nil {
		t.Errorf("before_run failure should return error")
	}
}

func TestHookTimeout(t *testing.T) {
	m := newMgr(t, Hooks{BeforeRun: "sleep 5", TimeoutMs: 100})
	ws, _ := m.CreateForIssue(context.Background(), "AB-1")
	if err := m.RunHook(context.Background(), "before_run", ws.Path); err == nil {
		t.Errorf("expected timeout error")
	}
}

func TestExistingNonDirectoryFails(t *testing.T) {
	root := t.TempDir()
	// Create a file where the workspace dir should be.
	if err := os.WriteFile(filepath.Join(root, "AB-1"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(root, Hooks{TimeoutMs: 1000}, nil)
	_, err := m.CreateForIssue(context.Background(), "AB-1")
	if err == nil {
		t.Errorf("expected error for non-directory path")
	}
}

func TestAssertLaunchCWD(t *testing.T) {
	m := newMgr(t, Hooks{TimeoutMs: 1000})
	ws, _ := m.CreateForIssue(context.Background(), "AB-1")
	if err := m.AssertLaunchCWD("AB-1", ws.Path); err != nil {
		t.Errorf("valid cwd rejected: %v", err)
	}
	if err := m.AssertLaunchCWD("AB-1", "/tmp/other"); err == nil {
		t.Errorf("mismatched cwd should be rejected")
	}
}

func TestCleanup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "removed.txt")
	m := New(filepath.Join(dir, "ws"), Hooks{
		BeforeRemove: "echo bye > " + marker,
		TimeoutMs:    2000,
	}, nil)
	ctx := context.Background()
	ws, _ := m.CreateForIssue(ctx, "AB-1")
	if err := m.Cleanup(ctx, "AB-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ws.Path); err == nil {
		t.Errorf("workspace should be removed")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("before_remove hook should have run")
	}
}
