package main

// Pure parsing of the previous-boot journal tail (journalctl -b -1 output in
// the short-iso format). Kept platform-neutral so the fixture tests run on
// every build; boot_linux.go only runs the command and wires the hooks.

import (
	"regexp"
	"strings"
	"time"

	"siren/internal/notify"
)

// journalTimestampRe matches the leading short-iso timestamp of a journalctl
// line, with or without the UTC offset (the offset appeared in newer
// systemd); the offset itself is irrelevant — only gaps within one host's
// journal are compared.
var journalTimestampRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:[+-]\d{4})?`)

// parseJournalTimestamp reads the leading timestamp of a journalctl line.
func parseJournalTimestamp(line string) (time.Time, bool) {
	m := journalTimestampRe.FindString(line)
	if m == "" {
		return time.Time{}, false
	}
	layout := "2006-01-02T15:04:05"
	if len(m) > 19 {
		layout = "2006-01-02T15:04:05-0700"
	}
	t, err := time.Parse(layout, m)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// journalTargetAction names the shutdown fingerprint of a reached systemd
// target: reboot or power-off. Every initiated restart passes through the
// reboot target, a plain power-off through the power-off/shutdown target.
func journalTargetAction(line string) string {
	switch {
	case strings.Contains(line, "Reached target Reboot"):
		return "reboot"
	case strings.Contains(line, "Reached target Power-Off"),
		strings.Contains(line, "Reached target Shutdown"),
		strings.Contains(line, "Reached target Halt"):
		return "shutdown"
	}
	return ""
}

// journalSudoLine extracts the invoker from a sudo log line
// ("sudo[123]: user : TTY=... ; COMMAND=...").
var journalSudoLine = regexp.MustCompile(`sudo\[\d+\]:\s+([^\s:]+)\s*:`)

// journalSudoShutdown recognizes a shutdown-family command issued through
// sudo; the invoker is best-effort evidence, its absence never fails the
// classification.
func journalSudoShutdown(line string) (user, command string, ok bool) {
	if !strings.Contains(line, "COMMAND=") {
		return "", "", false
	}
	m := journalSudoLine.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	cmd := strings.TrimSpace(line[strings.LastIndex(line, "COMMAND=")+len("COMMAND="):])
	lc := strings.ToLower(cmd)
	if !strings.Contains(lc, "reboot") && !strings.Contains(lc, "shutdown") &&
		!strings.Contains(lc, "poweroff") && !strings.Contains(lc, "halt") {
		return "", "", false
	}
	return m[1], cmd, true
}

// classifyLinuxSession builds the alert facts from the journal tail of the
// previous boot: a reached reboot/power-off target marks a clean end (D4),
// its absence — a dirty one; the freshest timestamp is the last activity.
func classifyLinuxSession(lines []string, now time.Time) notify.BootFacts {
	facts := notify.BootFacts{Known: true, Now: now}
	var action, sudoUser, sudoCmd string
	var markerAt, lastAt time.Time
	for _, line := range lines {
		if at, ok := parseJournalTimestamp(line); ok && at.After(lastAt) {
			lastAt = at
		}
		if a := journalTargetAction(line); a != "" {
			// The last marker wins: a reboot logs the shutdown target
			// first and the reboot target last, and the final one is
			// what actually happened.
			action = a
			if at, ok := parseJournalTimestamp(line); ok {
				markerAt = at
			}
		}
		if u, c, ok := journalSudoShutdown(line); ok && sudoUser == "" {
			sudoUser, sudoCmd = u, c
		}
	}
	facts.Clean = !markerAt.IsZero()
	facts.Action = action
	if facts.Clean {
		facts.LastAt = markerAt
		if sudoUser != "" {
			facts.Initiator = sudoUser
			if sudoCmd != "" {
				facts.Initiator += " (" + sudoCmd + ")"
			}
		}
	} else {
		facts.LastAt = lastAt
	}
	return facts
}
