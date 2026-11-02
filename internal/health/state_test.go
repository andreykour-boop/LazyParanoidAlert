package health

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fullSnapshot builds a snapshot with every entity kind populated, so the
// round-trip test can catch lost fields of any layer.
func fullSnapshot(day int) Snapshot {
	snap := baseSnapshot(day)
	snap.L1[0].Serial = "WD-xyz"
	snap.L1[0].Realloc = 3
	snap.L1[0].Pending = 1
	snap.L2 = append(snap.L2, L2Entity{Name: "pool0", Kind: KindPool, State: L2Degraded, Reason: "disk 3 missing"})
	snap.Volumes = append(snap.Volumes,
		Volume{Mount: "/data", TotalBytes: gb(3600), FreeBytes: gb(288)},
		Volume{Mount: "Z:", TotalBytes: gb(10), FreeBytes: gb(1), ReadOnly: true},
	)
	snap.Errors = []SourceError{
		{Source: "storcli", Reason: "CLI not found in PATH"},
	}
	return snap
}

// TestStateRoundTrip: Save then Load must return the identical snapshot.
func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	snap := fullSnapshot(1)

	if err := SaveState(dir, snap); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, ok, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !ok {
		t.Fatal("LoadState reported absent after SaveState")
	}
	if !got.CollectedAt.Equal(snap.CollectedAt) {
		t.Errorf("CollectedAt = %v, want %v", got.CollectedAt, snap.CollectedAt)
	}
	if len(got.L1) != len(snap.L1) || got.L1[0] != snap.L1[0] {
		t.Errorf("L1 lost in round-trip: %+v vs %+v", got.L1, snap.L1)
	}
	if len(got.L2) != 2 || got.L2[1].State != L2Degraded || got.L2[1].Reason != "disk 3 missing" {
		t.Errorf("L2 lost in round-trip: %+v", got.L2)
	}
	if len(got.Volumes) != 3 || got.Volumes[2].ReadOnly != true {
		t.Errorf("Volumes lost in round-trip: %+v", got.Volumes)
	}
	if len(got.Errors) != 1 || got.Errors[0].Source != "storcli" {
		t.Errorf("Errors lost in round-trip: %+v", got.Errors)
	}
	// No temporary file left behind.
	if _, err := os.Stat(filepath.Join(dir, "health_state.json.tmp")); !os.IsNotExist(err) {
		t.Error("temporary state file left behind after SaveState")
	}
}

// TestStateMissingFile: first run on a fresh data dir is reported as
// absent, without an error.
// TestStateMissingFile: a missing state file is not an error, just "no
// previous snapshot".
func TestStateMissingFile(t *testing.T) {
	_, ok, err := LoadState(t.TempDir())
	if err != nil {
		t.Fatalf("LoadState on empty dir: %v", err)
	}
	if ok {
		t.Error("ok = true for missing state file")
	}
}

// TestSaveStateCreatesDataDir: a fresh install may have no data directory
// at all (a Linux kit without a pre-made one); SaveState must create it
// instead of failing and losing every snapshot.
func TestSaveStateCreatesDataDir(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "opt", "siren", "data") // nested: parent exists, data does not
	if err := SaveState(dataDir, Snapshot{CollectedAt: time.Now()}); err != nil {
		t.Fatalf("SaveState into missing data dir: %v", err)
	}
	if _, ok, err := LoadState(dataDir); err != nil || !ok {
		t.Fatalf("state unreadable after save into created dir: ok=%v err=%v", ok, err)
	}
}

// TestStateCorruptFile: a broken state file is an error, not a silent
// "no history" — losing the diff base must be visible to the operator.
func TestStateCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "health_state.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}
	if _, ok, err := LoadState(dir); err == nil {
		t.Errorf("expected error for corrupt state file, got ok=%v", ok)
	}
}

// TestVolumeJSONInGiB: the state file is human-readable — sizes are written
// as GiB with two decimals, not raw bytes; loading accepts both the GiB
// fields and the legacy byte fields written by older agents.
func TestVolumeJSONInGiB(t *testing.T) {
	v := Volume{Mount: "/", TotalBytes: 235149344768, FreeBytes: 212727684864} // ~219 GiB / ~198.1 GiB
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"total_gb":219`) || !strings.Contains(s, `"free_gb":198.1`) {
		t.Errorf("state file must show GiB, got %s", s)
	}
	if strings.Contains(s, "total_bytes") || strings.Contains(s, "free_bytes") {
		t.Errorf("raw byte fields must not be written: %s", s)
	}

	// Legacy byte-format state file still loads as a diff base.
	var legacy Volume
	if err := json.Unmarshal([]byte(`{"mount":"/","total_bytes":107374182400,"free_bytes":53687091200}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.TotalBytes != 100<<30 || legacy.FreeBytes != 50<<30 {
		t.Errorf("legacy bytes lost: %+v", legacy)
	}

	// GiB fields load back into bytes (two-decimal precision).
	var cur Volume
	if err := json.Unmarshal([]byte(`{"mount":"/","total_gb":219,"free_gb":198.1}`), &cur); err != nil {
		t.Fatal(err)
	}
	if cur.TotalBytes != 219*gib || cur.FreeBytes != gbToBytes(198.1) {
		t.Errorf("GiB fields lost: %+v", cur)
	}
}

// TestDiffAfterRestart simulates the restart path: snapshot saved, process
// "restarts", the agent collects a new snapshot and diffs it against the
// loaded one — the disk degradation that happened during the downtime must
// be found, and an unchanged machine must stay silent.
func TestDiffAfterRestart(t *testing.T) {
	dir := t.TempDir()

	// First run: healthy machine, state saved.
	first := fullSnapshot(1)
	first.L1[0].Status = StatusOK
	if err := SaveState(dir, first); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// "Restart": load the persisted snapshot as the diff base.
	prev, ok, err := LoadState(dir)
	if err != nil || !ok {
		t.Fatalf("LoadState after restart: ok=%v err=%v", ok, err)
	}

	// The disk degraded while the agent was down.
	cur := fullSnapshot(2)
	cur.L1[0].Status = StatusWarn
	cur.L1[0].Realloc = 12

	changes := Diff(prev, cur, Thresholds{WarnPct: 10})
	assertKinds(t, changes, []string{"disk_status:sda"})

	// Unchanged machine after restart: silence.
	same := fullSnapshot(3)
	if changes := Diff(prev, same, Thresholds{WarnPct: 10}); len(changes) != 0 {
		t.Errorf("unchanged restart must be silent, got %v", chKinds(changes))
	}
}
