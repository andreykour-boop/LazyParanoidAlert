//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"siren/internal/config"
	"siren/internal/health"
)

// TestHealthStatePath: an absolute data_dir is used as is; a relative one is
// resolved against the executable directory, mirroring the service's
// Chdir(filepath.Dir(exe)), so the UI and the service agree on one file.
func TestHealthStatePath(t *testing.T) {
	if got := healthStatePath(nil); got != "" {
		t.Errorf("nil cfg path = %q, want empty", got)
	}

	abs := filepath.Join(t.TempDir(), "state")
	cfg := &config.Config{DataDir: abs}
	if got := healthStatePath(cfg); got != health.StatePath(abs) {
		t.Errorf("absolute data_dir = %q, want %q", got, health.StatePath(abs))
	}

	cfg.DataDir = "data"
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	want := health.StatePath(filepath.Join(filepath.Dir(exe), "data"))
	if got := healthStatePath(cfg); got != want {
		t.Errorf("relative data_dir = %q, want %q (exe dir)", got, want)
	}
}

func panelCfg(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		DataDir: t.TempDir(),
		Health:  &config.Health{Enabled: true, CheckHour: 9, ReportWeekday: 1, WarnPct: 10},
	}
}

// TestHealthPanelDisabled: the watch switched off is named explicitly.
func TestHealthPanelDisabled(t *testing.T) {
	cfg := panelCfg(t)
	cfg.Health.Enabled = false
	if got := renderHealthPanel(cfg, time.Now()); !strings.Contains(got, "выключен") {
		t.Errorf("disabled text = %q", got)
	}
}

// TestHealthPanelNoSnapshot: an enabled watch with no state file yet offers
// the live check instead of an empty panel.
func TestHealthPanelNoSnapshot(t *testing.T) {
	cfg := panelCfg(t)
	if got := renderHealthPanel(cfg, time.Now()); !strings.Contains(got, "Снимка ещё нет") {
		t.Errorf("no-snapshot text = %q", got)
	}
}

// TestHealthPanelCorruptState: a broken state file is reported, not hidden.
func TestHealthPanelCorruptState(t *testing.T) {
	cfg := panelCfg(t)
	if err := os.WriteFile(healthStatePath(cfg), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := renderHealthPanel(cfg, time.Now()); !strings.Contains(got, "не читается") {
		t.Errorf("corrupt text = %q", got)
	}
}

// TestHealthPanelSnapshotAndMarks: a fresh snapshot shows its collection
// time and age; SMART disks come before volumes; volumes render as
// total/used/free; the volume below the threshold is marked, a healthy one
// is not; a stale snapshot is marked as outdated; lines use \r\n.
func TestHealthPanelSnapshotAndMarks(t *testing.T) {
	cfg := panelCfg(t)
	now := time.Now()
	snap := health.Snapshot{
		CollectedAt: now.Add(-2 * time.Hour),
		L1:          []health.L1Disk{{Name: "TOSHIBA MD04ACA600", Status: health.StatusOK}},
		Volumes: []health.Volume{
			{Mount: "C:", TotalBytes: 500 << 30, FreeBytes: 100 << 30},  // 20% — ok
			{Mount: "D:", TotalBytes: 5600 << 30, FreeBytes: 400 << 30}, // ~7.1% — low
		},
	}
	if err := health.SaveState(cfg.DataDir, snap); err != nil {
		t.Fatal(err)
	}
	got := renderHealthPanel(cfg, now)
	if !strings.Contains(got, "Снимок от") || !strings.Contains(got, "2 ч") {
		t.Errorf("header missing time/age: %q", got)
	}
	if !strings.Contains(got, "\r\n") {
		t.Errorf("panel must use CRLF line breaks: %q", got)
	}
	diskIdx, volIdx := strings.Index(got, "Диски (SMART)"), strings.Index(got, "Тома:")
	if diskIdx < 0 || volIdx < 0 || diskIdx > volIdx {
		t.Errorf("SMART section must come before volumes: %q", got)
	}
	if !strings.Contains(got, "занято") {
		t.Errorf("volumes must show used space: %q", got)
	}
	if !strings.Contains(got, "ниже порога") {
		t.Errorf("low volume not marked: %q", got)
	}
	cIdx, dIdx := strings.Index(got, "C:"), strings.Index(got, "D:")
	if cIdx < 0 || dIdx < 0 || cIdx > dIdx {
		t.Errorf("volumes out of order: %q", got)
	}
	if strings.Contains(got[cIdx:dIdx], "ниже порога") {
		t.Errorf("healthy volume must not be marked: %q", got)
	}

	snap.CollectedAt = now.Add(-30 * time.Hour)
	if err := health.SaveState(cfg.DataDir, snap); err != nil {
		t.Fatal(err)
	}
	if got := renderHealthPanel(cfg, now); !strings.Contains(got, "УСТАРЕЛ") {
		t.Errorf("stale snapshot not marked: %q", got)
	}
}

// TestPanelBodyMinimalErrors: the panel is a glance screen — the permanent
// "vendor CLI not found" note is dropped when the machine has no RAID
// arrays, real losses (smartctl) always show, and the empty section prints
// nothing at all.
func TestPanelBodyMinimalErrors(t *testing.T) {
	th := health.Thresholds{WarnPct: 10}
	vendorNote := health.SourceError{
		Source: "vendor-cli",
		Reason: "vendor CLI (storcli/perccli/arcconf) не найден в PATH",
	}
	smartLoss := health.SourceError{Source: "smartctl", Reason: "smartctl: executable file not found"}

	// No RAID arrays: vendor-cli note is noise, dropped entirely; nothing
	// else failed either, so the whole section stays out.
	noRAID := health.Snapshot{L1: []health.L1Disk{{Name: "SSD", Status: health.StatusOK}}}
	if got := renderPanelBody(noRAID, th); strings.Contains(got, "Не смог проверить") {
		t.Errorf("empty errors section must not print: %q", got)
	}

	// No RAID arrays but smartctl lost: the real loss shows.
	noRAIDLoss := noRAID
	noRAIDLoss.Errors = []health.SourceError{vendorNote, smartLoss}
	if got := renderPanelBody(noRAIDLoss, th); !strings.Contains(got, "smartctl") {
		t.Errorf("real source loss must show: %q", got)
	}
	if got := renderPanelBody(noRAIDLoss, th); strings.Contains(got, "vendor-cli") {
		t.Errorf("vendor-cli note must be dropped without RAID: %q", got)
	}

	// RAID arrays present: vendor-cli missing is a real visibility gap.
	raid := noRAID
	raid.L2 = []health.L2Entity{{Name: "md0", Kind: "md", State: "clean"}}
	raid.Errors = []health.SourceError{vendorNote}
	if got := renderPanelBody(raid, th); !strings.Contains(got, "vendor-cli") {
		t.Errorf("vendor-cli note must show with RAID present: %q", got)
	}
}

// TestLiveHealthText: a live collection renders the fresh-check header and
// the snapshot body, and never writes the state file.
func TestLiveHealthText(t *testing.T) {
	cfg := panelCfg(t)
	got := renderLiveHealth(cfg)
	if !strings.Contains(got, "Проверено сейчас") {
		t.Errorf("live header missing: %q", got)
	}
	if !strings.Contains(got, "не сохранялось") {
		t.Errorf("live disclaimer missing: %q", got)
	}
	if _, ok, _ := health.LoadState(cfg.DataDir); ok {
		t.Error("live check must not write the state file")
	}
}
