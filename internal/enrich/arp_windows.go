//go:build windows

package enrich

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"unsafe"
)

var (
	modiphlpapi       = syscall.NewLazyDLL("iphlpapi.dll")
	procGetIpNetTable = modiphlpapi.NewProc("GetIpNetTable")
)

// Windows error codes returned by GetIpNetTable.
const (
	errNoData             = syscall.Errno(232) // neighbor table is empty
	errInsufficientBuffer = syscall.Errno(122)
)

// mibIPNetRow mirrors MIB_IPNETROW (MAXLEN_PHYSADDR = 8), 24 bytes.
type mibIPNetRow struct {
	index       uint32
	physAddrLen uint32
	physAddr    [8]byte
	addr        uint32 // IPv4 address, network byte order
	typ         uint32
}

const mibIPNetRowSize = 24

// MIB_IPNETROW.dwType values.
const (
	ipNetTypeInvalid = 2 // entry is marked invalid: skip
	ipNetTypeDynamic = 3
	ipNetTypeStatic  = 4
)

// readARPTable returns the host IPv4 neighbor table: IP -> normalized MAC.
func readARPTable() (map[netip.Addr]string, error) {
	var size uint32
	ret, _, _ := procGetIpNetTable.Call(0, uintptr(unsafe.Pointer(&size)), 0)
	switch {
	case ret == 0:
		return map[netip.Addr]string{}, nil // no entries, nothing to allocate
	case ret == uintptr(errNoData):
		return map[netip.Addr]string{}, nil
	case ret == uintptr(errInsufficientBuffer) && size >= 4:
	default:
		return nil, fmt.Errorf("enrich: GetIpNetTable sizing: %w", syscall.Errno(ret))
	}

	buf := make([]byte, size)
	ret, _, _ = procGetIpNetTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0, // sorted output
	)
	if ret != 0 {
		return nil, fmt.Errorf("enrich: GetIpNetTable: %w", syscall.Errno(ret))
	}
	return parseIPNetTable(buf), nil
}

// parseIPNetTable extracts the neighbor map from a GetIpNetTable buffer:
// uint32 entry count, then fixed-size MIB_IPNETROW records.
func parseIPNetTable(buf []byte) map[netip.Addr]string {
	if len(buf) < 4 {
		return map[netip.Addr]string{}
	}
	numEntries := binary.LittleEndian.Uint32(buf[0:4])
	table := make(map[netip.Addr]string, numEntries)
	for i := 0; i < int(numEntries); i++ {
		off := 4 + i*mibIPNetRowSize
		if off+mibIPNetRowSize > len(buf) {
			break // defensive: truncated buffer
		}
		row := (*mibIPNetRow)(unsafe.Pointer(&buf[off]))
		if row.typ == ipNetTypeInvalid || row.physAddrLen == 0 || row.physAddrLen > 8 {
			continue
		}
		// row.addr holds the IPv4 address in network byte order, so the
		// memory bytes are already the four octets.
		addrBytes := (*[4]byte)(unsafe.Pointer(&row.addr))
		addr, ok := netip.AddrFromSlice(addrBytes[:])
		if !ok {
			continue
		}
		mac := net.HardwareAddr(row.physAddr[:row.physAddrLen]).String()
		if mac != "" {
			table[addr] = mac
		}
	}
	return table
}
