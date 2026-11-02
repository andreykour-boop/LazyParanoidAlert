// Package logsetup builds the agent's own structured logger from config
// values: level filtering (debug..error) and optional file output with
// append semantics. An empty file path selects stderr.
package logsetup

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

// New creates the logger and a cleanup function that closes the log file, if
// one was opened. The caller MUST invoke cleanup on shutdown; passing it to
// defer is enough. level is one of "debug", "info", "warn", "error".
func New(level, file string) (*slog.Logger, func(), error) {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		return nil, nil, fmt.Errorf("logsetup: bad level %q: %w", level, err)
	}
	var out io.Writer = os.Stderr
	cleanup := func() {}
	if file != "" {
		f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, nil, fmt.Errorf("logsetup: open log file %s: %w", file, err)
		}
		out = f
		cleanup = func() { _ = f.Close() }
	}
	h := slog.NewJSONHandler(out, &slog.HandlerOptions{Level: lv})
	return slog.New(h), cleanup, nil
}

// Discard returns a logger that drops everything; used by tests and by
// components created without configuration.
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelError + 1,
	}))
}
