package main

// Boot event sensor: on every agent pipeline start it compares the current
// boot identity of the machine with the one saved in data/boot.json. A
// mismatch means the machine itself rebooted (the service restarts keep the
// identity); the sensor then reports the boot with a classification of the
// previous session through the direct delivery channel — immediately,
// bypassing the aggregation window. The sensor is one-shot: it runs, emits
// at most one alert and exits.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"siren/internal/config"
	"siren/internal/notify"
)

// Platform hooks, wired by boot_windows.go / boot_linux.go via init().
// bootIdentityFn resolves the current boot identity (mandatory). A nil
// bootClassifyFn means the previous-session classification is unavailable
// on this platform yet: the alert degrades to level 1.
var (
	bootIdentityFn func() (string, error)
	bootClassifyFn func() (notify.BootFacts, error)
)

// bootState is the persisted boot identity. At records when the identity
// was saved, not when the machine booted (the identity itself carries that
// where the platform provides it).
type bootState struct {
	Identity string    `json:"identity"`
	At       time.Time `json:"at"`
}

// bootStatePath mirrors the health state scheme: one JSON file in the data
// directory next to health_state.json.
func bootStatePath(dataDir string) string {
	return filepath.Join(dataDir, "boot.json")
}

// loadBootState reads the saved boot identity; ok is false both for a
// missing file (first run) and an unreadable one (caller logs the reason).
func loadBootState(dataDir string) (st bootState, ok bool, err error) {
	raw, err := os.ReadFile(bootStatePath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return bootState{}, false, nil
		}
		return bootState{}, false, fmt.Errorf("load boot state: %w", err)
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return bootState{}, false, fmt.Errorf("load boot state: %w", err)
	}
	return st, true, nil
}

// saveBootState persists the identity atomically (temp file + rename, the
// scheme of config.Save and health.SaveState), creating the data dir.
func saveBootState(dataDir string, st bootState) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("save boot state: create data dir: %w", err)
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("save boot state: %w", err)
	}
	raw = append(raw, '\n')
	tmp := bootStatePath(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("save boot state: %w", err)
	}
	if err := os.Rename(tmp, bootStatePath(dataDir)); err != nil {
		return fmt.Errorf("save boot state: %w", err)
	}
	return nil
}

// bootAction is the decision of planBoot.
type bootAction int

const (
	bootFirstRun bootAction = iota // no state yet: record silently
	bootSameBoot                   // service restart: no alert
	bootNewBoot                    // the machine rebooted: alert
)

// planBoot is the pure decision core. A missing previous identity inside an
// existing state file (corrupted JSON fields) counts as first run: the
// alert is skipped once, the state rebuilds.
func planBoot(stateExists bool, prev, cur string) bootAction {
	if !stateExists || strings.TrimSpace(prev) == "" {
		return bootFirstRun
	}
	if strings.EqualFold(strings.TrimSpace(prev), strings.TrimSpace(cur)) {
		return bootSameBoot
	}
	return bootNewBoot
}

// agentDataDir resolves cfg.DataDir against the executable directory for
// relative paths — the same contract the health state uses. One shared
// resolver for every agent state.
func agentDataDir(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if filepath.IsAbs(cfg.DataDir) {
		return cfg.DataDir
	}
	base, err := os.Executable()
	if err != nil {
		return cfg.DataDir
	}
	return filepath.Join(filepath.Dir(base), cfg.DataDir)
}

// runBoot is the sensor body: decide, emit at most one alert through the
// direct channel, persist the new identity. Every failure degrades to a log
// warning — the boot sensor must never take the agent pipeline down, and a
// missed classification must not suppress the fact of the boot.
func runBoot(ctx context.Context, cfg *config.Config, agentIP string, direct chan<- string, log *slog.Logger) error {
	dataDir := agentDataDir(cfg)
	if dataDir == "" {
		log.Warn("boot: no data dir configured, sensor skipped")
		return nil
	}
	cur, err := bootIdentityFn()
	if err != nil {
		log.Warn("boot: identity unavailable, no boot alert", "err", err)
		return nil
	}
	cur = strings.TrimSpace(cur)

	state, exists, err := loadBootState(dataDir)
	if err != nil {
		log.Warn("boot: state unreadable, treating as first run", "err", err)
		exists = false
	}

	switch planBoot(exists, state.Identity, cur) {
	case bootSameBoot:
		return nil
	case bootFirstRun:
		log.Info("boot: first run, recording identity silently", "identity", cur)
		return saveBootState(dataDir, bootState{Identity: cur, At: time.Now()})
	}

	// New machine boot: classify the previous session (best-effort) and
	// deliver immediately. The alert goes out before the state is saved:
	// a crash in between repeats the alert on the next start, which is
	// preferable to a lost alert.
	facts := notify.BootFacts{Now: time.Now()}
	if bootClassifyFn != nil {
		if f, err := bootClassifyFn(); err != nil {
			log.Warn("boot: previous session classification failed", "err", err)
		} else {
			facts = f
		}
	}
	if facts.Now.IsZero() {
		facts.Now = time.Now()
	}
	text := notify.BootAlertText(cfg.AgentName, agentIP, facts)
	select {
	case direct <- text:
	case <-ctx.Done():
		return ctx.Err()
	}
	return saveBootState(dataDir, bootState{Identity: cur, At: time.Now()})
}
