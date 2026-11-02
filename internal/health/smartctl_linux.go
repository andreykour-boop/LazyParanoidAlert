//go:build linux

package health

import (
	"os"
	"os/exec"
)

// readProcMounts reads /proc/mounts; split out for clarity.
func readProcMounts() (string, error) {
	raw, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// newSmartctlSource wires the smartctl collector to the real machine: the
// device list comes from /sys/block (pseudo-devices and removable media
// filtered out) and each device is queried with `smartctl -i -H -A`.
// A missing smartctl binary is not an error at construction: the source is
// still registered and reports its absence on collect, keeping the "do not
// stay silent" contract.
func newSmartctlSource() *smartctlSource {
	return &smartctlSource{
		listDevices: func() ([]string, error) {
			return listBlockDevices("/sys/block")
		},
		run: func(device string) (string, error) {
			bin := findTool("smartctl")
			if bin == "" {
				return "", errSmartctlMissing
			}
			out, err := exec.Command(bin, "-i", "-H", "-A", "/dev/"+device).CombinedOutput()
			return string(out), err
		},
	}
}
