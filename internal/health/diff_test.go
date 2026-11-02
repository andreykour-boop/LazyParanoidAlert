package health

import (
	"testing"
	"time"
)

func snapAt(day int) time.Time {
	return time.Date(2026, 10, day, 9, 0, 0, 0, time.Local)
}

func gb(n uint64) uint64 { return n << 30 }

// chKinds extracts the ordered kind/entity list for assertions.
func chKinds(changes []Change) []string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = string(c.Kind) + ":" + c.Entity
	}
	return out
}

func assertKinds(t *testing.T, got []Change, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("changes = %v, want %v", chKinds(got), want)
	}
	for i, w := range want {
		if got := chKinds(got)[i]; got != w {
			t.Errorf("changes[%d] = %q, want %q", i, got, w)
		}
	}
}

func baseSnapshot(day int) Snapshot {
	return Snapshot{
		CollectedAt: snapAt(day),
		L1: []L1Disk{
			{Name: "sda", Model: "WDC WD40", Status: StatusOK},
			{Name: "sdb", Model: "WDC WD40", Status: StatusOK},
		},
		L2: []L2Entity{
			{Name: "md0", Kind: KindMD, State: L2Clean},
		},
		Volumes: []Volume{
			{Mount: "/", TotalBytes: gb(100), FreeBytes: gb(50)},
		},
	}
}

// TestDiffFirstRun: no previous snapshot (zero CollectedAt) means no changes
// — the start-up rule is silence, never an "all is well" report.
func TestDiffFirstRun(t *testing.T) {
	var prev Snapshot
	changes := Diff(prev, baseSnapshot(2), Thresholds{WarnPct: 10})
	if len(changes) != 0 {
		t.Errorf("first run must be silent, got %v", chKinds(changes))
	}
}

// TestDiffIdenticalSnapshots: equal snapshots produce no changes.
func TestDiffIdenticalSnapshots(t *testing.T) {
	changes := Diff(baseSnapshot(1), baseSnapshot(2), Thresholds{WarnPct: 10})
	if len(changes) != 0 {
		t.Errorf("identical snapshots must be silent, got %v", chKinds(changes))
	}
}

// TestDiffDiskStatusChange: SMART status flip is reported with old and new
// status and the counter movement.
func TestDiffDiskStatusChange(t *testing.T) {
	prev := baseSnapshot(1)
	cur := baseSnapshot(2)
	cur.L1[1].Status = StatusWarn
	cur.L1[1].Realloc = 4
	cur.L1[1].Pending = 1

	changes := Diff(prev, cur, Thresholds{WarnPct: 10})
	assertKinds(t, changes, []string{"disk_status:sdb"})
	if changes[0].Detail == "" {
		t.Error("status change must carry a human-readable detail")
	}
}

// TestDiffDiskGoneAndNew: disappearance of a previously visible disk is
// reported; so is a newly appeared one.
func TestDiffDiskGoneAndNew(t *testing.T) {
	prev := baseSnapshot(1)
	cur := baseSnapshot(2)
	cur.L1 = cur.L1[:1] // sdb disappeared

	assertKinds(t, Diff(prev, cur, Thresholds{WarnPct: 10}), []string{"disk_gone:sdb"})

	cur2 := cur
	cur2.L1 = append(cur2.L1, L1Disk{Name: "sdc", Model: "NEW SSD", Status: StatusOK})
	assertKinds(t, Diff(cur, cur2, Thresholds{WarnPct: 10}), []string{"disk_new:sdc"})
}

// TestDiffReallocGrowth: realloc increase with the status still OK is the
// realloc_growth alarm; unchanged or decreased counters and the first
// snapshot are silent.
func TestDiffReallocGrowth(t *testing.T) {
	prev := baseSnapshot(1)
	cur := baseSnapshot(2)
	cur.L1[0].Realloc = 482 // prev sda has 0? no: set both sides explicitly
	prev.L1[0].Realloc = 481

	changes := Diff(prev, cur, Thresholds{WarnPct: 10})
	assertKinds(t, changes, []string{"realloc_growth:sda"})
	if changes[0].Detail != "диск sda: realloc 481 -> 482" {
		t.Errorf("detail = %q, want old -> new values", changes[0].Detail)
	}

	// Unchanged counter: silent.
	same := baseSnapshot(3)
	same.L1[0].Realloc = 482
	assertKinds(t, Diff(cur, same, Thresholds{WarnPct: 10}), nil)

	// Decrease is physically impossible; ignore rather than alarm.
	cur2 := baseSnapshot(4)
	cur2.L1[0].Realloc = 480
	assertKinds(t, Diff(same, cur2, Thresholds{WarnPct: 10}), nil)

	// First snapshot (no saved state): silent even with a high counter.
	var none Snapshot
	first := baseSnapshot(5)
	first.L1[0].Realloc = 900
	assertKinds(t, Diff(none, first, Thresholds{WarnPct: 10}), nil)
}

// TestDiffReallocGrowthScoping: appearing and disappearing disks never
// produce realloc_growth, and a growth alongside a status flip stays a
// single disk_status change (the counters are in its detail already).
func TestDiffReallocGrowthScoping(t *testing.T) {
	prev := baseSnapshot(1)
	cur := baseSnapshot(2)
	cur.L1 = []L1Disk{{Name: "sdc", Model: "NEW SSD", Status: StatusOK, Realloc: 3}}
	// The current-snapshot loop reports appearances first, then the
	// previous-snapshot loop reports disappearances.
	assertKinds(t, Diff(prev, cur, Thresholds{WarnPct: 10}),
		[]string{"disk_new:sdc", "disk_gone:sda", "disk_gone:sdb"})

	// Status flip + growth: one disk_status change, counters in the suffix.
	prev2 := baseSnapshot(1)
	cur2 := baseSnapshot(2)
	prev2.L1[0].Realloc = 481
	cur2.L1[0].Status = StatusWarn
	cur2.L1[0].Realloc = 482
	assertKinds(t, Diff(prev2, cur2, Thresholds{WarnPct: 10}), []string{"disk_status:sda"})
}

// TestDiffMediaErrorGrowth: NVMe media-error increase with the status
// still OK is the media_error_growth alarm; unchanged or decreased values
// and the first snapshot are silent.
func TestDiffMediaErrorGrowth(t *testing.T) {
	prev := baseSnapshot(1)
	cur := baseSnapshot(2)
	prev.L1[0].MediaErrors = 0
	cur.L1[0].MediaErrors = 3

	changes := Diff(prev, cur, Thresholds{WarnPct: 10})
	assertKinds(t, changes, []string{"media_error_growth:sda"})
	if changes[0].Detail != "диск sda: media errors 0 -> 3" {
		t.Errorf("detail = %q, want old -> new values", changes[0].Detail)
	}

	// Unchanged value: silent.
	same := baseSnapshot(3)
	same.L1[0].MediaErrors = 3
	assertKinds(t, Diff(cur, same, Thresholds{WarnPct: 10}), nil)

	// Decrease: ignored rather than alarmed.
	cur2 := baseSnapshot(4)
	cur2.L1[0].MediaErrors = 2
	assertKinds(t, Diff(same, cur2, Thresholds{WarnPct: 10}), nil)

	// First snapshot: silent even with a high counter.
	var none Snapshot
	first := baseSnapshot(5)
	first.L1[0].MediaErrors = 900
	assertKinds(t, Diff(none, first, Thresholds{WarnPct: 10}), nil)
}

// TestDiffRebaselineOnSourceChange: counters from different sources carry
// different semantics, so a source switch re-baselines silently (no growth
// alarm in either direction) and real growth after the switch is caught on
// the new base. An unstamped old state (previous agent version) is treated
// as a source switch too.
func TestDiffRebaselineOnSourceChange(t *testing.T) {
	// smartctl -> wmi: 0 -> 100 looks like growth but is a semantics flip;
	// no alarm, no counters in the status suffix.
	prev := baseSnapshot(1)
	cur := baseSnapshot(2)
	prev.L1[0].CountersSrc = "smartctl"
	prev.L1[0].Realloc = 0
	cur.L1[0].CountersSrc = "wmi"
	cur.L1[0].Realloc = 100
	assertKinds(t, Diff(prev, cur, Thresholds{WarnPct: 10}), nil)

	// wmi -> smartctl: 100 -> 0, silent as well.
	assertKinds(t, Diff(cur, prev, Thresholds{WarnPct: 10}), nil)

	// After the switch, real growth on the new base alarms.
	next := baseSnapshot(3)
	next.L1[0].CountersSrc = "smartctl"
	next.L1[0].Realloc = 1
	assertKinds(t, Diff(prev, next, Thresholds{WarnPct: 10}),
		[]string{"realloc_growth:sda"})

	// Media errors after the switch: same rule.
	mediaPrev := baseSnapshot(4)
	mediaPrev.L1[0].CountersSrc = "wmi"
	mediaPrev.L1[0].MediaErrors = 50
	mediaCur := baseSnapshot(5)
	mediaCur.L1[0].CountersSrc = "smartctl"
	mediaCur.L1[0].MediaErrors = 0
	assertKinds(t, Diff(mediaPrev, mediaCur, Thresholds{WarnPct: 10}), nil)

	// Old state without a source stamp (agent < add-smartctl-sources):
	// re-baseline, no alarm.
	oldState := baseSnapshot(6)
	oldState.L1[0].Realloc = 42
	newState := baseSnapshot(7)
	newState.L1[0].CountersSrc = "smartctl"
	newState.L1[0].Realloc = 0
	assertKinds(t, Diff(oldState, newState, Thresholds{WarnPct: 10}), nil)
}

// TestDiffArrayStates: clean -> degraded -> rebuilding -> clean transitions
// are all reported as array_status changes.
func TestDiffArrayStates(t *testing.T) {
	prev := baseSnapshot(1)

	degraded := baseSnapshot(2)
	degraded.L2[0].State = L2Degraded
	degraded.L2[0].Reason = "sda1 failed"
	changes := Diff(prev, degraded, Thresholds{WarnPct: 10})
	assertKinds(t, changes, []string{"array_status:md0"})
	if changes[0].Detail == "" {
		t.Error("array state change must carry detail")
	}

	rebuilding := baseSnapshot(3)
	rebuilding.L2[0].State = L2Rebuilding
	assertKinds(t, Diff(degraded, rebuilding, Thresholds{WarnPct: 10}), []string{"array_status:md0"})

	clean := baseSnapshot(4)
	assertKinds(t, Diff(rebuilding, clean, Thresholds{WarnPct: 10}), []string{"array_status:md0"})
}

// TestDiffArrayGoneAndNew.
func TestDiffArrayGoneAndNew(t *testing.T) {
	prev := baseSnapshot(1)
	cur := baseSnapshot(2)
	cur.L2 = nil
	assertKinds(t, Diff(prev, cur, Thresholds{WarnPct: 10}), []string{"array_gone:md0"})

	cur2 := cur
	cur2.L2 = []L2Entity{{Name: "md1", Kind: KindMD, State: L2Clean}}
	assertKinds(t, Diff(cur, cur2, Thresholds{WarnPct: 10}), []string{"array_new:md1"})
}

// TestDiffVolumeGoneAndNew.
func TestDiffVolumeGoneAndNew(t *testing.T) {
	prev := baseSnapshot(1)
	cur := baseSnapshot(2)
	cur.Volumes = nil
	assertKinds(t, Diff(prev, cur, Thresholds{WarnPct: 10}), []string{"volume_gone:/"})

	cur2 := cur
	cur2.Volumes = []Volume{{Mount: "/data", TotalBytes: gb(100), FreeBytes: gb(1)}} // 1% free
	// A newly appeared already-low volume reports both the appearance and
	// the violation.
	assertKinds(t, Diff(cur, cur2, Thresholds{WarnPct: 10}),
		[]string{"volume_new:/data", "space_low:/data"})
}

// TestDiffSpaceThresholds: a volume crossing below warn_pct, or below the
// absolute min_free_gb floor, is reported once; staying low is NOT a new
// change (reminders are the runner's daily job), and recovering is silent.
func TestDiffSpaceThresholds(t *testing.T) {
	// 3.6 TB volume with 8% free crosses the 10% threshold.
	prev := baseSnapshot(1)
	prev.Volumes = []Volume{{Mount: "D:", TotalBytes: gb(3600), FreeBytes: gb(500)}}
	cur := baseSnapshot(2)
	cur.Volumes = []Volume{{Mount: "D:", TotalBytes: gb(3600), FreeBytes: gb(288)}} // 8%

	changes := Diff(prev, cur, Thresholds{WarnPct: 10})
	assertKinds(t, changes, []string{"space_low:D:"})
	if changes[0].Detail == "" {
		t.Error("space alert must carry detail with sizes and threshold")
	}

	// Still low next day: no new change from Diff (reminder path handles it).
	still := baseSnapshot(3)
	still.Volumes = []Volume{{Mount: "D:", TotalBytes: gb(3600), FreeBytes: gb(100)}}
	assertKinds(t, Diff(cur, still, Thresholds{WarnPct: 10}), nil)

	// Absolute floor: free space above pct threshold but below 20 GiB.
	abovePct := baseSnapshot(4)
	abovePct.Volumes = []Volume{{Mount: "C:", TotalBytes: gb(1000), FreeBytes: gb(25)}} // 2.5%
	// 2.5% < 10% triggers pct; use a volume big enough for pct to pass:
	okPct := baseSnapshot(5)
	okPct.Volumes = []Volume{{Mount: "C:", TotalBytes: gb(100000), FreeBytes: gb(25)}} // 0.025% — no
	// Simplify: pct 1, floor 20 GiB, free 25 GiB of 100000 GiB (0.025% >= 1%? no).
	// Use free 2000 GiB of 100000 GiB: 2% < 10% pct triggers; set WarnPct=1.
	okPct.Volumes = []Volume{{Mount: "C:", TotalBytes: gb(100000), FreeBytes: gb(2000)}} // 2%
	lowAbs := baseSnapshot(6)
	lowAbs.Volumes = []Volume{{Mount: "C:", TotalBytes: gb(100000), FreeBytes: gb(15)}} // 0.015%, below 20 GiB floor
	changes = Diff(okPct, lowAbs, Thresholds{WarnPct: 1, MinFreeGB: 20})
	assertKinds(t, changes, []string{"space_low:C:"})

	// Recovering above the threshold is silent.
	recovered := baseSnapshot(7)
	recovered.Volumes = []Volume{{Mount: "D:", TotalBytes: gb(3600), FreeBytes: gb(1000)}}
	assertKinds(t, Diff(cur, recovered, Thresholds{WarnPct: 10}), nil)
}

// TestSpaceViolations: the reminder path lists every currently-low volume,
// read-only volumes excluded.
func TestSpaceViolations(t *testing.T) {
	snap := Snapshot{CollectedAt: snapAt(1), Volumes: []Volume{
		{Mount: "C:", TotalBytes: gb(100), FreeBytes: gb(5)},                 // 5% < 10%
		{Mount: "D:", TotalBytes: gb(100), FreeBytes: gb(50)},                // fine
		{Mount: "E:", TotalBytes: gb(100), FreeBytes: gb(2), ReadOnly: true}, // low but read-only
	}}
	viol := SpaceViolations(snap, Thresholds{WarnPct: 10})
	if len(viol) != 1 || viol[0].Mount != "C:" {
		t.Errorf("violations = %+v, want only C:", viol)
	}

	// Absolute floor alone: 15 GiB free (15%) with a 20 GiB floor.
	snap.Volumes = []Volume{{Mount: "C:", TotalBytes: gb(100), FreeBytes: gb(15)}}
	viol = SpaceViolations(snap, Thresholds{WarnPct: 10, MinFreeGB: 20})
	if len(viol) != 1 {
		t.Errorf("absolute floor violation missed: %+v", viol)
	}
}

// TestDiffSourceLost: a source collected yesterday but failing today is the
// "lost visibility" alarm; a source failing in both snapshots is not news.
func TestDiffSourceLost(t *testing.T) {
	prev := baseSnapshot(1)

	cur := baseSnapshot(2)
	cur.Errors = []SourceError{{Source: "smartctl", Reason: "executable not found"}}
	assertKinds(t, Diff(prev, cur, Thresholds{WarnPct: 10}), []string{"source_lost:smartctl"})

	// Source already lost before: staying lost is not a new change.
	still := baseSnapshot(3)
	still.Errors = []SourceError{{Source: "smartctl", Reason: "still missing"}}
	assertKinds(t, Diff(cur, still, Thresholds{WarnPct: 10}), nil)

	// Source recovered: silent; the weekly report shows it collected again.
	assertKinds(t, Diff(still, baseSnapshot(4), Thresholds{WarnPct: 10}), nil)
}
