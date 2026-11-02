//go:build linux

package health

// NewPlatformSources assembles the Linux collectors: physical disks via
// smartctl, software arrays via mdadm, volumes via /proc/mounts + statfs,
// vendor RAID tools when installed (an honest "controller level unverified"
// error source replaces them when absent).
func NewPlatformSources() (l1 []L1Source, l2 []L2Source, vols []VolumesSource) {
	l1 = append(l1, newSmartctlSource())
	l1 = append(l1, newVendorCLISources()...)
	l2 = append(l2, newMdadmSource())
	vols = append(vols, newVolumesSource())
	return l1, l2, vols
}
