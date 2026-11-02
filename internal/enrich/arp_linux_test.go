//go:build linux

package enrich

import (
	"net/netip"
	"testing"
)

const sampleProcNetArp = `IP address       HW type     Flags       HW address            Mask     Device
192.168.1.1      0x1         0x2         aa:bb:cc:dd:ee:01     *        eth0
192.168.1.55     0x1         0x2         11:22:33:44:55:66     *        eth0
192.168.1.99     0x1         0x0         00:00:00:00:00:00     *        eth0
bad-entry-not-ip 0x1         0x2         aa:bb:cc:dd:ee:09     *        eth0
fe80::1          0x1         0x2         aa:bb:cc:dd:ee:10     *        eth0
`

func TestParseProcNetArp(t *testing.T) {
	table := parseProcNetArp(sampleProcNetArp)
	if len(table) != 2 {
		t.Fatalf("entries = %d, want 2", len(table))
	}
	if mac := table[netip.MustParseAddr("192.168.1.1")]; mac != "aa:bb:cc:dd:ee:01" {
		t.Errorf("mac = %q, want aa:bb:cc:dd:ee:01", mac)
	}
	if mac := table[netip.MustParseAddr("192.168.1.55")]; mac != "11:22:33:44:55:66" {
		t.Errorf("mac = %q, want 11:22:33:44:55:66", mac)
	}
	// Incomplete entry (flags 0x0) must be skipped.
	if _, ok := table[netip.MustParseAddr("192.168.1.99")]; ok {
		t.Error("incomplete entry must be skipped")
	}
	// Non-IP and IPv6 lines must be skipped.
	if _, ok := table[netip.MustParseAddr("fe80::1")]; ok {
		t.Error("IPv6 line must be skipped")
	}
}

func TestParseProcNetArpEmpty(t *testing.T) {
	if got := len(parseProcNetArp("IP address   HW type   Flags   HW address   Mask   Device\n")); got != 0 {
		t.Errorf("header-only input = %d entries, want 0", got)
	}
}
