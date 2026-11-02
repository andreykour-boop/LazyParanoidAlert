package health

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// vendorCLI tools that expose hardware-RAID controller health. When one is
// found in PATH, physical disks behind the controller are enumerated via
// smartctl with the corresponding device type.
type vendorCLI struct {
	// Name is the binary looked up in PATH.
	Name string
	// SmartctlDeviceType is the smartctl -d argument for disks behind it.
	SmartctlDeviceType string
	// ControllerPrefix is how the L1 disk names are prefixed.
	ControllerPrefix string
}

var knownVendorCLIs = []vendorCLI{
	{Name: "storcli", SmartctlDeviceType: "megaraid", ControllerPrefix: "storcli"},
	{Name: "perccli", SmartctlDeviceType: "megaraid", ControllerPrefix: "perccli"},
	{Name: "arcconf", SmartctlDeviceType: "aacraid", ControllerPrefix: "arcconf"},
}

// findVendorCLI returns the first known tool present in the given PATH
// value (empty in production, where the process PATH is used). The PATH is
// injectable so the detection is testable on any platform; lookup defaults
// to exec.LookPath.
func findVendorCLI(paths []string, lookup func(file string) (string, error)) *vendorCLI {
	if lookup == nil {
		lookup = exec.LookPath
	}
	for _, cli := range knownVendorCLIs {
		for _, dir := range paths {
			if dir == "" {
				continue
			}
			if _, err := lookup(filepath.Join(dir, cli.Name)); err == nil {
				return &cli
			}
		}
	}
	return nil
}

// absentVendorCLISource always fails: a hardware-RAID controller level
// cannot be verified without the vendor CLI, and the "do not stay silent"
// rule requires naming that in every snapshot and report.
type absentVendorCLISource struct{}

func (s *absentVendorCLISource) Name() string { return "vendor-cli" }

func (s *absentVendorCLISource) CollectL1() ([]L1Disk, error) {
	return nil, fmt.Errorf("vendor CLI (storcli/perccli/arcconf) не найден в PATH — уровень аппаратного RAID-контроллера не проверяется")
}

// vendorCLISource probes disks behind a hardware RAID controller through
// smartctl (-d <type>,N). If the controller cannot be probed, the source
// reports an error, which the snapshot records as a SourceError — the
// "controller level" is honestly named as unverified, never as OK.
type vendorCLISource struct {
	cli      vendorCLI
	smartctl string // path to smartctl; empty = absent
	// probe runs smartctl -d <type>,<index> and returns combined output;
	// index runs 0,1,2... until an error with no output.
	probe func(diskIndex int, deviceType string) (string, error)
	// maxDisks caps the enumeration behind one controller.
	maxDisks int
}

func (s *vendorCLISource) Name() string {
	return s.cli.ControllerPrefix
}

func (s *vendorCLISource) CollectL1() ([]L1Disk, error) {
	if s.smartctl == "" {
		return nil, fmt.Errorf("smartctl отсутствует — диски за %s не проверить", s.cli.Name)
	}
	if s.probe == nil {
		return nil, fmt.Errorf("контроллер %s не настроен для опроса", s.cli.Name)
	}
	limit := s.maxDisks
	if limit == 0 {
		limit = 32
	}

	var disks []L1Disk
	foundAny := false
	for i := 0; i < limit; i++ {
		output, err := s.probe(i, s.cli.SmartctlDeviceType)
		if strings.TrimSpace(output) == "" {
			// No disk behind this index: enumeration end (empty output on
			// missing slot). An error with empty output ends the scan.
			if err != nil && i == 0 {
				return nil, fmt.Errorf("контроллер %s не отвечает: %v", s.cli.Name, err)
			}
			break
		}
		foundAny = true
		name := fmt.Sprintf("%s:%d", s.cli.ControllerPrefix, i)
		disk, parseErr := parseSmartctl(name, output)
		if parseErr != nil {
			disk = L1Disk{Name: name, Status: StatusUnknown, Reason: parseErr.Error()}
		}
		disks = append(disks, disk)
	}
	if !foundAny {
		return nil, fmt.Errorf("контроллер %s найден, но диски за ним не прочитаны", s.cli.Name)
	}
	return disks, nil
}
