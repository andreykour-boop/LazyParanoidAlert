//go:build windows

package health

import (
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
)

// fixtureSMARTSource builds a smartCountersSource with injected queries:
// two identical-model SATA disks distinguished by serial, one of them with
// a real-looking attribute table (realloc 481), plus an unrelated instance.
// The ids mirror a real machine: PNPDeviceID uses the
// "SCSI\DISK&VEN_..&PROD_..\devnode" spelling while the SMART InstanceName
// says "SCSI\Disk<model>\devnode" — only the devnode tail matches.
func fixtureSMARTSource() (*smartCountersSource, []L1Disk) {
	blob1 := buildSMARTBlob(map[byte]uint64{
		smartAttrRealloc: 481,
		smartAttrPending: 0,
	})
	blob2 := buildSMARTBlob(map[byte]uint64{
		smartAttrRealloc: 7,
		smartAttrPending: 3,
	})
	s := &smartCountersSource{
		drives: func() ([]winDrive, error) {
			return []winDrive{
				{PNPDeviceID: `SCSI\DISK&VEN_TOSHIBA&PROD_MD04ACA600\5&3955AA6&0&030000`, SerialNumber: "95NHK0IZFFQC", Model: "TOSHIBA MD04ACA600"},
				{PNPDeviceID: `SCSI\DISK&VEN_TOSHIBA&PROD_MD04ACA600\5&3955AA6&0&030100`, SerialNumber: "95NHK0IZFFQD", Model: "TOSHIBA MD04ACA600"},
			}, nil
		},
		failureData: func() ([]smartInstance, error) {
			return []smartInstance{
				{InstanceName: `SCSI\Disk&Ven_TOSHIBA&Prod_MD04ACA600\5&3955aa6&0&030000_0`, VendorSpecific: blob1},
				{InstanceName: `SCSI\Disk&Ven_TOSHIBA&Prod_MD04ACA600\5&3955aa6&0&030100_0`, VendorSpecific: blob2},
				{InstanceName: `SCSI\DiskSeagate_Expansion______\5&999_0`, VendorSpecific: buildSMARTBlob(map[byte]uint64{smartAttrRealloc: 5})},
			}, nil
		},
	}
	disks := []L1Disk{
		{Name: "TOSHIBA MD04ACA600", Model: "TOSHIBA MD04ACA600", Serial: "95NHK0IZFFQC", Status: StatusOK},
		{Name: "TOSHIBA MD04ACA600 (1)", Model: "TOSHIBA MD04ACA600", Serial: "95NHK0IZFFQD", Status: StatusOK},
		{Name: "Seagate Expansion", Model: "Seagate Expansion", Serial: "USB004", Status: StatusOK},
	}
	return s, disks
}

// TestParseFailurePredictJSON: the powershell transport decodes base64
// blobs, accepts both single-object and array output, and skips undecodable
// records instead of failing.
func TestParseFailurePredictJSON(t *testing.T) {
	blob := buildSMARTBlob(map[byte]uint64{smartAttrRealloc: 481})
	b64 := base64.StdEncoding.EncodeToString(blob)

	// Array output (several instances).
	raw := fmt.Sprintf(`[{"InstanceName":"SCSI\\DiskA","VendorB64":%q},{"InstanceName":"SCSI\\DiskB","VendorB64":"!!!not-base64!!!"}]`, b64)
	recs, err := parseFailurePredictJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].InstanceName != `SCSI\DiskA` || len(recs[0].VendorSpecific) != len(blob) {
		t.Errorf("array records = %+v, want one decoded record", recs)
	}

	// Single-object output (one instance only).
	raw = fmt.Sprintf(`{"InstanceName":"SCSI\\DiskA","VendorB64":%q}`, b64)
	recs, err = parseFailurePredictJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].InstanceName != `SCSI\DiskA` {
		t.Errorf("single record = %+v, want one decoded record", recs)
	}
}

// TestSmartCountersEnrichBySerial: counters land on the right disk even
// with two identical models, matched through serial -> PNPDeviceID ->
// InstanceName (devnode tail, since the spellings differ); a USB disk
// without a SMART instance stays untouched.
func TestSmartCountersEnrichBySerial(t *testing.T) {
	s, disks := fixtureSMARTSource()
	s.enrich(disks)

	if disks[0].Realloc != 481 || disks[0].Pending != 0 {
		t.Errorf("disk 1 counters = realloc %d pending %d, want 481/0", disks[0].Realloc, disks[0].Pending)
	}
	if disks[1].Realloc != 7 || disks[1].Pending != 3 {
		t.Errorf("disk 2 counters = realloc %d pending %d, want 7/3", disks[1].Realloc, disks[1].Pending)
	}
	if disks[2].Realloc != 0 || disks[2].Pending != 0 {
		t.Errorf("USB disk must stay without counters, got realloc %d pending %d", disks[2].Realloc, disks[2].Pending)
	}
}

// TestSmartCountersEnrichByPrefix: the IDE\Disk spelling shares the first
// two segments between PNPDeviceID and InstanceName — the prefix key also
// matches.
func TestSmartCountersEnrichByPrefix(t *testing.T) {
	s, disks := fixtureSMARTSource()
	s.drives = func() ([]winDrive, error) {
		return []winDrive{
			{PNPDeviceID: `IDE\DISKTOSHIBA_MD04ACA600______________95NHK0IZFFQC\5&10d4c3f&0&0.0.0`, SerialNumber: "95NHK0IZFFQC", Model: "TOSHIBA MD04ACA600"},
		}, nil
	}
	s.failureData = func() ([]smartInstance, error) {
		return []smartInstance{
			{InstanceName: `IDE\DiskTOSHIBA_MD04ACA600______________95NHK0IZFFQC\5&10d4c3f&0&0.0.0`,
				VendorSpecific: buildSMARTBlob(map[byte]uint64{smartAttrRealloc: 33})},
		}, nil
	}
	s.enrich(disks)
	if disks[0].Realloc != 33 {
		t.Errorf("disk realloc = %d, want 33 (prefix match)", disks[0].Realloc)
	}
}

// TestSmartCountersEnrichByModelFallback: Win32_DiskDrive unavailable —
// the model matcher still assigns counters, each instance once.
func TestSmartCountersEnrichByModelFallback(t *testing.T) {
	s, disks := fixtureSMARTSource()
	s.drives = func() ([]winDrive, error) { return nil, errors.New("WMI denied") }
	s.enrich(disks)

	if disks[0].Realloc != 481 {
		t.Errorf("disk 1 realloc = %d, want 481 (model fallback)", disks[0].Realloc)
	}
	if disks[1].Realloc != 7 {
		t.Errorf("disk 2 realloc = %d, want 7 (model fallback)", disks[1].Realloc)
	}
	if disks[2].Realloc != 5 {
		t.Errorf("USB disk realloc = %d, want 5 (model fallback matched its instance)", disks[2].Realloc)
	}
}

// TestSmartCountersSourceFailureIsSilent: a failing SMART query must not
// touch the disk records at all — counters are a bonus, never an error.
func TestSmartCountersSourceFailureIsSilent(t *testing.T) {
	s, disks := fixtureSMARTSource()
	s.failureData = func() ([]smartInstance, error) { return nil, errors.New("access denied") }
	s.enrich(disks)
	for i, d := range disks {
		if d.Realloc != 0 || d.Pending != 0 {
			t.Errorf("disk %d changed on source failure: %+v", i, d)
		}
	}
}

// TestPhysicalDiskSourceEnrichIntegration: the enrichment is wired into the
// collector path and survives the WMI->PowerShell fallback ordering.
func TestPhysicalDiskSourceEnrichIntegration(t *testing.T) {
	smart, _ := fixtureSMARTSource()
	src := &physicalDiskSource{
		smart: smart,
		query: func() ([]L1Disk, error) {
			return []L1Disk{
				{Name: "TOSHIBA MD04ACA600", Model: "TOSHIBA MD04ACA600", Serial: "95NHK0IZFFQC", Status: StatusOK},
			}, nil
		},
	}
	disks, err := src.CollectL1()
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 1 || disks[0].Realloc != 481 {
		t.Errorf("enrichment not applied through CollectL1: %+v", disks)
	}
}
