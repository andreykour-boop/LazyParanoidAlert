//go:build linux

package enrich

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
)

// procNetArpPath is the kernel's IPv4 neighbor table.
const procNetArpPath = "/proc/net/arp"

// readARPTable returns the host IPv4 neighbor table: IP -> normalized MAC.
// Incomplete entries (flags 0x0, zero MAC) are skipped.
func readARPTable() (map[netip.Addr]string, error) {
	data, err := os.ReadFile(procNetArpPath)
	if err != nil {
		return nil, fmt.Errorf("enrich: read %s: %w", procNetArpPath, err)
	}
	return parseProcNetArp(string(data)), nil
}

// parseProcNetArp parses the /proc/net/arp text format:
//
//	IP address  HW type  Flags  HW address  Mask  Device
func parseProcNetArp(data string) map[netip.Addr]string {
	table := make(map[netip.Addr]string)
	for i, line := range strings.Split(data, "\n") {
		if i == 0 || line == "" {
			continue // header or trailing newline
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		addr, err := netip.ParseAddr(fields[0])
		if err != nil || !addr.Is4() {
			continue
		}
		if fields[2] == "0x0" {
			continue // incomplete neighbor entry
		}
		mac, err := net.ParseMAC(fields[3])
		if err != nil {
			continue
		}
		table[addr] = mac.String()
	}
	return table
}
