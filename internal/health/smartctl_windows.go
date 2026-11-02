//go:build windows

package health

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// smartctlCountersSource enriches L1 disk records with counters read
// through the smartctl binary: `--scan -j` lists devices, `-i -H -A -j`
// queries each one. It is the primary counter source on Windows (honest
// raw values for SATA, NVMe health log included); the WMI
// smartCountersSource is the fallback when smartctl is missing or answers
// nothing. Matching is by normalized serial number. All exec hooks are
// injectable for tests.
type smartctlCountersSource struct {
	scan func() ([]string, error)
	run  func(device string) (string, error)
}

func newSmartctlCountersSource() *smartctlCountersSource {
	return &smartctlCountersSource{
		scan: smartctlScan,
		run:  smartctlRun,
	}
}

// smartctlScan lists device paths from `smartctl --scan -j`.
func smartctlScan() ([]string, error) {
	bin := findTool("smartctl")
	if bin == "" {
		return nil, errSmartctlMissing
	}
	out, err := exec.Command(bin, "--scan", "-j").Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("smartctl --scan: %w", err)
	}
	return parseSmartctlScanJSON(out)
}

// smartctlScanJSON is the device list of `smartctl --scan -j`.
type smartctlScanJSON struct {
	Devices []struct {
		Name string `json:"name"`
	} `json:"devices"`
}

func parseSmartctlScanJSON(raw []byte) ([]string, error) {
	var doc smartctlScanJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse smartctl --scan output: %w", err)
	}
	out := make([]string, 0, len(doc.Devices))
	for _, d := range doc.Devices {
		if d.Name != "" {
			out = append(out, d.Name)
		}
	}
	return out, nil
}

// smartctlRun runs `smartctl -i -H -A -j <device>`. A non-zero exit with
// output is not an error here: smartctl exits non-zero for failing disks
// and for unreadable side data while the health section is still present.
func smartctlRun(device string) (string, error) {
	bin := findTool("smartctl")
	if bin == "" {
		return "", errSmartctlMissing
	}
	out, err := exec.Command(bin, "-i", "-H", "-A", "-j", device).CombinedOutput()
	return string(out), err
}

// enrich fills the counters of matching L1 records and returns how many
// disks it matched; the caller treats 0 as "fallback to WMI". Failures are
// swallowed by design: counters are a bonus, and a disk that stays without
// them must not degrade into an error or a broken collection.
func (s *smartctlCountersSource) enrich(disks []L1Disk) int {
	if s == nil || len(disks) == 0 {
		return 0
	}
	devices, err := s.scan()
	if err != nil || len(devices) == 0 {
		return 0
	}
	var records []L1Disk
	for _, dev := range devices {
		output, _ := s.run(dev)
		if strings.TrimSpace(output) == "" {
			continue
		}
		rec, err := parseSmartctlJSON(dev, output)
		if err != nil {
			continue
		}
		records = append(records, rec)
	}
	if len(records) == 0 {
		return 0
	}
	// Primary match: normalized serial number. Fallback: normalized model —
	// MSFT_PhysicalDisk reports an EUI64-derived identifier instead of the
	// real serial for NVMe drives ("0000_0000_..._0905."), so the serial
	// never matches smartctl's serial_number; the model text matches
	// exactly. Identical models take the first unused record, the same
	// rule as the WMI model fallback.
	used := make([]bool, len(records))
	matched := 0
	for i := range disks {
		sn := normSerial(disks[i].Serial)
		if sn == "" {
			continue
		}
		for j, rec := range records {
			if used[j] {
				continue
			}
			if normSerial(rec.Serial) == sn {
				applyCounters(&disks[i], rec)
				used[j] = true
				matched++
				break
			}
		}
	}
	for i := range disks {
		if disks[i].CountersSrc != "" {
			continue
		}
		model := normID(disks[i].Model)
		if model == "" {
			continue
		}
		for j, rec := range records {
			if used[j] {
				continue
			}
			if normID(rec.Model) == model {
				applyCounters(&disks[i], rec)
				used[j] = true
				matched++
				break
			}
		}
	}
	return matched
}

// applyCounters copies the counters of a smartctl record onto an L1 disk.
func applyCounters(d *L1Disk, rec L1Disk) {
	d.Realloc = rec.Realloc
	d.Pending = rec.Pending
	d.MediaErrors = rec.MediaErrors
	d.Wear = rec.Wear
	d.CountersSrc = rec.CountersSrc
}
