package health

import "fmt"

// ChangeKind classifies one difference between two snapshots.
type ChangeKind string

const (
	ChangeDiskStatus    ChangeKind = "disk_status"        // L1 status changed
	ChangeDiskNew       ChangeKind = "disk_new"           // disk appeared
	ChangeDiskGone      ChangeKind = "disk_gone"          // disk disappeared
	ChangeArrayStatus   ChangeKind = "array_status"       // L2 state changed
	ChangeArrayNew      ChangeKind = "array_new"          // array/pool/lvolume appeared
	ChangeArrayGone     ChangeKind = "array_gone"         // array/pool/lvolume disappeared
	ChangeVolumeNew     ChangeKind = "volume_new"         // mount appeared
	ChangeVolumeGone    ChangeKind = "volume_gone"        // mount disappeared
	ChangeSpaceLow      ChangeKind = "space_low"          // new free-space threshold violation
	ChangeSourceLost    ChangeKind = "source_lost"        // data source became unavailable
	ChangeReallocGrowth ChangeKind = "realloc_growth"     // SMART realloc count increased
	ChangeMediaGrowth   ChangeKind = "media_error_growth" // NVMe media errors increased
)

// Change is one detected difference. Entity is the disk/array/mount/source
// name; Detail is a human-readable one-line description for alerts.
type Change struct {
	Kind   ChangeKind `json:"kind"`
	Entity string     `json:"entity"`
	Detail string     `json:"detail"`
}

// Thresholds carries the free-space alert thresholds from the config's
// health section.
type Thresholds struct {
	// WarnPct: alert when free space falls below this percent of volume
	// size. Validated range 1..99.
	WarnPct int
	// MinFreeGB: alert when free space falls below this absolute floor in
	// GiB; 0 disables the absolute check.
	MinFreeGB int
}

// spaceLow reports whether a volume violates the thresholds.
func (th Thresholds) spaceLow(v Volume) bool {
	if th.WarnPct > 0 && v.FreePct() < float64(th.WarnPct) {
		return true
	}
	if th.MinFreeGB > 0 && v.FreeBytes < uint64(th.MinFreeGB)<<30 {
		return true
	}
	return false
}

// SpaceViolations lists every volume of the snapshot currently below the
// thresholds. The runner uses it for the daily "still low" reminders, which
// are not snapshot changes and therefore not part of Diff.
func SpaceViolations(snap Snapshot, th Thresholds) []Volume {
	var out []Volume
	for _, v := range snap.Volumes {
		if !v.ReadOnly && th.spaceLow(v) {
			out = append(out, v)
		}
	}
	return out
}

// Diff compares the previous snapshot against the current one and returns
// every change worth alerting about. A nil prev (first run, no saved state)
// yields no changes: the rule is "alert on change", never a start-up
// "all is well" report. Status flips, entities appearing or disappearing,
// new threshold violations and lost data sources are all reported.
func Diff(prev, cur Snapshot, th Thresholds) []Change {
	if prev.CollectedAt.IsZero() {
		return nil
	}

	var out []Change

	curL1 := indexL1(cur)
	prevL1 := indexL1(prev)
	for _, d := range cur.L1 {
		old, ok := prevL1[d.Name]
		switch {
		case !ok:
			out = append(out, Change{Kind: ChangeDiskNew, Entity: d.Name,
				Detail: fmt.Sprintf("диск появился: %s (%s), статус %s", d.Name, diskLabel(d), d.Status)})
		case old.Status != d.Status:
			out = append(out, Change{Kind: ChangeDiskStatus, Entity: d.Name,
				Detail: fmt.Sprintf("диск %s: статус %s -> %s%s", d.Name, old.Status, d.Status, countersSuffix(old, d))})
		case sameCounterSource(old, d) && d.Realloc > old.Realloc:
			// Reallocated-sector growth while SMART still passes: the
			// earliest sign of surface degradation, alert immediately.
			// A decrease is physically impossible and ignored.
			out = append(out, Change{Kind: ChangeReallocGrowth, Entity: d.Name,
				Detail: fmt.Sprintf("диск %s: realloc %d -> %d", d.Name, old.Realloc, d.Realloc)})
		case sameCounterSource(old, d) && d.MediaErrors > old.MediaErrors:
			// NVMe media-error growth: the sector-counter analog of
			// realloc growth — the earliest sign of NAND degradation.
			out = append(out, Change{Kind: ChangeMediaGrowth, Entity: d.Name,
				Detail: fmt.Sprintf("диск %s: media errors %d -> %d", d.Name, old.MediaErrors, d.MediaErrors)})
		}
	}
	for _, d := range prev.L1 {
		if _, ok := curL1[d.Name]; !ok {
			out = append(out, Change{Kind: ChangeDiskGone, Entity: d.Name,
				Detail: fmt.Sprintf("диск исчез из системы: %s (%s)", d.Name, diskLabel(d))})
		}
	}

	curL2 := indexL2(cur)
	prevL2 := indexL2(prev)
	for _, e := range cur.L2 {
		old, ok := prevL2[e.Name]
		switch {
		case !ok:
			out = append(out, Change{Kind: ChangeArrayNew, Entity: e.Name,
				Detail: fmt.Sprintf("массив/том появился: %s (%s), состояние %s", e.Name, e.Kind, e.State)})
		case old.State != e.State:
			out = append(out, Change{Kind: ChangeArrayStatus, Entity: e.Name,
				Detail: fmt.Sprintf("массив %s (%s): состояние %s -> %s%s", e.Name, e.Kind, old.State, e.State, reasonSuffix(old.Reason, e.Reason))})
		}
	}
	for _, e := range prev.L2 {
		if _, ok := curL2[e.Name]; !ok {
			out = append(out, Change{Kind: ChangeArrayGone, Entity: e.Name,
				Detail: fmt.Sprintf("массив/том исчез: %s (%s, было %s)", e.Name, e.Kind, e.State)})
		}
	}

	curVols := indexVolumes(cur)
	prevVols := indexVolumes(prev)
	for _, v := range cur.Volumes {
		old, ok := prevVols[v.Mount]
		newViolation := th.spaceLow(v)
		switch {
		case !ok:
			out = append(out, Change{Kind: ChangeVolumeNew, Entity: v.Mount,
				Detail: fmt.Sprintf("том появился: %s (%s)", v.Mount, volumeSize(v))})
			if newViolation {
				out = append(out, Change{Kind: ChangeSpaceLow, Entity: v.Mount,
					Detail: spaceDetail(v, th)})
			}
		case newViolation && !th.spaceLow(old):
			out = append(out, Change{Kind: ChangeSpaceLow, Entity: v.Mount,
				Detail: spaceDetail(v, th)})
		}
	}
	for _, v := range prev.Volumes {
		if _, ok := curVols[v.Mount]; !ok {
			out = append(out, Change{Kind: ChangeVolumeGone, Entity: v.Mount,
				Detail: fmt.Sprintf("том исчез: %s (%s)", v.Mount, volumeSize(v))})
		}
	}

	prevErrs := indexErrors(prev)
	for _, e := range cur.Errors {
		if _, ok := prevErrs[e.Source]; !ok {
			out = append(out, Change{Kind: ChangeSourceLost, Entity: e.Source,
				Detail: fmt.Sprintf("источник данных потерян: %s (%s)", e.Source, e.Reason)})
		}
	}

	return out
}

func indexL1(s Snapshot) map[string]L1Disk {
	m := make(map[string]L1Disk, len(s.L1))
	for _, d := range s.L1 {
		m[d.Name] = d
	}
	return m
}

func indexL2(s Snapshot) map[string]L2Entity {
	m := make(map[string]L2Entity, len(s.L2))
	for _, e := range s.L2 {
		m[e.Name] = e
	}
	return m
}

func indexVolumes(s Snapshot) map[string]Volume {
	m := make(map[string]Volume, len(s.Volumes))
	for _, v := range s.Volumes {
		m[v.Mount] = v
	}
	return m
}

func indexErrors(s Snapshot) map[string]SourceError {
	m := make(map[string]SourceError, len(s.Errors))
	for _, e := range s.Errors {
		m[e.Source] = e
	}
	return m
}

func diskLabel(d L1Disk) string {
	if d.Model == "" {
		return string(d.Status)
	}
	return d.Model
}

// sameCounterSource reports whether the counters of the two disk records
// came from the same source (or both records have none). Counters from
// different sources are not comparable — the same counter name carries
// different semantics (WMI reports normalized "health" values where
// smartctl reports sector counts) — so the diff re-baselines instead of
// comparing across a source switch.
func sameCounterSource(old, cur L1Disk) bool {
	return old.CountersSrc == cur.CountersSrc
}

func countersSuffix(old, cur L1Disk) string {
	if !sameCounterSource(old, cur) {
		return ""
	}
	if cur.Realloc != old.Realloc || cur.Pending != old.Pending {
		return fmt.Sprintf(", reallocated %d -> %d, pending %d -> %d",
			old.Realloc, cur.Realloc, old.Pending, cur.Pending)
	}
	if cur.MediaErrors != old.MediaErrors {
		return fmt.Sprintf(", media errors %d -> %d", old.MediaErrors, cur.MediaErrors)
	}
	return ""
}

func reasonSuffix(oldReason, curReason string) string {
	if curReason != "" && curReason != oldReason {
		return " (" + curReason + ")"
	}
	return ""
}

func volumeSize(v Volume) string {
	return fmt.Sprintf("всего %d ГБ, свободно %d ГБ (%.1f%%)",
		v.TotalBytes>>30, v.FreeBytes>>30, v.FreePct())
}

func spaceDetail(v Volume, th Thresholds) string {
	return fmt.Sprintf("место на исходе: %s, порог %d%% (минимум %d ГБ)",
		volumeSize(v), th.WarnPct, th.MinFreeGB)
}
