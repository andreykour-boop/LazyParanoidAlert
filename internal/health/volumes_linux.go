//go:build linux

package health

import "syscall"

// statfsUnix is the real statfs bridge for Linux: converts syscall.Statfs_t
// to byte counts. Bavail (not Bfree) is used so the numbers match what the
// owner can actually still write.
func statfsUnix(path string) (uint64, uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	total := st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	return total, free, nil
}

// newVolumesSource wires the volumes collector to the real machine: mount
// candidates from /proc/mounts (pseudo and network filesystems filtered),
// capacity via statfs.
func newVolumesSource() *volumesSource {
	return &volumesSource{
		mounts: func() ([]mountEntry, error) {
			raw, err := readProcMounts()
			if err != nil {
				return nil, err
			}
			return parseProcMounts(raw), nil
		},
		statfs: statfsUnix,
	}
}
