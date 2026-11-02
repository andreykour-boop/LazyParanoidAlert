//go:build windows

package enrich

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// buildMIBTable assembles a synthetic MIB_IPNETTABLE buffer.
func buildMIBTable(rows []mibIPNetRow) []byte {
	buf := make([]byte, 4+len(rows)*mibIPNetRowSize)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(rows)))
	for i, row := range rows {
		off := 4 + i*mibIPNetRowSize
		binary.LittleEndian.PutUint32(buf[off:off+4], row.index)
		binary.LittleEndian.PutUint32(buf[off+4:off+8], row.physAddrLen)
		copy(buf[off+8:off+16], row.physAddr[:])
		binary.LittleEndian.PutUint32(buf[off+16:off+20], row.addr)
		binary.LittleEndian.PutUint32(buf[off+20:off+24], row.typ)
	}
	return buf
}

func TestParseIPNetTable(t *testing.T) {
	// 192.168.1.1 -> aa:bb:cc:dd:ee:01 (dynamic), 10.0.0.5 -> 11:22:33:44:55:66
	// (static), plus entries that must be skipped.
	bin := func(s string) uint32 {
		b := netip.MustParseAddr(s).As4()
		// The DWORD value stores the address in network byte order, so in
		// little-endian memory the octets appear in reading order; the
		// numeric value is therefore LittleEndian over the octets.
		return binary.LittleEndian.Uint32(b[:])
	}
	ip1 := bin("192.168.1.1")
	ip2 := bin("10.0.0.5")

	buf := buildMIBTable([]mibIPNetRow{
		{index: 5, physAddrLen: 6, physAddr: [8]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x01}, addr: ip1, typ: ipNetTypeDynamic},
		{index: 5, physAddrLen: 6, physAddr: [8]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}, addr: ip2, typ: ipNetTypeStatic},
		{index: 5, physAddrLen: 6, physAddr: [8]byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x00}, addr: bin("10.0.0.9"), typ: ipNetTypeInvalid},
		{index: 5, physAddrLen: 0, addr: bin("10.0.0.7"), typ: ipNetTypeDynamic}, // no MAC: skip
	})

	table := parseIPNetTable(buf)
	if len(table) != 2 {
		t.Fatalf("entries = %d, want 2", len(table))
	}
	if mac := table[netip.MustParseAddr("192.168.1.1")]; mac != "aa:bb:cc:dd:ee:01" {
		t.Errorf("mac = %q, want aa:bb:cc:dd:ee:01", mac)
	}
	if mac := table[netip.MustParseAddr("10.0.0.5")]; mac != "11:22:33:44:55:66" {
		t.Errorf("mac = %q, want 11:22:33:44:55:66", mac)
	}
	if _, ok := table[netip.MustParseAddr("10.0.0.9")]; ok {
		t.Error("invalid entry must be skipped")
	}
	if _, ok := table[netip.MustParseAddr("10.0.0.7")]; ok {
		t.Error("entry without MAC must be skipped")
	}
}

func TestParseIPNetTableTruncated(t *testing.T) {
	// Entry count lies about the buffer size: must not panic.
	buf := make([]byte, 4+2*mibIPNetRowSize-5)
	binary.LittleEndian.PutUint32(buf[0:4], 2)
	table := parseIPNetTable(buf)
	if len(table) != 0 {
		t.Errorf("truncated buffer must yield an empty table, got %d", len(table))
	}
}

func TestParseIPNetTableEmpty(t *testing.T) {
	if got := len(parseIPNetTable([]byte{0, 0, 0, 0})); got != 0 {
		t.Errorf("empty table = %d entries, want 0", got)
	}
	if got := len(parseIPNetTable(nil)); got != 0 {
		t.Errorf("nil buffer = %d entries, want 0", got)
	}
}

// TestReadARPTableLive parses the real host neighbor table; the machine has
// at least one neighbor entry in normal operation. Skipped on hosts with an
// empty table.
func TestReadARPTableLive(t *testing.T) {
	table, err := readARPTable()
	if err != nil {
		t.Fatalf("readARPTable: %v", err)
	}
	if len(table) == 0 {
		t.Skip("host neighbor table is empty")
	}
	for ip, mac := range table {
		if !ip.IsValid() || !ip.Is4() {
			t.Errorf("bad IP in table: %v", ip)
		}
		if len(mac) != 17 { // xx:xx:xx:xx:xx:xx
			t.Errorf("bad MAC %q for %v", mac, ip)
		}
		t.Logf("live entry: %v -> %s", ip, mac)
		break // one entry is enough to prove the parse
	}
}
