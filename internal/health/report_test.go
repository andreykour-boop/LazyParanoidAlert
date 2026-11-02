package health

import (
	"strings"
	"testing"
)

// TestSnapshotTextFiltersVendorNoteWithoutRAID: the Telegram report must
// not carry the permanent "vendor CLI not found" note on machines with no
// RAID arrays (the owner asked for a minimal report); real losses always
// show, and with arrays present the absent vendor CLI matters again.
// Sections without data print nothing — no "нет данных" placeholders, no
// empty-marker line.
func TestSnapshotTextFiltersVendorNoteWithoutRAID(t *testing.T) {
	th := Thresholds{WarnPct: 10}
	vendorNote := SourceError{Source: "vendor-cli", Reason: "vendor CLI (storcli/perccli/arcconf) не найден в PATH"}
	smartLoss := SourceError{Source: "smartctl", Reason: "smartctl: executable file not found"}

	// RAID-less machine, only the vendor note: nothing to report at all.
	noRAID := Snapshot{L1: []L1Disk{{Name: "sda", Status: StatusOK}}, Errors: []SourceError{vendorNote}}
	got := SnapshotText(noRAID, th)
	if strings.Contains(got, "vendor-cli") {
		t.Errorf("vendor-cli note must be dropped without RAID: %q", got)
	}
	if strings.Contains(got, "Не смог проверить") {
		t.Errorf("empty errors section must not print: %q", got)
	}
	if strings.Contains(got, "Массивы/тома") {
		t.Errorf("no arrays on the machine, section must not print: %q", got)
	}
	if !strings.Contains(got, "Диски:") || !strings.Contains(got, "sda") {
		t.Errorf("collected disks must show: %q", got)
	}

	// RAID-less machine, real loss: shows alongside the filter.
	noRAIDLoss := noRAID
	noRAIDLoss.Errors = []SourceError{vendorNote, smartLoss}
	if got := SnapshotText(noRAIDLoss, th); !strings.Contains(got, "smartctl") {
		t.Errorf("real loss must show: %q", got)
	}

	// RAID arrays present: the absent vendor CLI is a visibility gap.
	raid := noRAID
	raid.L2 = []L2Entity{{Name: "md0", Kind: "md", State: "clean"}}
	if got := SnapshotText(raid, th); !strings.Contains(got, "vendor-cli") {
		t.Errorf("vendor-cli note must show with RAID present: %q", got)
	}
}

// TestSnapshotTextNVMeCounters: NVMe counters render as "media errors N,
// wear N%" when present and stay silent when absent — no placeholders.
func TestSnapshotTextNVMeCounters(t *testing.T) {
	th := Thresholds{WarnPct: 10}
	snap := Snapshot{L1: []L1Disk{
		{Name: "nvme0", Model: "SSD AMD R3MP30512G8", Status: StatusOK, MediaErrors: 0, Wear: 11},
		{Name: "sdb", Model: "TOSHIBA MD04ACA600", Status: StatusOK},
	}}
	got := SnapshotText(snap, th)
	if !strings.Contains(got, "media errors 0, wear 11%") {
		t.Errorf("NVMe counters must render: %q", got)
	}
	if strings.Contains(got, "realloc") {
		t.Errorf("disk without counters must stay without counter text: %q", got)
	}

	// All-zero counters (fresh disk): nothing printed — same minimalism
	// rule as realloc/pending.
	empty := Snapshot{L1: []L1Disk{{Name: "nvme1", Status: StatusOK}}}
	if got := SnapshotText(empty, th); strings.Contains(got, "media errors") {
		t.Errorf("absent counters must not print: %q", got)
	}
}
