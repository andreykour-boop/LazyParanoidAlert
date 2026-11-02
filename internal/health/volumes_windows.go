//go:build windows

package health

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// volumesSourceWin enumerates fixed local drives A:..Z:, skipping CD-ROM,
// network shares and unknown drive types. Removable drives are reported
// with ReadOnly=true (monitored, but excluded from space alarms).
type volumesSourceWin struct {
	// probe returns (driveType, total, free, err) for a drive root like
	// "C:\"; injectable for tests. nil = real kernel32 calls.
	probe func(root string) (driveType uint32, total, free uint64, err error)
}

func (s *volumesSourceWin) Name() string { return "volumes" }

// kernel32 drive types.
const (
	driveUnknown   = 0
	driveNoRootDir = 1
	driveRemovable = 2
	driveFixed     = 3
	driveRemote    = 4
	driveCDROM     = 5
	driveRAMDisk   = 6
)

var kernel32 = syscall.NewLazyDLL("kernel32.dll")

var procGetDriveTypeW = kernel32.NewProc("GetDriveTypeW")

func (s *volumesSourceWin) CollectVolumes() ([]Volume, error) {
	probe := s.probe
	if probe == nil {
		probe = probeDrive
	}
	var vols []Volume
	for letter := byte('A'); letter <= 'Z'; letter++ {
		root := string(letter) + `:\`
		driveType, total, free, err := probe(root)
		if err != nil {
			continue // no such drive / not ready
		}
		switch driveType {
		case driveFixed:
			vols = append(vols, Volume{Mount: root[:2], TotalBytes: total, FreeBytes: free})
		case driveRemovable, driveRAMDisk:
			vols = append(vols, Volume{Mount: root[:2], TotalBytes: total, FreeBytes: free, ReadOnly: true})
		default:
			// CD-ROM, network share, unknown, no root: not monitored.
		}
	}
	if len(vols) == 0 {
		return nil, fmt.Errorf("no local drives found")
	}
	return vols, nil
}

// probeDrive queries the real drive type and capacity.
func probeDrive(root string) (uint32, uint64, uint64, error) {
	rootPtr, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return driveUnknown, 0, 0, err
	}
	driveType, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(rootPtr)))
	if driveType == driveUnknown || driveType == driveNoRootDir {
		return uint32(driveType), 0, 0, fmt.Errorf("drive %s not present", root)
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(rootPtr, &freeToCaller, &total, &totalFree); err != nil {
		return uint32(driveType), 0, 0, err // not ready (empty card reader etc.)
	}
	return uint32(driveType), total, totalFree, nil
}
