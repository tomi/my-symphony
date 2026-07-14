// Package logging provides structured key=value logging with the spec's
// required context fields and multi-sink degradation (SPEC §13.1–§13.2).
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// Logger emits structured logs with stable key=value phrasing (SPEC §13.1).
type Logger struct {
	mu    sync.Mutex
	sinks []*slog.Logger
}

// New returns a Logger writing key=value text to stderr by default.
func New() *Logger {
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	return &Logger{sinks: []*slog.Logger{slog.New(h)}}
}

// NewWithSinks builds a Logger over one or more slog loggers (sinks).
func NewWithSinks(sinks ...*slog.Logger) *Logger {
	return &Logger{sinks: sinks}
}

// maxMessageLen bounds any single log value to avoid dumping large payloads
// (SPEC §13.1, §15.4).
const maxMessageLen = 500

// Truncate shortens a value for logging (SPEC §15.4).
func Truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxMessageLen {
		return s
	}
	return s[:maxMessageLen] + "…(truncated)"
}

func (l *Logger) log(level slog.Level, msg string, args ...any) {
	l.mu.Lock()
	sinks := l.sinks
	l.mu.Unlock()
	// A failing sink must not crash the service; degrade to remaining sinks
	// (SPEC §13.2).
	for _, s := range sinks {
		func() {
			defer func() { _ = recover() }()
			s.Log(context.Background(), level, msg, args...)
		}()
	}
}

// Info logs at info level.
func (l *Logger) Info(msg string, args ...any) { l.log(slog.LevelInfo, msg, args...) }

// Warn logs at warn level.
func (l *Logger) Warn(msg string, args ...any) { l.log(slog.LevelWarn, msg, args...) }

// Error logs at error level.
func (l *Logger) Error(msg string, args ...any) { l.log(slog.LevelError, msg, args...) }

// Debug logs at debug level.
func (l *Logger) Debug(msg string, args ...any) { l.log(slog.LevelDebug, msg, args...) }
