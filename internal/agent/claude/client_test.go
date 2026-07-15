package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomi/my-symphony/internal/agent"
)

func collect(events *[]agent.Event) func(agent.Event) {
	return func(e agent.Event) { *events = append(*events, e) }
}

func runTurn(t *testing.T, cfg Config, workspace string, sess *Session) (*TurnResult, []agent.Event, error) {
	t.Helper()
	c := NewClient(cfg)
	var events []agent.Event
	res, err := c.RunTurn(context.Background(), sess, "PROMPT-BODY", collect(&events))
	return res, events, err
}

func TestRunTurn_SuccessSessionAndUsage(t *testing.T) {
	ws := t.TempDir()
	cmd := `printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-9"}' '{"type":"result","is_error":false,"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":2}}'`
	sess := (&Client{}).StartSession(ws, "AB-1", "Title")
	res, events, err := runTurn(t, Config{Command: cmd, TurnTimeoutMs: 5000, ReadTimeoutMs: 2000}, ws, sess)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if sess.SessionID != "sess-9" {
		t.Errorf("session id = %q", sess.SessionID)
	}
	if res.Usage == nil || res.Usage.InputTokens != 12 || res.Usage.OutputTokens != 5 || res.Usage.TotalTokens != 17 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if !hasEvent(events, agent.EventSessionStarted) || !hasEvent(events, agent.EventTurnCompleted) {
		t.Errorf("missing lifecycle events: %+v", events)
	}
}

func TestRunTurn_CwdAndStdin(t *testing.T) {
	ws := t.TempDir()
	cwdFile := filepath.Join(ws, "cwd.txt")
	stdinFile := filepath.Join(ws, "stdin.txt")
	cmd := `pwd > ` + cwdFile + `; cat > ` + stdinFile + `; printf '{"type":"result","is_error":false}\n'`
	sess := (&Client{}).StartSession(ws, "AB-1", "Title")
	_, _, err := runTurn(t, Config{Command: cmd, TurnTimeoutMs: 5000, ReadTimeoutMs: 2000}, ws, sess)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	gotCwd, _ := os.ReadFile(cwdFile)
	// macOS/tmp symlinks aside, on linux TempDir is canonical.
	if strings.TrimSpace(string(gotCwd)) != ws {
		t.Errorf("cwd = %q, want %q", strings.TrimSpace(string(gotCwd)), ws)
	}
	gotStdin, _ := os.ReadFile(stdinFile)
	if string(gotStdin) != "PROMPT-BODY" {
		t.Errorf("stdin = %q", string(gotStdin))
	}
}

func TestRunTurn_ResumeAppended(t *testing.T) {
	ws := t.TempDir()
	cmdFile := filepath.Join(ws, "cmdline.txt")
	// Capture the full bash cmdline first (it includes any appended flags),
	// then emit the terminal result last so trailing appended args are harmless.
	cmd := `tr '\0' ' ' < /proc/$$/cmdline > ` + cmdFile + `; printf '{"type":"result","is_error":false}\n'`
	sess := &Session{Workspace: ws, Identifier: "AB-1", SessionID: "sess-42"}
	_, _, err := runTurn(t, Config{Command: cmd, ResumeAcrossTurns: true, TurnTimeoutMs: 5000, ReadTimeoutMs: 2000}, ws, sess)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	line, _ := os.ReadFile(cmdFile)
	if !strings.Contains(string(line), "--resume sess-42") {
		t.Errorf("expected --resume in command line, got %q", string(line))
	}
}

func TestRunTurn_ModelAndEffortAppended(t *testing.T) {
	ws := t.TempDir()
	cmdFile := filepath.Join(ws, "cmdline.txt")
	cmd := `tr '\0' ' ' < /proc/$$/cmdline > ` + cmdFile + `; printf '{"type":"result","is_error":false}\n'`
	sess := &Session{Workspace: ws, Identifier: "AB-1", SessionID: "sess-7"}
	_, _, err := runTurn(t, Config{
		Command:           cmd,
		Model:             "opus",
		ReasoningEffort:   "high",
		ResumeAcrossTurns: true,
		TurnTimeoutMs:     5000,
		ReadTimeoutMs:     2000,
	}, ws, sess)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	line, _ := os.ReadFile(cmdFile)
	got := string(line)
	for _, want := range []string{"--model 'opus'", "--effort 'high'", "--resume sess-7"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in command line, got %q", want, got)
		}
	}
}

func TestBuildCommand_OmitsFlagsWhenUnset(t *testing.T) {
	c := NewClient(Config{Command: "claude -p", ResumeAcrossTurns: true})
	got := c.buildCommand(PendingSessionID)
	if got != "claude -p" {
		t.Errorf("expected bare command, got %q", got)
	}
}

func TestRunTurn_ErrorResult(t *testing.T) {
	ws := t.TempDir()
	cmd := `printf '{"type":"result","is_error":true}\n'`
	sess := (&Client{}).StartSession(ws, "AB-1", "T")
	_, _, err := runTurn(t, Config{Command: cmd, TurnTimeoutMs: 5000, ReadTimeoutMs: 2000}, ws, sess)
	assertClaudeCode(t, err, CodeResponseError)
}

func TestRunTurn_NoResultFails(t *testing.T) {
	ws := t.TempDir()
	cmd := `printf '{"type":"system","subtype":"init","session_id":"s"}\n'`
	sess := (&Client{}).StartSession(ws, "AB-1", "T")
	_, _, err := runTurn(t, Config{Command: cmd, TurnTimeoutMs: 5000, ReadTimeoutMs: 2000}, ws, sess)
	assertClaudeCode(t, err, CodeTurnFailed)
}

func TestRunTurn_MalformedRunFails(t *testing.T) {
	ws := t.TempDir()
	cmd := `for i in $(seq 1 15); do echo "garbage line $i"; done`
	sess := (&Client{}).StartSession(ws, "AB-1", "T")
	_, _, err := runTurn(t, Config{Command: cmd, TurnTimeoutMs: 5000, ReadTimeoutMs: 2000}, ws, sess)
	assertClaudeCode(t, err, CodeStreamCorruption)
}

func TestRunTurn_TurnTimeout(t *testing.T) {
	ws := t.TempDir()
	cmd := `printf '{"type":"system","subtype":"init","session_id":"s"}\n'; sleep 5`
	sess := (&Client{}).StartSession(ws, "AB-1", "T")
	// read timeout disabled so turn timeout is the trigger
	_, _, err := runTurn(t, Config{Command: cmd, TurnTimeoutMs: 200, ReadTimeoutMs: 0}, ws, sess)
	assertClaudeCode(t, err, CodeTurnTimeout)
}

func TestRunTurn_ReadTimeout(t *testing.T) {
	ws := t.TempDir()
	cmd := `sleep 5`
	sess := (&Client{}).StartSession(ws, "AB-1", "T")
	start := time.Now()
	_, _, err := runTurn(t, Config{Command: cmd, TurnTimeoutMs: 10000, ReadTimeoutMs: 100}, ws, sess)
	assertClaudeCode(t, err, CodeResponseTimeout)
	if time.Since(start) > 3*time.Second {
		t.Errorf("read timeout took too long")
	}
}

func hasEvent(events []agent.Event, kind string) bool {
	for _, e := range events {
		if e.Event == kind {
			return true
		}
	}
	return false
}

func assertClaudeCode(t *testing.T, err error, code string) {
	t.Helper()
	ce, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected claude.Error, got %T: %v", err, err)
	}
	if ce.Code != code {
		t.Fatalf("code = %s, want %s", ce.Code, code)
	}
}
