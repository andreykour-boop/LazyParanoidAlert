//go:build linux

package health

import (
	"os/exec"
	"strconv"
	"strings"
)

// newVendorCLISources builds L1 sources for every known hardware-RAID CLI
// found in PATH. Disks behind the controller are probed with
// `smartctl -d <type>,N /dev/sda`. When no CLI is installed, one honest
// "controller level unverified" error source is returned instead, so the
// gap is named in every snapshot rather than hidden.
func newVendorCLISources() []L1Source {
	var sources []L1Source
	for _, cli := range knownVendorCLIs {
		bin := findTool(cli.Name)
		if bin == "" {
			continue
		}
		cli := cli
		smartctl := findTool("smartctl")
		sources = append(sources, &vendorCLISource{
			cli:      cli,
			smartctl: smartctl,
			probe: func(diskIndex int, deviceType string) (string, error) {
				bin := findTool("smartctl")
				if bin == "" {
					return "", errSmartctlMissing
				}
				// smartctl needs a physical /dev entry to attach to the
				// controller even with an explicit -d device type.
				out, err := exec.Command(bin, "-d", deviceType+","+strconv.Itoa(diskIndex),
					"-i", "-H", "-A", "/dev/sda").CombinedOutput()
				return strings.TrimSpace(string(out)), err
			},
		})
	}
	if len(sources) == 0 {
		sources = append(sources, &absentVendorCLISource{})
	}
	return sources
}
