//go:build windows

package health

import (
	"errors"
	"testing"
)

func TestParsePhysicalDiskJSON(t *testing.T) {
	// Array form (several disks).
	raw := []byte(`[
 {"DeviceId":"0","FriendlyName":"PhysicalDrive0","Model":"Samsung SSD 870","SerialNumber":"S6PXNS0R","HealthStatus":"Healthy"},
 {"DeviceId":"1","FriendlyName":"PhysicalDrive1","Model":"WDC WD40EFRX","SerialNumber":"WD-WCC","HealthStatus":"Warning"}
]`)
	disks, err := parsePhysicalDiskJSON(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(disks) != 2 {
		t.Fatalf("disks = %+v, want 2", disks)
	}
	if disks[0].Name != "PhysicalDrive0" || disks[0].Status != StatusOK || disks[0].Model != "Samsung SSD 870" {
		t.Errorf("disk 0 wrong: %+v", disks[0])
	}
	if disks[1].Status != StatusWarn {
		t.Errorf("disk 1 status = %q, want warn", disks[1].Status)
	}

	// Single-object form (one disk).
	single := []byte(`{"DeviceId":"0","FriendlyName":"PhysicalDrive0","Model":"M","SerialNumber":"S","HealthStatus":"Unhealthy"}`)
	disks, err = parsePhysicalDiskJSON(single)
	if err != nil {
		t.Fatalf("parse single: %v", err)
	}
	if len(disks) != 1 || disks[0].Status != StatusFail {
		t.Errorf("single disk wrong: %+v", disks)
	}

	// Broken JSON.
	if _, err := parsePhysicalDiskJSON([]byte("not json")); err == nil {
		t.Error("expected error for broken output")
	}
}

func TestParsePhysicalDiskJSONFallbacks(t *testing.T) {
	// Unknown health value -> unknown status, not a crash.
	disks, err := parsePhysicalDiskJSON([]byte(`{"DeviceId":"5","FriendlyName":"","Model":"X","HealthStatus":"Mystery"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if disks[0].Status != StatusUnknown {
		t.Errorf("status = %q, want unknown", disks[0].Status)
	}
	if disks[0].Name != "5" {
		t.Errorf("name must fall back to DeviceId: %+v", disks[0])
	}
}

func TestParseStoragePoolJSON(t *testing.T) {
	raw := []byte(`[
 {"FriendlyName":"Mirror Pool","HealthStatus":"Healthy"},
 {"FriendlyName":"Simple Space","HealthStatus":"Warning"},
 {"FriendlyName":"Broken Pool","HealthStatus":"Unhealthy"}
]`)
	pools, err := parseStoragePoolJSON(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pools) != 3 {
		t.Fatalf("pools = %+v, want 3", pools)
	}
	wantStates := []L2State{L2Clean, L2Degraded, L2Failed}
	for i, want := range wantStates {
		if pools[i].State != want {
			t.Errorf("pool %d state = %q, want %q", i, pools[i].State, want)
		}
		if pools[i].Kind != KindPool {
			t.Errorf("pool %d kind = %q, want pool", i, pools[i].Kind)
		}
	}

	// Empty output (no pools on the machine) is not an error.
	pools, err = parseStoragePoolJSON([]byte(`[]`))
	if err != nil || len(pools) != 0 {
		t.Errorf("empty pool list: pools=%+v err=%v", pools, err)
	}
}

// TestWMIQueryIntegration is a live check of the COM/WMI path against the
// real machine's storage namespace; run with -run WMI on a Windows box.
func TestWMIQueryIntegration(t *testing.T) {
	disks, err := queryPhysicalDisksWMI()
	if err != nil {
		t.Fatalf("WMI physical disks: %v", err)
	}
	if len(disks) == 0 {
		t.Fatal("WMI returned no disks")
	}
	for _, d := range disks {
		t.Logf("disk: %+v", d)
	}

	pools, err := queryStoragePoolsWMI()
	if err != nil {
		t.Logf("WMI storage pools (ok to fail without Storage Spaces): %v", err)
	} else {
		for _, p := range pools {
			t.Logf("pool: %+v", p)
		}
	}

	// The powershell fallback must resolve the interpreter by absolute path.
	t.Logf("powershell resolved at: %s", powershellPath())
}

func TestPhysicalDiskSourceFallback(t *testing.T) {
	s := &physicalDiskSource{}
	if s.query != nil {
		t.Fatal("default source must use WMI+fallback path in production")
	}
}

// TestVolumesSourceWinFilter: drive-type filtering with a fake probe.
func TestVolumesSourceWinFilter(t *testing.T) {
	fake := map[string]struct {
		driveType uint32
		total     uint64
		free      uint64
		err       error
	}{
		`C:\`: {driveFixed, gb(500), gb(50), nil},
		`D:\`: {driveFixed, gb(3600), gb(288), nil},
		`E:\`: {driveCDROM, 0, 0, nil},                     // CD-ROM -> skip
		`F:\`: {driveRemote, 0, 0, nil},                    // network -> skip
		`G:\`: {driveRemovable, gb(64), gb(2), nil},        // removable -> ReadOnly
		`H:\`: {driveUnknown, 0, 0, nil},                   // phantom -> skip
		`I:\`: {driveFixed, 0, 0, errors.New("not ready")}, // card reader -> skip
	}

	s := &volumesSourceWin{
		probe: func(root string) (uint32, uint64, uint64, error) {
			d, ok := fake[root]
			if !ok {
				return driveNoRootDir, 0, 0, errors.New("absent")
			}
			return d.driveType, d.total, d.free, d.err
		},
	}

	vols, err := s.CollectVolumes()
	if err != nil {
		t.Fatalf("CollectVolumes: %v", err)
	}
	if len(vols) != 3 {
		t.Fatalf("volumes = %+v, want 3 (C, D, G)", vols)
	}
	if vols[0].Mount != "C:" || vols[0].ReadOnly {
		t.Errorf("fixed drive wrong: %+v", vols[0])
	}
	if vols[1].Mount != "D:" {
		t.Errorf("second fixed drive wrong: %+v", vols[1])
	}
	if vols[2].Mount != "G:" || !vols[2].ReadOnly {
		t.Errorf("removable drive must be ReadOnly: %+v", vols[2])
	}
}

func TestVolumesSourceWinEmpty(t *testing.T) {
	s := &volumesSourceWin{
		probe: func(string) (uint32, uint64, uint64, error) {
			return driveNoRootDir, 0, 0, errors.New("absent")
		},
	}
	if _, err := s.CollectVolumes(); err == nil {
		t.Fatal("expected error when no local drives exist")
	}
}
