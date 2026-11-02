//go:build windows

package health

import (
	"errors"
	"testing"
)

// Fixture: real `smartctl -i -H -A -j /dev/sdc` output for the NVMe disk of
// the live machine (trimmed to the fields the parser consumes).
const smartctlJSONNVMe = `{
  "model_name": "SSD AMD R3MP30512G8 512GB",
  "serial_number": "01162225A0661",
  "smart_status": {
    "passed": true,
    "nvme": {"value": 0}
  },
  "nvme_smart_health_information_log": {
    "critical_warning": 0,
    "temperature": 41,
    "available_spare": 100,
    "available_spare_threshold": 1,
    "percentage_used": 11,
    "power_on_hours": 14976,
    "unsafe_shutdowns": 24,
    "media_errors": 0,
    "num_err_log_entries": 0
  }
}`

// Fixture: real `smartctl -i -H -A -j /dev/sdb` output for the SATA disk of
// the live machine (attribute table trimmed; raw values are honest zeros —
// the same counters WMI reports as normalized 100).
const smartctlJSONSATA = `{
  "model_family": "Toshiba MD04ACA... Enterprise HDD",
  "model_name": "TOSHIBA MD04ACA600",
  "serial_number": "95NHK0IZFFQC",
  "smart_status": {"passed": true},
  "ata_smart_attributes": {
    "revision": 16,
    "table": [
      {"id": 9, "name": "Power_On_Hours", "value": 1, "worst": 1,
       "raw": {"value": 41310, "string": "41310"}},
      {"id": 5, "name": "Reallocated_Sector_Ct", "value": 100, "worst": 100,
       "raw": {"value": 0, "string": "0"}},
      {"id": 197, "name": "Current_Pending_Sector", "value": 100, "worst": 100,
       "raw": {"value": 0, "string": "0"}}
    ]
  }
}`

// Fixture: NVMe disk with nonzero degradation counters.
const smartctlJSONNVMEDegraded = `{
  "model_name": "SSD TEST",
  "serial_number": "TEST01",
  "smart_status": {"passed": false},
  "nvme_smart_health_information_log": {
    "critical_warning": 4,
    "percentage_used": 99,
    "media_errors": 17408
  }
}`

func TestParseSmartctlJSONNVMe(t *testing.T) {
	disk, err := parseSmartctlJSON("/dev/sdc", smartctlJSONNVMe)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if disk.Model != "SSD AMD R3MP30512G8 512GB" || disk.Serial != "01162225A0661" {
		t.Errorf("identity parsed wrong: %+v", disk)
	}
	if disk.Status != StatusOK || disk.Reason != "" {
		t.Errorf("Status = %q/%q, want ok", disk.Status, disk.Reason)
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

func TestParseSmartctlJSONSATA(t *testing.T) {
	disk, err := parseSmartctlJSON("/dev/sdb", smartctlJSONSATA)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if disk.Model != "TOSHIBA MD04ACA600" || disk.Serial != "95NHK0IZFFQC" {
		t.Errorf("identity parsed wrong: %+v", disk)
	}
	if disk.Status != StatusOK {
		t.Errorf("Status = %q, want ok", disk.Status)
	}
	if disk.Realloc != 0 || disk.Pending != 0 {
		t.Errorf("counters = %d/%d, want honest raw 0/0", disk.Realloc, disk.Pending)
	}
	if disk.MediaErrors != 0 || disk.Wear != 0 {
		t.Errorf("NVMe fields must stay zero for SATA: %+v", disk)
	}
	if disk.CountersSrc != "smartctl" {
		t.Errorf("CountersSrc = %q, want smartctl", disk.CountersSrc)
	}
}

func TestParseSmartctlJSONDegraded(t *testing.T) {
	disk, err := parseSmartctlJSON("nvme0", smartctlJSONNVMEDegraded)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if disk.Status != StatusFail || disk.Reason == "" {
		t.Errorf("Status = %q/%q, want fail with reason", disk.Status, disk.Reason)
	}
	if disk.MediaErrors != 17408 || disk.Wear != 99 {
		t.Errorf("counters = media %d wear %d, want 17408/99", disk.MediaErrors, disk.Wear)
	}
}

func TestParseSmartctlJSONInvalid(t *testing.T) {
	for name, out := range map[string]string{
		"not json":    "smartctl: garbage",
		"empty json":  "{}",
		"no sections": `{"model_name":"X"}`,
	} {
		if _, err := parseSmartctlJSON("dev", out); err == nil {
			t.Errorf("%s: want error, got none", name)
		}
	}
}

const smartctlScanJSONFixture = `{
  "devices": [
    {"name": "/dev/sda", "info_name": "/dev/sda [SAT]", "type": "sat", "protocol": "ATA"},
    {"name": "/dev/sdb", "info_name": "/dev/sdb", "type": "ata", "protocol": "ATA"},
    {"name": "/dev/sdc", "info_name": "/dev/sdc", "type": "nvme", "protocol": "NVMe"}
  ]
}`

func TestParseSmartctlScanJSON(t *testing.T) {
	devices, err := parseSmartctlScanJSON([]byte(smartctlScanJSONFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(devices) != 3 || devices[0] != "/dev/sda" || devices[2] != "/dev/sdc" {
		t.Errorf("devices = %v, want three names", devices)
	}
	if _, err := parseSmartctlScanJSON([]byte("nope")); err == nil {
		t.Error("garbage scan output: want error")
	}
}

// TestPhysicalDiskSourceCounterPriority: smartctl counters win when it
// matched anything; WMI runs only when smartctl answers nothing. Each
// source stamps CountersSrc, so the diff can tell them apart.
func TestPhysicalDiskSourceCounterPriority(t *testing.T) {
	wmiCalled := false
	wmiSrc := &smartCountersSource{
		drives:      func() ([]winDrive, error) { return nil, errors.New("unavailable") },
		failureData: func() ([]smartInstance, error) { wmiCalled = true; return nil, nil },
	}
	smartctlSrc := &smartctlCountersSource{
		scan: func() ([]string, error) { return []string{"/dev/sdb"}, nil },
		run:  func(device string) (string, error) { return smartctlJSONSATA, nil },
	}
	src := &physicalDiskSource{smartctl: smartctlSrc, smart: wmiSrc}

	// smartctl matched: WMI must not be called.
	disks := []L1Disk{{Name: "Disk 0", Serial: "95NHK0IZFFQC", Status: StatusOK}}
	src.enrichCounters(disks)
	if wmiCalled {
		t.Error("WMI enrichment ran despite smartctl match")
	}
	if disks[0].CountersSrc != "smartctl" || disks[0].Realloc != 0 {
		t.Errorf("counters = %+v, want smartctl-stamped", disks[0])
	}

	// smartctl answers nothing: WMI fallback runs (fresh collect).
	src.smartctl = &smartctlCountersSource{
		scan: func() ([]string, error) { return nil, errors.New("missing") },
	}
	disks2 := []L1Disk{{Name: "Disk 0", Serial: "95NHK0IZFFQC", Status: StatusOK}}
	src.enrichCounters(disks2)
	if !wmiCalled {
		t.Error("WMI fallback did not run")
	}
	// WMI fixture has no SMART records, so no counters are assigned and
	// the disk stays unstamped — honest absence, not a stale source.
	if disks2[0].CountersSrc != "" {
		t.Errorf("CountersSrc = %q, want empty when no source delivered", disks2[0].CountersSrc)
	}
}

// TestSmartctlCountersEnrich: counters land on the disks matching by
// serial; a disk smartctl does not see (USB bridge) stays untouched; a
// scan failure or empty scan returns 0 (the caller falls back to WMI).
// NVMe disks whose platform serial is an EUI64-derived identifier (not the
// real serial) match by model instead.
func TestSmartctlCountersEnrich(t *testing.T) {
	src := &smartctlCountersSource{
		scan: func() ([]string, error) { return []string{"/dev/sdb", "/dev/sdc"}, nil },
		run: func(device string) (string, error) {
			switch device {
			case "/dev/sdb":
				return smartctlJSONSATA, nil
			case "/dev/sdc":
				return smartctlJSONNVMe, nil
			}
			return "", errors.New("unknown device")
		},
	}
	disks := []L1Disk{
		{Name: "Disk 0", Serial: "95NHK0IZFFQC", Status: StatusOK},
		// Real-machine NVMe record: MSFT_PhysicalDisk reports an
		// EUI64-derived identifier, not the smartctl serial.
		{Name: "Disk 1", Model: "SSD AMD R3MP30512G8 512GB",
			Serial: "0000_0000_0000_0000_0C82_D500_0890_0905.", Status: StatusOK},
		{Name: "Disk 2", Serial: "NA8JGT10", Status: StatusOK}, // USB, not in scan
	}
	if n := src.enrich(disks); n != 2 {
		t.Errorf("matched = %d, want 2", n)
	}
	if disks[0].Realloc != 0 || disks[0].Pending != 0 || disks[0].CountersSrc != "smartctl" {
		t.Errorf("SATA disk counters = %+v, want raw 0/0 from smartctl", disks[0])
	}
	if disks[1].MediaErrors != 0 || disks[1].Wear != 11 || disks[1].CountersSrc != "smartctl" {
		t.Errorf("NVMe disk counters = %+v, want media 0 wear 11 from smartctl (model match)", disks[1])
	}
	if disks[2].CountersSrc != "" {
		t.Errorf("USB disk must stay without counters, got %+v", disks[2])
	}

	// Serial normalization: the same serial with spaces still matches.
	disks[0].Serial = " 95NHK0IZFFQC "
	disks[0].CountersSrc = ""
	disks[1].CountersSrc = ""
	if n := src.enrich(disks); n != 2 {
		t.Errorf("matched with spaced serial = %d, want 2", n)
	}

	// Ambiguous models: two identical NVMe disks, one smartctl record —
	// only the first disk gets counters, the second stays honestly without.
	twin := []L1Disk{
		{Name: "nvmeA", Model: "SSD AMD R3MP30512G8 512GB", Status: StatusOK},
		{Name: "nvmeB", Model: "SSD AMD R3MP30512G8 512GB", Status: StatusOK},
	}
	src.scan = func() ([]string, error) { return []string{"/dev/sdc"}, nil }
	if n := src.enrich(twin); n != 1 {
		t.Errorf("matched twins = %d, want 1 (first unmatched only)", n)
	}
	if twin[0].Wear != 11 || twin[0].CountersSrc != "smartctl" {
		t.Errorf("first twin must get counters: %+v", twin[0])
	}
	if twin[1].CountersSrc != "" {
		t.Errorf("second twin must stay without counters: %+v", twin[1])
	}

	// Scan failure: 0 matches, nothing assigned.
	src.scan = func() ([]string, error) { return nil, errors.New("no smartctl") }
	if n := src.enrich(disks); n != 0 {
		t.Errorf("matched on scan failure = %d, want 0", n)
	}

	// Empty scan: 0 matches.
	src.scan = func() ([]string, error) { return nil, nil }
	if n := src.enrich(disks); n != 0 {
		t.Errorf("matched on empty scan = %d, want 0", n)
	}

	// Unparseable per-device output: 0 matches.
	src.scan = func() ([]string, error) { return []string{"/dev/sdb"}, nil }
	src.run = func(device string) (string, error) { return "garbage", nil }
	if n := src.enrich(disks); n != 0 {
		t.Errorf("matched on garbage output = %d, want 0", n)
	}
}
