package logsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// readLogLines parses the temp log file as JSON lines and returns them.
func readLogLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	var lines []map[string]any
	for _, ln := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(ln) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(ln, &m); err != nil {
			t.Fatalf("parse log line %q: %v", ln, err)
		}
		lines = append(lines, m)
	}
	return lines
}

func TestFileOutputAndLevels(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agent.log")

	log, cleanup, err := New("info", logFile)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()

	log.Debug("debug must be filtered out")
	log.Info("info goes through", "agent", "siren")
	log.Error("config error: bad level")

	lines := readLogLines(t, logFile)
	if len(lines) != 2 {
		t.Fatalf("want 2 log lines after filtering, got %d", len(lines))
	}
	if msg, _ := lines[0]["msg"].(string); msg != "info goes through" {
		t.Errorf("first line msg = %q", msg)
	}
	if msg, _ := lines[1]["msg"].(string); msg != "config error: bad level" {
		t.Errorf("second line msg = %q", msg)
	}
	if lvl, _ := lines[1]["level"].(string); lvl != "ERROR" {
		t.Errorf("error line level = %q", lvl)
	}
}

func TestAppendSemantics(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agent.log")

	for i := 0; i < 2; i++ {
		log, cleanup, err := New("info", logFile)
		if err != nil {
			t.Fatalf("New run %d: %v", i, err)
		}
		log.Info("startup attempt", "run", i)
		cleanup()
	}
	if lines := readLogLines(t, logFile); len(lines) != 2 {
		t.Fatalf("log must append across runs, want 2 lines, got %d", len(lines))
	}
}

func TestBadLevel(t *testing.T) {
	if _, _, err := New("loud", ""); err == nil {
		t.Fatal("expected error for bad level, got nil")
	}
}

func TestBadFile(t *testing.T) {
	if _, _, err := New("info", filepath.Join(t.TempDir(), "no-such-dir", "x.log")); err == nil {
		t.Fatal("expected error for unopenable file, got nil")
	}
}

func TestDiscard(t *testing.T) {
	// Discard must accept any call without panicking or writing anywhere.
	Discard().Info("dropped")
	if Discard().Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("Discard logger must report nothing as enabled")
	}
}
