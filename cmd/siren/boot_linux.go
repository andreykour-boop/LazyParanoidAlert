//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"siren/internal/notify"
)

// Linux boot identity: the kernel's boot_id, a stable UUID that changes on
// every machine boot and survives service restarts. Reading /proc is
// instantaneous and dependency-free.
func init() { bootIdentityFn = linuxBootIdentity }

// linuxBootClassify reads the tail of the previous boot's journal (D4) and
// classifies it with the pure functions of boot_journal.go. An empty tail
// (no records for boot -1) is a classification failure, not a dirty verdict:
// the alert degrades to level 1 via the caller.
func init() { bootClassifyFn = linuxBootClassify }

func linuxBootIdentity() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("boot: read boot_id: %w", err)
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", fmt.Errorf("boot: empty boot_id")
	}
	return id, nil
}

func linuxBootClassify() (notify.BootFacts, error) {
	out, err := exec.Command("journalctl", "-b", "-1", "-n", "200",
		"--output=short-iso", "--no-pager").CombinedOutput()
	if err != nil {
		return notify.BootFacts{}, fmt.Errorf("boot: journalctl -b -1: %v: %s", err, strings.TrimSpace(string(out)))
	}
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return notify.BootFacts{}, fmt.Errorf("boot: journalctl -b -1 returned no records")
	}
	return classifyLinuxSession(lines, time.Now()), nil
}
