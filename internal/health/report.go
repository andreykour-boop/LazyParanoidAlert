package health

import (
	"fmt"
	"strings"
)

// reportCap keeps the report inside the Telegram message limit (4096) with
// headroom for the header the delivery layer may add.
const reportCap = 3800

// formatChanges renders a change list as the alarm body: one line per
// change, already human-readable.
func formatChanges(changes []Change) string {
	lines := make([]string, 0, len(changes))
	for _, c := range changes {
		lines = append(lines, "• "+c.Detail)
	}
	return strings.Join(lines, "\n")
}

// formatSpaceAlert renders the immediate space-alarm body: violating
// volumes with sizes and the configured thresholds.
func formatSpaceAlert(violations []Volume, th Thresholds) string {
	lines := make([]string, 0, len(violations))
	for _, v := range violations {
		lines = append(lines, "• "+volumeLine(v))
	}
	return strings.Join(lines, "\n") + "\nпорог: " + thresholdLine(th)
}

// formatSpaceReminder is the daily "still low" reminder text; same facts as
// the alarm, delivered directly.
func formatSpaceReminder(violations []Volume, th Thresholds) string {
	return formatSpaceAlert(violations, th)
}

func volumeLine(v Volume) string {
	mark := ""
	if v.ReadOnly {
		mark = " (съёмный)"
	}
	return fmt.Sprintf("%s%s — всего %.1f ГБ, свободно %.1f ГБ (%.1f%%)",
		v.Mount, mark, giB(v.TotalBytes), giB(v.FreeBytes), v.FreePct())
}

func thresholdLine(th Thresholds) string {
	if th.MinFreeGB > 0 {
		return fmt.Sprintf("%d%% или %d ГБ", th.WarnPct, th.MinFreeGB)
	}
	return fmt.Sprintf("%d%%", th.WarnPct)
}

func giB(b uint64) float64 {
	return float64(b) / (1 << 30)
}

// VisibleErrors filters snapshot source errors for user-facing renders
// (Telegram report, control panel): the permanently absent hardware-RAID
// vendor CLI is noise on machines with no RAID arrays at all — there is
// nothing behind a controller it could check — so the note is dropped
// there. Real losses (smartctl, WMI) always show, and with RAID arrays
// present the absent vendor CLI is a genuine visibility gap.
func VisibleErrors(snap Snapshot) []SourceError {
	out := make([]SourceError, 0, len(snap.Errors))
	for _, e := range snap.Errors {
		if e.Source == "vendor-cli" && len(snap.L2) == 0 {
			continue
		}
		out = append(out, e)
	}
	return out
}

// SnapshotText renders the snapshot body: volumes with capacity and free
// space (GB and %), then L2 arrays/pools and L1 disks when present, then
// the "could not check" section only when something actually failed (the
// absent RAID vendor CLI on RAID-less machines is filtered out as noise).
// Volumes below the space thresholds carry a warning mark; removable media
// is never marked (space alerts exclude it too). WeeklyReport wraps this
// with its header; the control UI reuses the body as-is for its panel.
func SnapshotText(snap Snapshot, th Thresholds) string {
	var b strings.Builder

	// Volumes; violating ones are marked so the reader sees the problem
	// without comparing percentages against the config by hand.
	low := make(map[string]bool)
	for _, v := range SpaceViolations(snap, th) {
		low[v.Mount] = true
	}
	if len(snap.Volumes) > 0 {
		b.WriteString("Тома:\n")
		for _, v := range snap.Volumes {
			line := volumeLine(v)
			if low[v.Mount] {
				line += "  ⚠ ниже порога (" + thresholdLine(th) + ")"
			}
			fmt.Fprintf(&b, "  %s\n", line)
		}
	} else {
		b.WriteString("Тома: не собраны\n")
	}

	// L2 arrays/pools/logical volumes: shown only when there is data — an
	// RAID-less machine gets no placeholder line.
	if len(snap.L2) > 0 {
		b.WriteString("Массивы/тома:\n")
		for _, e := range snap.L2 {
			line := fmt.Sprintf("  %s (%s): %s", e.Name, e.Kind, e.State)
			if e.Reason != "" {
				line += " — " + e.Reason
			}
			fmt.Fprintf(&b, "%s\n", line)
		}
	}

	// L1 physical disks: shown only when collected; a machine whose disks
	// could not be checked names them in the "could not check" section.
	if len(snap.L1) > 0 {
		b.WriteString("Диски:\n")
		for _, d := range snap.L1 {
			line := fmt.Sprintf("  %s: %s", d.Name, d.Status)
			if d.Realloc > 0 || d.Pending > 0 {
				line += fmt.Sprintf(" — realloc %d, pending %d", d.Realloc, d.Pending)
			}
			if d.MediaErrors > 0 || d.Wear > 0 {
				line += fmt.Sprintf(" — media errors %d, wear %d%%", d.MediaErrors, d.Wear)
			}
			if d.Reason != "" && d.Status != StatusOK {
				line += " — " + d.Reason
			}
			fmt.Fprintf(&b, "%s\n", line)
		}
	}

	// "Could not check" section: printed only when something actually
	// failed. The permanent "vendor CLI not found" note on RAID-less
	// machines is filtered out as noise.
	if errs := VisibleErrors(snap); len(errs) > 0 {
		b.WriteString("Не смог проверить:\n")
		for _, e := range errs {
			fmt.Fprintf(&b, "  %s: %s\n", e.Source, e.Reason)
		}
	}

	out := strings.TrimRight(b.String(), "\n")
	return out
}

// WeeklyReport renders the planned weekly report: the header line plus the
// snapshot body (SnapshotText). This text goes straight to delivery,
// bypassing aggregation.
func WeeklyReport(snap Snapshot, agentLabel string, th Thresholds) string {
	header := fmt.Sprintf("📅 Еженедельный отчёт о дисках — %s, %s\n\n", agentLabel,
		snap.CollectedAt.Format("2006-01-02 15:04"))
	out := strings.TrimRight(header+SnapshotText(snap, th), "\n")
	if len(out) > reportCap {
		out = out[:reportCap] + "\n… (отчёт сокращён)"
	}
	return out
}
