package main

import (
	"strings"
	"testing"
	"time"
)

// journalFixture builds a short-iso journalctl line.
func journalFixture(ts time.Time, unit, message string) string {
	return ts.Format("2006-01-02T15:04:05-0700") + " srv-10-5 " + unit + ": " + message
}

var jt100 = time.Date(2026, 11, 3, 5, 40, 0, 0, time.FixedZone("T", 3*3600))
var jtNow = jt100.Add(8 * time.Hour)

func TestJournalTimestampParsing(t *testing.T) {
	line := journalFixture(jt100, "systemd[1]", "Stopping target")
	at, ok := parseJournalTimestamp(line)
	if !ok {
		t.Fatalf("timestamp not parsed: %q", line)
	}
	if !at.Equal(jt100) {
		t.Errorf("parsed %v, want %v", at, jt100)
	}
	// Offset-less variant (older systemd) and garbage.
	if _, ok := parseJournalTimestamp("2026-11-03T05:40:00 systemd[1]: x"); !ok {
		t.Error("offset-less timestamp must parse")
	}
	if _, ok := parseJournalTimestamp("no timestamp here"); ok {
		t.Error("garbage must not parse")
	}
}

func TestClassifyLinuxSessionCleanRebootWithSudo(t *testing.T) {
	lines := []string{
		journalFixture(jt100.Add(-time.Hour), "sshd[800]", "Accepted publickey for owner"),
		journalFixture(jt100.Add(-time.Minute), "sudo[900]",
			`andrey : TTY=pts/0 ; PWD=/home/andrey ; USER=root ; COMMAND=/usr/sbin/reboot`),
		journalFixture(jt100, "systemd[1]", "Stopping Session 2 of user andrey."),
		journalFixture(jt100.Add(30*time.Second), "systemd[1]", "Reached target Shutdown."),
		journalFixture(jt100.Add(31*time.Second), "systemd[1]", "Reached target Reboot."),
	}
	f := classifyLinuxSession(lines, jtNow)
	if !f.Known || !f.Clean {
		t.Errorf("clean reboot misclassified: %+v", f)
	}
	if f.Action != "reboot" {
		t.Errorf("action = %q, want reboot", f.Action)
	}
	if !f.LastAt.Equal(jt100.Add(31 * time.Second)) {
		t.Errorf("LastAt = %v, want the reboot marker time", f.LastAt)
	}
	if !strings.Contains(f.Initiator, "andrey") || !strings.Contains(f.Initiator, "/usr/sbin/reboot") {
		t.Errorf("initiator = %q, want user with command", f.Initiator)
	}
}

func TestClassifyLinuxSessionCleanPowerOffNoSudo(t *testing.T) {
	lines := []string{
		journalFixture(jt100, "systemd[1]", "Stopping Daily apt upgrade and clean activities."),
		journalFixture(jt100.Add(10*time.Second), "systemd[1]", "Reached target Power-Off."),
	}
	f := classifyLinuxSession(lines, jtNow)
	if !f.Known || !f.Clean {
		t.Errorf("clean power-off misclassified: %+v", f)
	}
	if f.Action != "shutdown" {
		t.Errorf("action = %q, want shutdown", f.Action)
	}
	// Absent sudo evidence: the initiator is omitted, the alert stands.
	if f.Initiator != "" {
		t.Errorf("initiator = %q, want empty", f.Initiator)
	}
}

func TestClassifyLinuxSessionDirty(t *testing.T) {
	lines := []string{
		journalFixture(jt100.Add(-time.Hour), "cron[500]", "job started"),
		journalFixture(jt100, "kernel", "nvidia: probe failed"),
	}
	f := classifyLinuxSession(lines, jtNow)
	if !f.Known {
		t.Error("classification must be known even for a dirty session")
	}
	if f.Clean {
		t.Errorf("dirty session misclassified as clean: %+v", f)
	}
	if f.Action != "" {
		t.Errorf("dirty session must have no action, got %q", f.Action)
	}
	if !f.LastAt.Equal(jt100) {
		t.Errorf("LastAt = %v, want the last activity %v", f.LastAt, jt100)
	}
	if f.Initiator != "" {
		t.Errorf("dirty session must have no initiator, got %q", f.Initiator)
	}
}

func TestJournalSudoShutdownFiltering(t *testing.T) {
	// A sudo line with an unrelated command must not count as evidence.
	if _, _, ok := journalSudoShutdown(journalFixture(jt100, "sudo[900]",
		`andrey : TTY=pts/0 ; PWD=/ ; USER=root ; COMMAND=/usr/bin/apt update`)); ok {
		t.Error("unrelated sudo command must not match")
	}
	// shutdown -r counts as reboot intent, still recorded as sudo evidence.
	user, cmd, ok := journalSudoShutdown(journalFixture(jt100, "sudo[901]",
		`root : TTY=console ; PWD=/ ; USER=root ; COMMAND=/sbin/shutdown -r now`))
	if !ok || user != "root" || !strings.Contains(cmd, "-r") {
		t.Errorf("shutdown -r evidence lost: user=%q cmd=%q ok=%v", user, cmd, ok)
	}
}
