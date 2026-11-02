//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"siren/internal/config"
	"siren/internal/health"
)

// staleAfter bounds the "fresh enough" window for the persisted snapshot:
// one daily check cycle plus slack. An older snapshot means the watch has
// not collected for over a day (the service is likely not running), which
// the panel must name instead of presenting stale data as current.
const staleAfter = 26 * time.Hour

// healthDataDir resolves the data directory for the control UI: an absolute
// data_dir is used as is; a relative one is resolved against the executable
// directory, mirroring the service's Chdir(filepath.Dir(exe)) on start, so
// the UI reads the same snapshot file the running service writes regardless
// of where the UI was launched from. A failing executable path falls back
// to the process working directory (best effort).
func healthDataDir(cfg *config.Config) string {
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

// healthStatePath returns the snapshot file path the panel reads; a nil
// config has no path (the panel shows the config error instead).
func healthStatePath(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return health.StatePath(healthDataDir(cfg))
}

// panelThresholds builds the space thresholds for rendering from the config
// section (nil section never reaches here: the caller checks Enabled first).
func panelThresholds(h *config.Health) health.Thresholds {
	return health.Thresholds{WarnPct: h.WarnPct, MinFreeGB: h.MinFreeGB}
}

// renderHealthPanel renders the panel text from the persisted snapshot.
// Degenerate states are named explicitly instead of showing an empty or
// misleading panel: the watch disabled, a missing snapshot, an unreadable
// state file and a stale snapshot each get their own message.
func renderHealthPanel(cfg *config.Config, now time.Time) string {
	if cfg == nil {
		return panelCRLF("Конфиг не читается — данные дисков недоступны")
	}
	if cfg.Health == nil || !cfg.Health.Enabled {
		return panelCRLF("Сторож дисков выключен (health.enabled = false в конфиге).")
	}
	snap, ok, err := health.LoadState(healthDataDir(cfg))
	if err != nil {
		return panelCRLF("Снимок здоровья не читается: " + err.Error())
	}
	if !ok {
		return panelCRLF("Снимка ещё нет — сторож не собирал данные.\n" +
			"Нажмите «Проверить сейчас», чтобы собрать свежие данные.")
	}
	age := now.Sub(snap.CollectedAt)
	head := fmt.Sprintf("Снимок от %s (%s назад)",
		snap.CollectedAt.Format("02.01.2006 15:04"), humanizeAge(age))
	if age > staleAfter {
		head += "\n⚠ СНИМОК УСТАРЕЛ — сторож давно не собирал данные (служба работает?)"
	}
	return panelCRLF(head + "\n\n" + renderPanelBody(snap, panelThresholds(cfg.Health)))
}

// renderLiveHealth renders a fresh one-shot collection result. It is only
// display: the persisted state file is untouched and no notifier exists in
// the UI process, so nothing is saved and nothing is sent anywhere.
func renderLiveHealth(cfg *config.Config) string {
	l1, l2, vols := health.NewPlatformSources()
	c := &health.Collector{L1Sources: l1, L2Sources: l2, VolumesSources: vols}
	snap := c.Snapshot()
	head := fmt.Sprintf("Проверено сейчас — %s (живой сбор, состояние не сохранялось)",
		time.Now().Format("02.01.2006 15:04:05"))
	return panelCRLF(head + "\n\n" + renderPanelBody(snap, panelThresholds(cfg.Health)))
}

// renderPanelBody formats a snapshot for the panel, structured top-down:
// SMART disks first, then arrays/pools when present, then volumes one line
// each (total / used / free), then the mandatory "could not check" section.
// This layout is the owner's preference for a glance screen and is
// deliberately separate from health.SnapshotText, which keeps the spec'd
// Telegram weekly-report format.
func renderPanelBody(snap health.Snapshot, th health.Thresholds) string {
	var b strings.Builder

	// L1 physical disks (SMART) first — the health answer at a glance.
	if len(snap.L1) > 0 {
		b.WriteString("Диски (SMART):\n")
		for _, d := range snap.L1 {
			line := fmt.Sprintf("  %s: %s", d.Name, d.Status)
			if d.Realloc > 0 || d.Pending > 0 {
				line += fmt.Sprintf(" — realloc %d, pending %d", d.Realloc, d.Pending)
			}
			if d.MediaErrors > 0 || d.Wear > 0 {
				line += fmt.Sprintf(" — media errors %d, wear %d%%", d.MediaErrors, d.Wear)
			}
			if d.Reason != "" && d.Status != health.StatusOK {
				line += " — " + d.Reason
			}
			b.WriteString(line + "\n")
		}
	} else {
		b.WriteString("Диски (SMART): нет данных\n")
	}

	// L2 arrays/pools/logical volumes: a section only when there is data.
	if len(snap.L2) > 0 {
		b.WriteString("\nМассивы/тома:\n")
		for _, e := range snap.L2 {
			line := fmt.Sprintf("  %s (%s): %s", e.Name, e.Kind, e.State)
			if e.Reason != "" {
				line += " — " + e.Reason
			}
			b.WriteString(line + "\n")
		}
	}

	// Volumes, one line per volume: capacity, used, free.
	low := make(map[string]bool)
	for _, v := range health.SpaceViolations(snap, th) {
		low[v.Mount] = true
	}
	if len(snap.Volumes) > 0 {
		b.WriteString("\nТома:\n")
		for _, v := range snap.Volumes {
			used := v.TotalBytes - v.FreeBytes
			line := fmt.Sprintf("  %s — всего %.1f ГБ, занято %.1f ГБ, свободно %.1f ГБ (%.1f%%)",
				v.Mount, giB(v.TotalBytes), giB(used), giB(v.FreeBytes), v.FreePct())
			if low[v.Mount] {
				line += "  ⚠ ниже порога (" + thresholdLinePanel(th) + ")"
			}
			b.WriteString(line + "\n")
		}
	} else {
		b.WriteString("\nТома: не собраны\n")
	}

	// "Could not check" section, minimally: shown only when something is
	// actually worth reporting. The permanent "vendor CLI not found" note
	// is noise on machines without hardware RAID and is dropped when the
	// snapshot has no L2 arrays at all; real losses (smartctl, WMI) always
	// show. The Telegram weekly report applies the same filter.
	errs := health.VisibleErrors(snap)
	if len(errs) > 0 {
		b.WriteString("\nНе смог проверить:\n")
		for _, e := range errs {
			fmt.Fprintf(&b, "  %s: %s\n", e.Source, e.Reason)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// panelCRLF converts newlines for the Windows EDIT control, which only
// breaks lines on \r\n.
func panelCRLF(text string) string {
	return strings.ReplaceAll(text, "\n", "\r\n")
}

// thresholdLinePanel renders the configured space thresholds for the mark.
func thresholdLinePanel(th health.Thresholds) string {
	if th.MinFreeGB > 0 {
		return fmt.Sprintf("%d%% или %d ГБ", th.WarnPct, th.MinFreeGB)
	}
	return fmt.Sprintf("%d%%", th.WarnPct)
}

func giB(b uint64) float64 {
	return float64(b) / (1 << 30)
}

// humanizeAge renders a snapshot age compactly for the panel header.
func humanizeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "только что"
	case d < time.Hour:
		return fmt.Sprintf("%d мин", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d ч %d мин", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%d сут %d ч", int(d.Hours())/24, int(d.Hours())%24)
	}
}
