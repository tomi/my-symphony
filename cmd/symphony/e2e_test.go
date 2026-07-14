package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// buildBinary compiles the symphony binary once for lifecycle tests.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "symphony")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

func TestCLI_MissingWorkflowExitsNonzero(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, filepath.Join(t.TempDir(), "nope.md"))
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected nonzero exit for missing workflow")
	}
}

// fakeLinear serves minimal GraphQL responses driving one full dispatch cycle.
func fakeLinear(t *testing.T, candidateFetched, refreshFetched *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)

		// State refresh (running reconciliation / per-turn refresh) uses $ids.
		// Keep the issue active so the worker completes its (single) turn and
		// exits normally rather than being cancelled by terminal reconciliation
		// mid-run (which would interrupt the after_create hook).
		if strings.Contains(req.Query, "$ids") {
			atomic.AddInt32(refreshFetched, 1)
			w.Write([]byte(`{"data":{"issues":{"nodes":[
				{"id":"i1","identifier":"AB-1","title":"t","state":{"name":"In Progress"},"labels":{"nodes":[]}}
			],"pageInfo":{"hasNextPage":false}}}}`))
			return
		}
		// Distinguish terminal fetch from candidate fetch by filtered states.
		filter, _ := req.Variables["filter"].(map[string]any)
		if isTerminalFilter(filter) {
			w.Write([]byte(`{"data":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}`))
			return
		}
		// Candidate fetch: return AB-1 only on the first call.
		if atomic.AddInt32(candidateFetched, 1) == 1 {
			w.Write([]byte(`{"data":{"issues":{"nodes":[
				{"id":"i1","identifier":"AB-1","title":"Do it","priority":1,"state":{"name":"Todo"},
				 "labels":{"nodes":[]},"inverseRelations":{"nodes":[]}}
			],"pageInfo":{"hasNextPage":false}}}}`))
			return
		}
		w.Write([]byte(`{"data":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}`))
	}))
}

func isTerminalFilter(filter map[string]any) bool {
	state, _ := filter["state"].(map[string]any)
	name, _ := state["name"].(map[string]any)
	in, _ := name["in"].([]any)
	for _, s := range in {
		if s == "Done" {
			return true
		}
	}
	return false
}

func TestCLI_FullLifecycle(t *testing.T) {
	bin := buildBinary(t)

	var candidateFetched, refreshFetched int32
	srv := fakeLinear(t, &candidateFetched, &refreshFetched)
	defer srv.Close()

	dir := t.TempDir()
	claudeCmd := `printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-1"}' ` +
		`'{"type":"result","is_error":false,"usage":{"input_tokens":3,"output_tokens":1}}'`
	// after_create writes a marker OUTSIDE the workspace root so that terminal
	// workspace cleanup (the issue goes Done) cannot remove our evidence that a
	// workspace was created and its hook ran.
	marker := filepath.Join(dir, "created.marker")
	workflow := fmt.Sprintf(`---
tracker:
  kind: linear
  endpoint: %s
  api_key: test-key
  project_slug: proj
  active_states: [Todo, In Progress]
  terminal_states: [Done]
polling:
  interval_ms: 200
workspace:
  root: %s
hooks:
  after_create: "printf created > %s"
agent:
  max_turns: 1
claude:
  command: "%s"
  read_timeout_ms: 2000
  turn_timeout_ms: 5000
  stall_timeout_ms: 0
---
Work on {{ issue.identifier }}: {{ issue.title }}
`, srv.URL, filepath.Join(dir, "ws"), marker, strings.ReplaceAll(claudeCmd, `"`, `\"`))

	wfPath := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(wfPath, []byte(workflow), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, wfPath)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Poll until a workspace was created (marker present) and the loop has
	// fetched candidates and refreshed a running issue. Polling with a timeout
	// avoids racing the worker's asynchronous startup.
	deadline := time.Now().Add(15 * time.Second)
	workspaceCreated := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			workspaceCreated = true
		}
		if workspaceCreated &&
			atomic.LoadInt32(&candidateFetched) >= 1 && atomic.LoadInt32(&refreshFetched) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if atomic.LoadInt32(&candidateFetched) < 1 {
		_ = cmd.Process.Kill()
		t.Fatalf("candidate fetch never happened")
	}
	if atomic.LoadInt32(&refreshFetched) < 1 {
		_ = cmd.Process.Kill()
		t.Fatalf("running-issue refresh never happened (worker did not dispatch)")
	}
	if !workspaceCreated {
		_ = cmd.Process.Kill()
		t.Fatalf("workspace for AB-1 was never created (after_create marker missing)")
	}

	// Graceful shutdown should exit zero.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected clean exit, got %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("process did not exit after SIGTERM")
	}
}
