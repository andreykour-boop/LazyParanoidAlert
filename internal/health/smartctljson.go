package health

import (
	"encoding/json"
	"fmt"
)

// smartctl -j JSON parsing: the direct smartctl path on both platforms.
// JSON is used instead of the text output because smartctl prints NVMe
// counters with locale thousands separators in text mode (["14 974",
// "17,785,059"]) which a strconv.ParseInt silently fails on, while JSON
// numbers arrive clean and the structure is stable across versions.

// smartctlJSON mirrors the subset of `smartctl -i -H -A -j` output the
// watch consumes. Fields stay pointers/optional: a source may omit whole
// sections (a SATA disk has no NVMe log and vice versa).
type smartctlJSON struct {
	ModelName    string `json:"model_name"`
	SerialNumber string `json:"serial_number"`
	SmartStatus  *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	ATAAttributes *struct {
		Table []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Raw  struct {
				Value int64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	NVMeHealth *struct {
		MediaErrors    *int64 `json:"media_errors"`
		PercentageUsed *int64 `json:"percentage_used"`
	} `json:"nvme_smart_health_information_log"`
}

// smartAttrReallocID / smartAttrPendingID are the ATA attribute ids for the
// sector counters, mirroring smartattr.go's byte-level constants.
const (
	smartAttrReallocID = 5
	smartAttrPendingID = 197
)

// parseSmartctlJSON extracts an L1 disk record from one `smartctl
// -i -H -A -j` output. Counters land in the ATA or NVMe fields depending
// on what the device reports; CountersSrc is stamped "smartctl" so the
// diff can re-baseline when the counter source changes.
func parseSmartctlJSON(device, output string) (L1Disk, error) {
	var doc smartctlJSON
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		return L1Disk{}, fmt.Errorf("parse smartctl json for %s: %w", device, err)
	}
	disk := L1Disk{Name: device, Model: doc.ModelName, Serial: doc.SerialNumber}
	switch {
	case doc.SmartStatus == nil:
		disk.Status = StatusUnknown
		disk.Reason = "no smart_status in smartctl output"
	case doc.SmartStatus.Passed:
		disk.Status = StatusOK
	default:
		disk.Status = StatusFail
		disk.Reason = "SMART health status: FAILED"
	}
	if doc.ATAAttributes != nil {
		for _, a := range doc.ATAAttributes.Table {
			switch a.ID {
			case smartAttrReallocID:
				disk.Realloc = a.Raw.Value
				disk.CountersSrc = "smartctl"
			case smartAttrPendingID:
				disk.Pending = a.Raw.Value
				disk.CountersSrc = "smartctl"
			}
		}
	}
	if doc.NVMeHealth != nil {
		if doc.NVMeHealth.MediaErrors != nil {
			disk.MediaErrors = *doc.NVMeHealth.MediaErrors
			disk.CountersSrc = "smartctl"
		}
		if doc.NVMeHealth.PercentageUsed != nil {
			disk.Wear = *doc.NVMeHealth.PercentageUsed
			disk.CountersSrc = "smartctl"
		}
	}
	if doc.SmartStatus == nil && doc.ATAAttributes == nil && doc.NVMeHealth == nil {
		return L1Disk{}, fmt.Errorf("no health data in smartctl output for %s", device)
	}
	return disk, nil
}
