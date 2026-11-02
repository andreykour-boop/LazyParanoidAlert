//go:build linux

package health

import (
	"os"
	"os/exec"
	"strings"
)

// newMdadmSource wires the mdadm collector to the real machine: the array
// list from /proc/mdstat, details via `mdadm --detail`. A missing mdadm
// binary is reported on collect (unknown source), never silently.
func newMdadmSource() *mdadmSource {
	return &mdadmSource{
		listArrays: func() ([]string, error) {
			raw, err := os.ReadFile("/proc/mdstat")
			if err != nil {
				return nil, err
			}
			return parseMdstat(string(raw)), nil
		},
		detail: func(device string) (string, error) {
			bin := findTool("mdadm")
			if bin == "" {
				return "", constError("mdadm not found in PATH")
			}
			out, err := exec.Command(bin, "--detail", "/dev/"+device).CombinedOutput()
			return strings.TrimSpace(string(out)), err
		},
	}
}
