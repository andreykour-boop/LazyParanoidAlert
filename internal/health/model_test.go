package health

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// fakeL2 / fakeL1 / fakeVolumes are fixed in-memory sources for tests.
type fakeL2 struct {
	name     string
	entities []L2Entity
	err      error
}

func (f fakeL2) Name() string { return f.name }
func (f fakeL2) CollectL2() ([]L2Entity, error) {
	if f.err != nil {
		return nil, f.err
	}
	// Return a copy so the collector cannot mutate the fixture.
	out := make([]L2Entity, len(f.entities))
	copy(out, f.entities)
	return out, nil
}

type fakeL1 struct {
	name  string
	disks []L1Disk
	err   error
}

func (f fakeL1) Name() string { return f.name }
func (f fakeL1) CollectL1() ([]L1Disk, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]L1Disk, len(f.disks))
	copy(out, f.disks)
	return out, nil
}

type fakeVolumes struct {
	name string
	vols []Volume
	err  error
}

func (f fakeVolumes) Name() string { return f.name }
func (f fakeVolumes) CollectVolumes() ([]Volume, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]Volume, len(f.vols))
	copy(out, f.vols)
	return out, nil
}

func fixedNow() time.Time { return time.Date(2026, 10, 30, 9, 0, 0, 0, time.Local) }

// TestSnapshotFromFixedSources: the collector assembles entities from all
// sources, stamps the snapshot and sorts lists deterministically.
func TestSnapshotFromFixedSources(t *testing.T) {
	c := &Collector{
		L2Sources: []L2Source{
			fakeL2{name: "mdadm", entities: []L2Entity{
				{Name: "md1", Kind: KindMD, State: L2Degraded, Reason: "sda1 missing"},
				{Name: "md0", Kind: KindMD, State: L2Clean},
			}},
		},
		L1Sources: []L1Source{
			fakeL1{name: "smartctl", disks: []L1Disk{
				{Name: "sdb", Model: "WDC WD40EFRX", Serial: "WD-abc", Status: StatusOK},
				{Name: "sda", Model: "WDC WD40EFRX", Serial: "WD-xyz", Status: StatusWarn, Realloc: 8, Pending: 2},
			}},
			// A second L1 source, e.g. vendor CLI behind a RAID controller.
			fakeL1{name: "storcli", disks: []L1Disk{
				{Name: "megaraid:0", Model: "ST6000NM", Status: StatusOK},
			}},
		},
		VolumesSources: []VolumesSource{
			fakeVolumes{name: "statfs", vols: []Volume{
				{Mount: "/home", TotalBytes: 100 << 30, FreeBytes: 5 << 30},
				{Mount: "/", TotalBytes: 50 << 30, FreeBytes: 25 << 30},
			}},
		},
		Now: fixedNow,
	}

	snap := c.Snapshot()

	if !snap.CollectedAt.Equal(fixedNow()) {
		t.Errorf("CollectedAt = %v, want %v", snap.CollectedAt, fixedNow())
	}
	if len(snap.Errors) != 0 {
		t.Errorf("Errors = %+v, want none", snap.Errors)
	}
	if len(snap.L2) != 2 || snap.L2[0].Name != "md0" || snap.L2[1].Name != "md1" {
		t.Errorf("L2 not merged and sorted: %+v", snap.L2)
	}
	if len(snap.L1) != 3 || snap.L1[0].Name != "megaraid:0" || snap.L1[1].Name != "sda" || snap.L1[2].Name != "sdb" {
		t.Errorf("L1 not merged and sorted: %+v", snap.L1)
	}
	if snap.L1[1].Realloc != 8 || snap.L1[1].Pending != 2 {
		t.Errorf("SMART counters lost: %+v", snap.L1[1])
	}
	if len(snap.Volumes) != 2 || snap.Volumes[0].Mount != "/" || snap.Volumes[1].Mount != "/home" {
		t.Errorf("Volumes not merged and sorted: %+v", snap.Volumes)
	}
	if got := snap.Volumes[0].FreePct(); got != 50 {
		t.Errorf("FreePct(/) = %v, want 50", got)
	}
}

// TestSnapshotSourceFailureBecomesError: a failing source must not vanish —
// it contributes to Errors with its name and reason (the "do not stay
// silent" rule), while healthy sources still populate the snapshot.
func TestSnapshotSourceFailureBecomesError(t *testing.T) {
	c := &Collector{
		L2Sources: []L2Source{
			fakeL2{name: "mdadm", err: errors.New("mdadm: command not found")},
		},
		L1Sources: []L1Source{
			fakeL1{name: "smartctl", err: errors.New("smartctl: executable file not found in $PATH")},
			fakeL1{name: "wmi", disks: []L1Disk{{Name: "PhysicalDisk0", Status: StatusOK}}},
		},
		VolumesSources: []VolumesSource{
			fakeVolumes{name: "statfs", err: errors.New("permission denied")},
		},
		Now: fixedNow,
	}

	snap := c.Snapshot()

	if len(snap.L2) != 0 {
		t.Errorf("failed source must contribute no entities, got %+v", snap.L2)
	}
	wantErrs := map[string]string{
		"mdadm":    "mdadm: command not found",
		"smartctl": "smartctl: executable file not found in $PATH",
		"statfs":   "permission denied",
	}
	if len(snap.Errors) != len(wantErrs) {
		t.Fatalf("Errors = %+v, want %d entries", snap.Errors, len(wantErrs))
	}
	for _, e := range snap.Errors {
		reason, ok := wantErrs[e.Source]
		if !ok {
			t.Errorf("unexpected error source %q", e.Source)
			continue
		}
		if e.Reason != reason {
			t.Errorf("Errors[%s].Reason = %q, want %q", e.Source, e.Reason, reason)
		}
	}
	// The healthy L1 source must still be in the snapshot.
	if len(snap.L1) != 1 || snap.L1[0].Name != "PhysicalDisk0" {
		t.Errorf("healthy source lost: %+v", snap.L1)
	}
}

// TestL1DiskStateCompatibility: a state file written by an older agent
// (no media_errors/wear/counters_src) still loads as a diff base, and the
// new fields round-trip through JSON.
func TestL1DiskStateCompatibility(t *testing.T) {
	// Old state: only the fields known before add-smartctl-sources.
	const oldJSON = `{"name":"sda","model":"TOSHIBA MD04ACA600","serial":"95NHK0IZFFQC","status":"ok","realloc":481,"pending":2}`
	var old L1Disk
	if err := json.Unmarshal([]byte(oldJSON), &old); err != nil {
		t.Fatalf("old state load: %v", err)
	}
	if old.Realloc != 481 || old.Pending != 2 || old.MediaErrors != 0 || old.Wear != 0 || old.CountersSrc != "" {
		t.Errorf("old state parsed wrong: %+v", old)
	}

	// New fields round-trip.
	cur := L1Disk{Name: "nvme0", Status: StatusOK, MediaErrors: 3, Wear: 11, CountersSrc: "smartctl"}
	raw, err := json.Marshal(cur)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back L1Disk
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back != cur {
		t.Errorf("round-trip = %+v, want %+v", back, cur)
	}
	// Empty counters stay absent in the JSON (no placeholder noise).
	minimal, _ := json.Marshal(L1Disk{Name: "sdb", Status: StatusOK})
	if string(minimal) != `{"name":"sdb","status":"ok"}` {
		t.Errorf("minimal disk JSON = %s, want no counter fields", minimal)
	}
}
