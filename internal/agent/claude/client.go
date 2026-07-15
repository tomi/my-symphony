package claude

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tomi/my-symphony/internal/agent"
)

// maxLineBytes bounds a single stream-json line at 10 MB (SPEC §10.1).
const maxLineBytes = 10 * 1024 * 1024

// malformedLineLimit is the run of consecutive unparseable lines treated as
// stream corruption (SPEC §10.3).
const malformedLineLimit = 10

// PendingSessionID is the placeholder session id until the real one arrives
// (SPEC §10.2).
const PendingSessionID = "pending"

// Config holds the claude client settings (SPEC §5.3.6, §5.3.7).
type Config struct {
	Command           string
	Model             string // appended as --model when non-empty
	ReasoningEffort   string // appended as --effort when non-empty
	ResumeAcrossTurns bool
	TurnTimeoutMs     int
	ReadTimeoutMs     int
}

// Client launches Claude Code CLI subprocesses (SPEC §10.1).
type Client struct {
	cfg Config
}

// NewClient constructs a Claude Code client.
func NewClient(cfg Config) *Client { return &Client{cfg: cfg} }

// Session tracks one worker's Claude session across continuation turns
// (SPEC §10.2). A subprocess is spawned per turn; the session only carries the
// resolved session id and turn counter.
type Session struct {
	Workspace  string
	Identifier string
	Title      string
	SessionID  string
	turn       int
}

// StartSession returns a pending session; no subprocess is spawned until the
// first turn (SPEC §10.2).
func (c *Client) StartSession(workspace, identifier, title string) *Session {
	return &Session{
		Workspace:  workspace,
		Identifier: identifier,
		Title:      title,
		SessionID:  PendingSessionID,
	}
}

// StopSession is a no-op: each Claude Code subprocess exits at the end of its
// turn (SPEC §10.3).
func (c *Client) StopSession(_ *Session) {}

// buildCommand assembles the shell command for a turn: the base command, then
// per-run --model / --effort flags (SPEC §5.3.7), then --resume once a session id
// is known (SPEC §5.3.6). Model/effort values are shell-quoted since the command
// is executed via `bash -lc`.
func (c *Client) buildCommand(sessionID string) string {
	command := c.cfg.Command
	if c.cfg.Model != "" {
		command += " --model " + shellSingleQuote(c.cfg.Model)
	}
	if c.cfg.ReasoningEffort != "" {
		command += " --effort " + shellSingleQuote(c.cfg.ReasoningEffort)
	}
	if c.cfg.ResumeAcrossTurns && sessionID != "" && sessionID != PendingSessionID {
		command += " --resume " + sessionID
	}
	return command
}

// shellSingleQuote wraps a value in single quotes for safe interpolation into a
// `bash -lc` command string.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TurnResult reports per-turn usage for accumulation (SPEC §13.5).
type TurnResult struct {
	Usage *agent.Usage
}

// RunTurn spawns one subprocess, writes the prompt to stdin, and streams events
// until a terminal result (SPEC §10.1, §10.3).
func (c *Client) RunTurn(ctx context.Context, s *Session, prompt string, emit func(agent.Event)) (*TurnResult, error) {
	s.turn++
	turnID := strconv.Itoa(s.turn)

	command := c.buildCommand(s.SessionID)

	turnTimeout := time.Duration(c.cfg.TurnTimeoutMs) * time.Millisecond
	var turnCtx context.Context
	var cancel context.CancelFunc
	if turnTimeout > 0 {
		turnCtx, cancel = context.WithTimeout(ctx, turnTimeout)
	} else {
		turnCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	// bash -lc <command>, cwd = workspace, prompt on stdin (SPEC §10.1).
	cmd := exec.CommandContext(turnCtx, "bash", "-lc", command)
	cmd.Dir = s.Workspace

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, &Error{Code: CodeResponseError, Msg: "stdin pipe", Wrapped: err}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &Error{Code: CodeResponseError, Msg: "stdout pipe", Wrapped: err}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, &Error{Code: CodeResponseError, Msg: "stderr pipe", Wrapped: err}
	}

	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, &Error{Code: CodeClaudeNotFound, Msg: err.Error(), Wrapped: err}
		}
		return nil, &Error{Code: CodeClaudeNotFound, Msg: err.Error(), Wrapped: err}
	}

	pid := strconv.Itoa(cmd.Process.Pid)

	// Write prompt then close stdin (SPEC §10.1).
	go func() {
		_, _ = io.WriteString(stdin, prompt)
		_ = stdin.Close()
	}()

	// Drain stderr into a bounded tail for diagnostics only (SPEC §10.3).
	tail := &boundedTail{limit: 8 * 1024}
	var stderrWG sync.WaitGroup
	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		tail.readFrom(stderr)
	}()

	// Read stdout lines on a goroutine so the main loop can enforce timeouts.
	linesCh := make(chan lineMsg)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
		for sc.Scan() {
			b := append([]byte(nil), sc.Bytes()...)
			linesCh <- lineMsg{line: b}
		}
		if err := sc.Err(); err != nil {
			linesCh <- lineMsg{err: err}
		}
		close(linesCh)
	}()

	result, runErr := c.consume(turnCtx, s, turnID, &pid, linesCh, emit)

	// Kill the subprocess (if still running) before waiting so a returned error
	// — e.g. a read timeout while the process idles — does not block on Wait
	// (SPEC §10.6).
	cancel()
	_ = cmd.Wait()
	stderrWG.Wait()

	if runErr != nil {
		// Attach stderr tail for diagnostics.
		if de, ok := runErr.(*Error); ok && de.Msg == "" {
			de.Msg = tail.string()
		}
		return nil, runErr
	}
	return result, nil
}

// lineMsg carries one stdout line or a read error.
type lineMsg struct {
	line []byte
	err  error
}

// consume processes stream-json lines until a terminal result, timeout, or EOF.
func (c *Client) consume(ctx context.Context, s *Session, turnID string, pid *string,
	linesCh <-chan lineMsg, emit func(agent.Event)) (*TurnResult, error) {

	readTimeout := time.Duration(c.cfg.ReadTimeoutMs) * time.Millisecond
	var readTimer *time.Timer
	var readCh <-chan time.Time
	if readTimeout > 0 {
		readTimer = time.NewTimer(readTimeout)
		readCh = readTimer.C
		defer readTimer.Stop()
	}

	malformed := 0
	sawEvent := false
	sessionStartedEmitted := false

	for {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				emit(agent.Event{Event: agent.EventTurnFailed, Timestamp: time.Now().UTC(),
					AgentPID: pid, SessionID: s.SessionID, TurnID: turnID, Message: "turn timeout"})
				return nil, &Error{Code: CodeTurnTimeout}
			}
			emit(agent.Event{Event: agent.EventTurnCancelled, Timestamp: time.Now().UTC(),
				AgentPID: pid, SessionID: s.SessionID, TurnID: turnID})
			return nil, &Error{Code: CodeTurnCancelled}

		case <-readCh:
			if !sawEvent {
				// No first event within read timeout (SPEC §10.6 response_timeout).
				emit(agent.Event{Event: agent.EventStartupFailed, Timestamp: time.Now().UTC(),
					AgentPID: pid, SessionID: s.SessionID, TurnID: turnID, Message: "response timeout"})
				return nil, &Error{Code: CodeResponseTimeout}
			}

		case msg, ok := <-linesCh:
			if !ok {
				// Stream ended with no terminal result -> failure (SPEC §10.3).
				emit(agent.Event{Event: agent.EventTurnFailed, Timestamp: time.Now().UTC(),
					AgentPID: pid, SessionID: s.SessionID, TurnID: turnID,
					Message: "stream ended before result"})
				return nil, &Error{Code: CodeTurnFailed, Msg: "stream ended before result"}
			}
			if msg.err != nil {
				emit(agent.Event{Event: agent.EventTurnFailed, Timestamp: time.Now().UTC(),
					AgentPID: pid, SessionID: s.SessionID, TurnID: turnID,
					Message: "stdout read error"})
				return nil, &Error{Code: CodeResponseError, Msg: msg.err.Error(), Wrapped: msg.err}
			}
			if len(msg.line) == 0 {
				continue
			}

			evt, parseErr := parseLine(msg.line)
			if parseErr != nil {
				malformed++
				emit(agent.Event{Event: agent.EventMalformed, Timestamp: time.Now().UTC(),
					AgentPID: pid, SessionID: s.SessionID, TurnID: turnID,
					Message: "malformed line"})
				if malformed >= malformedLineLimit {
					return nil, &Error{Code: CodeStreamCorruption,
						Msg: fmt.Sprintf("%d consecutive malformed lines", malformed)}
				}
				continue
			}
			malformed = 0
			sawEvent = true
			if readTimer != nil {
				readTimer.Stop()
				readCh = nil
			}

			done, res, err := c.handleEvent(s, turnID, pid, evt, &sessionStartedEmitted, emit)
			if err != nil {
				return nil, err
			}
			if done {
				return res, nil
			}
		}
	}
}
