package health

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// smartctlSource collects L1 physical disks via smartctl. The device list
// and the per-device execution are injectable, so all logic is testable
// without Linux; the linux wiring (real /sys/block walk + exec) lives in
// the platform file.
type smartctlSource struct {
	// listDevices returns physical disk device names ("sda", "nvme0n1").
	listDevices func() ([]string, error)
	// run executes smartctl for one device and returns combined output;
	// a non-zero exit code with output is NOT an error here (smartctl
	// exits non-zero when a disk is failing) — output is parsed anyway.
	run func(device string) (string, error)
}

func (s *smartctlSource) Name() string { return "smartctl" }

func (s *smartctlSource) CollectL1() ([]L1Disk, error) {
	if s.listDevices == nil || s.run == nil {
		return nil, fmt.Errorf("smartctl not configured")
	}
	devices, err := s.listDevices()
	if err != nil {
		return nil, err
	}
	disks := make([]L1Disk, 0, len(devices))
	for _, dev := range devices {
		output, runErr := s.run(dev)
		if strings.TrimSpace(output) == "" {
			reason := "no output"
			if runErr != nil {
				reason = runErr.Error()
			}
			disks = append(disks, L1Disk{Name: dev, Status: StatusUnknown, Reason: reason})
			continue
		}
		disk, parseErr := parseSmartctl(dev, output)
		if parseErr != nil {
			disk = L1Disk{Name: dev, Status: StatusUnknown, Reason: parseErr.Error()}
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

// errSmartctlMissing is reported per source when smartmontools are not
// installed; install.sh puts them in place.
var errSmartctlMissing = constError("smartctl not found in PATH (install smartmontools)")

type constError string

func (e constError) Error() string { return string(e) }

// findTool resolves a tool name via PATH; on a miss, well-known install
// locations are probed — the smartmontools NSIS installer on Windows adds
// its bin dir to the *user* PATH, which a LocalSystem service never sees.
// Empty means "not installed", which the collector reports as an unknown
// source, never as silence.
func findTool(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	for _, cand := range toolCandidates(name) {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	return ""
}

// toolCandidates returns well-known absolute locations for a tool beyond
// PATH. Injectable for tests. Only tools we actually ship installers for
// have candidates; everything else relies on PATH alone.
var toolCandidates = func(name string) []string {
	if name != "smartctl" {
		return nil
	}
	return []string{`C:\Program Files\smartmontools\bin\smartctl.exe`}
}
