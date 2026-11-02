package health

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixture: SATA disk, healthy, no bad sectors.
const smartctlSATAOK = `smartctl 7.2 2020-12-30 r5155 [x86_64-linux-5.15.0] (local build)
Copyright (C) 2002-20, Bruce Allen, Michael Fine, Christian Franke

=== START OF INFORMATION SECTION ===
Device Model:     WDC WD40EFRX-68N32N0
Serial Number:    WD-WCC4E1234567
Firmware Version: 82.00A82
User Capacity:    4,000,787,030,016 bytes [4.00 TB]
Sector Size:      512 bytes logical/physical

=== START OF READ SMART DATA SECTION ===
SMART overall-health self-assessment test result: PASSED

ID# ATTRIBUTE_NAME          FLAG     VALUE WORST THRESH TYPE      UPDATED  WHEN_FAILED RAW_VALUE
  1 Raw_Read_Error_Rate     0x002f   200   200   051    Pre-fail  Always   -       0
  5 Reallocated_Sector_Ct   0x0033   200   200   140    Pre-fail  Always   -       0
197 Current_Pending_Sector  0x0022   200   200   000    Old_age   Always   -       0
`

// Fixture: SATA disk failing with reallocated and pending sectors.
const smartctlSATAFail = `smartctl 7.2 2020-12-30 r5155 [x86_64-linux-5.15.0] (local build)

=== START OF INFORMATION SECTION ===
Device Model:     ST4000DM000-1F2168
Serial Number:    Z301ABCDEF

=== START OF READ SMART DATA SECTION ===
SMART overall-health self-assessment test result: FAILED!
Drive failure expected in less than 24 hours. SAVE ALL DATA.

ID# ATTRIBUTE_NAME          FLAG     VALUE WORST THRESH TYPE      UPDATED  WHEN_FAILED RAW_VALUE
  5 Reallocated_Sector_Ct   0x0033   087   087   140    Pre-fail  Always   -       788
197 Current_Pending_Sector  0x0022   099   099   000    Old_age   Always   -       12
`

// Fixture: NVMe disk (no sector-count attributes by design), with the
// health-log counters the watch consumes. Large numbers appear with locale
// thousands separators in text mode — the parser must not choke on them.
const smartctlNVMeOK = `smartctl 7.2 2020-12-30 r5155 [x86_64-linux-5.15.0] (local build)

=== START OF INFORMATION SECTION ===
Model Number:                       Samsung SSD 980 PRO 1TB
Serial Number:                      S5GXNX0R123456

=== START OF SMART DATA SECTION ===
SMART overall-health self-assessment test result: PASSED
Critical Warning:                   0x00
Available Spare:                    100%
Percentage Used:                    11%
Data Units Read:                    17,785,059 [9.10 TB]
Media and Data Integrity Errors:    0
Error Information Log Entries:      0
`

// Fixture: NVMe disk with degraded counters and separator-formatted values.
const smartctlNVMEDegraded = `smartctl 7.5 2025-04-30 r5714 [x86_64-linux-7.0.0-38-generic] (local build)

=== START OF INFORMATION SECTION ===
Model Number:                       Samsung SSD 980 PRO 1TB
Serial Number:                      S5GXNX0R123456

=== START OF SMART DATA SECTION ===
SMART overall-health self-assessment test result: PASSED
Percentage Used:                    42%
Media and Data Integrity Errors:    1,234
`

func TestParseSmartctlSATAOK(t *testing.T) {
	disk, err := parseSmartctl("sda", smartctlSATAOK)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if disk.Model != "WDC WD40EFRX-68N32N0" || disk.Serial != "WD-WCC4E1234567" {
		t.Errorf("identity parsed wrong: %+v", disk)
	}
	if disk.Status != StatusOK {
		t.Errorf("Status = %q, want ok", disk.Status)
	}
	if disk.Realloc != 0 || disk.Pending != 0 {
		t.Errorf("counters = %d/%d, want 0/0", disk.Realloc, disk.Pending)
	}
}

func TestParseSmartctlSATAFail(t *testing.T) {
	disk, err := parseSmartctl("sdb", smartctlSATAFail)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if disk.Status != StatusFail {
		t.Errorf("Status = %q, want fail", disk.Status)
	}
	if disk.Realloc != 788 || disk.Pending != 12 {
		t.Errorf("counters = %d/%d, want 788/12", disk.Realloc, disk.Pending)
	}
	if disk.Reason == "" {
		t.Error("fail status must carry the smartctl reason line")
	}
}

func TestParseSmartctlNVMeOK(t *testing.T) {
	disk, err := parseSmartctl("nvme0n1", smartctlNVMeOK)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if disk.Model != "Samsung SSD 980 PRO 1TB" || disk.Serial != "S5GXNX0R123456" {
		t.Errorf("identity parsed wrong: %+v", disk)
	}
	if disk.Status != StatusOK {
		t.Errorf("Status = %q, want ok", disk.Status)
	}
	if disk.MediaErrors != 0 || disk.Wear != 11 {
		t.Errorf("counters = media %d wear %d, want 0/11", disk.MediaErrors, disk.Wear)
	}
	if disk.Realloc != 0 || disk.Pending != 0 {
		t.Errorf("ATA counters must stay zero for NVMe: %+v", disk)
	}
	if disk.CountersSrc != "smartctl" {
		t.Errorf("CountersSrc = %q, want smartctl", disk.CountersSrc)
	}
}

// TestParseSmartctlNVMEDegraded: separator-formatted counters parse cleanly
// ("1,234" -> 1234, "42%" -> 42); a misparse must not invent values.
func TestParseSmartctlNVMEDegraded(t *testing.T) {
	disk, err := parseSmartctl("nvme0n1", smartctlNVMEDegraded)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if disk.MediaErrors != 1234 || disk.Wear != 42 {
		t.Errorf("counters = media %d wear %d, want 1234/42", disk.MediaErrors, disk.Wear)
	}
	if disk.CountersSrc != "smartctl" {
		t.Errorf("CountersSrc = %q, want smartctl", disk.CountersSrc)
	}
}

// TestParseSMARTNumber: separators, percent signs and bracket summaries
// are stripped before parsing; garbage stays unparsed.
func TestParseSMARTNumber(t *testing.T) {
	cases := map[string]struct {
		want int64
		ok   bool
	}{
		"0":                    {0, true},
		"1,234":                {1234, true},
		"17,785,059 [9.10 TB]": {17785059, true},
		"14 974":               {14974, true},
		"42%":                  {42, true},
		"11":                   {11, true},
		"":                     {0, false},
		"n/a":                  {0, false},
	}
	for in, w := range cases {
		got, ok := parseSMARTNumber(in)
		if ok != w.ok || (ok && got != w.want) {
			t.Errorf("parseSMARTNumber(%q) = %d/%v, want %d/%v", in, got, ok, w.want, w.ok)
		}
	}
}

func TestParseSmartctlUnknownOutput(t *testing.T) {
	_, err := parseSmartctl("sdc", "smartctl: unknown output with no health line\n")
	if err == nil {
		t.Fatal("expected error for unrecognized output")
	}
}

// makeFakeSysBlock builds a /sys/block-style fixture tree with the given
// device names and removable attributes.
func makeFakeSysBlock(t *testing.T, devices map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, removable := range devices {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "removable"), []byte(removable+"\n"), 0o644); err != nil {
			t.Fatalf("write removable: %v", err)
		}
	}
	return root
}

func TestListBlockDevices(t *testing.T) {
	root := makeFakeSysBlock(t, map[string]string{
		"sda":     "0", // real disk
		"sdb":     "1", // removable -> skip
		"nvme0n1": "0", // NVMe disk
		"vda":     "0", // virtio disk
		"loop0":   "0", // loop -> skip
		"ram0":    "0", // RAM -> skip
		"zram0":   "0", // zram -> skip
		"sr0":     "0", // CD/DVD -> skip
		"md0":     "0", // software array -> skip (L2)
		"dm-0":    "0", // device-mapper/LVM -> skip (non-goal)
		"fd0":     "0", // floppy -> skip
	})

	devices, err := listBlockDevices(root)
	if err != nil {
		t.Fatalf("listBlockDevices: %v", err)
	}
	want := []string{"nvme0n1", "sda", "vda"}
	if strings.Join(devices, ",") != strings.Join(want, ",") {
		t.Errorf("devices = %v, want %v", devices, want)
	}
}

func TestListBlockDevicesMissingRoot(t *testing.T) {
	if _, err := listBlockDevices(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("expected error for missing /sys/block root")
	}
}

// TestSmartctlSourceCollect: healthy parse path, per-device failure marked
// unknown with reason (the device stays visible), missing tool reported as
// a source error.
func TestSmartctlSourceCollect(t *testing.T) {
	s := &smartctlSource{
		listDevices: func() ([]string, error) { return []string{"sda", "sdb", "sdc"}, nil },
		run: func(device string) (string, error) {
			switch device {
			case "sda":
				return smartctlSATAOK, nil
			case "sdb":
				return smartctlSATAFail, errors.New("exit status 4") // smartctl signals failing health via exit code
			case "sdc":
				return "", errors.New("exit status 2") // device open failed, no output
			}
			return "", errors.New("unexpected device")
		},
	}

	disks, err := s.CollectL1()
	if err != nil {
		t.Fatalf("CollectL1: %v", err)
	}
	if len(disks) != 3 {
		t.Fatalf("disks = %+v, want 3 entries", disks)
	}
	if disks[0].Status != StatusOK || disks[0].Name != "sda" {
		t.Errorf("sda parsed wrong: %+v", disks[0])
	}
	if disks[1].Status != StatusFail || disks[1].Realloc != 788 {
		t.Errorf("sdb parsed wrong: %+v", disks[1])
	}
	if disks[2].Status != StatusUnknown || disks[2].Reason == "" {
		t.Errorf("sdc must be unknown with reason: %+v", disks[2])
	}
}

func TestSmartctlSourceDeviceListError(t *testing.T) {
	s := &smartctlSource{
		listDevices: func() ([]string, error) { return nil, errors.New("/sys/block unreadable") },
		run:         func(string) (string, error) { return "", nil },
	}
	if _, err := s.CollectL1(); err == nil {
		t.Fatal("expected error when the device list is unreadable")
	}
}
