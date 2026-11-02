package config

import (
	"reflect"
	"testing"
)

// TestParseAllowlistEntry: the whole string is tried as IP/CIDR first (the
// legacy format and unbracketed IPv6 stay plain addresses); the port
// suffix is parsed only on that miss. One bad port spoils the entry.
func TestParseAllowlistEntry(t *testing.T) {
	cases := []struct {
		in    string
		addr  string
		ports []int
	}{
		{"192.168.1.10", "192.168.1.10", nil},
		{"10.0.0.0/24", "10.0.0.0/24", nil},
		{" ::1 ", "::1", nil},             // trimmed, no ports
		{"fe80::1:22", "fe80::1:22", nil}, // ambiguous form stays an address
		{"192.168.1.10:22", "192.168.1.10", []int{22}},
		{"10.0.0.0/24:22,3389", "10.0.0.0/24", []int{22, 3389}},
		{"[::1]:22", "::1", []int{22}},
		{"192.168.1.10: 22 , 3389", "192.168.1.10", []int{22, 3389}},
	}
	for _, c := range cases {
		addr, ports, err := ParseAllowlistEntry(c.in)
		if err != nil {
			t.Errorf("ParseAllowlistEntry(%q): %v", c.in, err)
			continue
		}
		if addr != c.addr || !reflect.DeepEqual(ports, c.ports) {
			t.Errorf("ParseAllowlistEntry(%q) = %q, %v; want %q, %v", c.in, addr, ports, c.addr, c.ports)
		}
	}

	for _, in := range []string{
		"not-an-ip",           // no colon, not an address
		"192.168.1.10:",       // empty port list
		"192.168.1.10:0",      // port below the range
		"192.168.1.10:70000",  // port above the range
		"192.168.1.10:22,abc", // non-numeric port
		"[::1]:",              // bracketed head, empty list
		"[bad]:22",            // bracketed garbage head
	} {
		if _, _, err := ParseAllowlistEntry(in); err == nil {
			t.Errorf("ParseAllowlistEntry(%q): want error, got none", in)
		}
	}
}
