package health

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// parseSmartctl extracts the L1 disk record from a combined
// `smartctl -i -H -A <device>` output. Works for both SATA (attribute
// table) and NVMe (health result line; the NVMe health-log counters
// media_errors and percentage_used are parsed too — see parseSMARTNumber
// for the thousands-separator caveat).
func parseSmartctl(device, output string) (L1Disk, error) {
	disk := L1Disk{Name: device, Status: StatusUnknown, Reason: "smartctl output not recognized"}

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "device model:"),
			strings.HasPrefix(lower, "model number:"):
			disk.Model = afterColon(line)
		case strings.HasPrefix(lower, "serial number:"):
			disk.Serial = afterColon(line)
		case strings.Contains(lower, "overall-health self-assessment test result:"),
			strings.HasPrefix(lower, "smart health status:"):
			value := strings.ToUpper(afterColon(line))
			disk.Reason = ""
			switch {
			case value == "PASSED" || value == "OK" || value == "HEALTHY":
				disk.Status = StatusOK
			case strings.HasPrefix(value, "FAILED") || strings.HasPrefix(value, "CRITICAL"):
				disk.Status = StatusFail
				disk.Reason = line
			default:
				disk.Status = StatusUnknown
				disk.Reason = line
			}
		case strings.HasPrefix(line, "5 "), strings.HasPrefix(line, "197 "):
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			raw := fields[len(fields)-1]
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				continue
			}
			switch fields[1] {
			case "Reallocated_Sector_Ct":
				disk.Realloc = value
			case "Current_Pending_Sector":
				disk.Pending = value
			}
			disk.CountersSrc = "smartctl"
		case strings.HasPrefix(lower, "media and data integrity errors:"),
			strings.HasPrefix(lower, "percentage used:"):
			value, ok := parseSMARTNumber(afterColon(line))
			if !ok {
				continue
			}
			if strings.HasPrefix(lower, "media") {
				disk.MediaErrors = value
			} else {
				disk.Wear = value
			}
			disk.CountersSrc = "smartctl"
		}
	}

	if disk.Status == StatusUnknown && disk.Reason == "smartctl output not recognized" {
		return disk, fmt.Errorf("no health result line in smartctl output for %s", device)
	}
	return disk, nil
}

// parseSMARTNumber cleans a smartctl text-mode number before parsing:
// locale thousands separators ("17,785,059", "14 974" — the exact reason
// the direct path prefers JSON), a trailing percent sign and the bracketed
// human summary ("... [9.10 TB]") are stripped first.
func parseSMARTNumber(s string) (int64, bool) {
	if i := strings.Index(s, " ["); i >= 0 {
		s = s[:i]
	}
	s = strings.Map(func(r rune) rune {
		switch r {
		case ',', ' ', '.', '%', '\u00a0', '\u202f':
			return -1
		}
		return r
	}, s)
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n, err == nil
}

func afterColon(line string) string {
	if i := strings.Index(line, ":"); i >= 0 {
		return strings.TrimSpace(line[i+1:])
	}
	return ""
}

// skippedBlockPrefixes are pseudo-devices that must never reach SMART:
// loop-back mounts, RAM disks, compressed RAM, CD/DVD, device-mapper
// (LVM — a non-goal) and md arrays (collected as L2 via mdadm).
var skippedBlockPrefixes = []string{"loop", "ram", "zram", "sr", "fd", "dm-", "md"}

// listBlockDevices walks a /sys/block-style root and returns physical disk
// device names (e.g. "sda", "nvme0n1"), sorted. A device is skipped when its
// name has a pseudo-device prefix or its "removable" attribute reads 1.
// The root is a parameter, so tests can feed a fixture tree.
func listBlockDevices(sysBlockDir string) ([]string, error) {
	entries, err := os.ReadDir(sysBlockDir)
	if err != nil {
		return nil, fmt.Errorf("list block devices: %w", err)
	}
	var devices []string
	for _, entry := range entries {
		name := entry.Name()
		// /sys/block entries are symlinks to the real device directories
		// ("/sys/block/sda -> ../devices/..."); DirEntry.IsDir is an lstat
		// and would classify every real disk as "not a directory", leaving
		// the list silently empty. Stat through the link instead.
		full := filepath.Join(sysBlockDir, name)
		fi, err := os.Stat(full)
		if err != nil || !fi.IsDir() {
			continue
		}
		skip := false
		for _, prefix := range skippedBlockPrefixes {
			if strings.HasPrefix(name, prefix) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		removable, err := os.ReadFile(filepath.Join(sysBlockDir, name, "removable"))
		if err == nil && strings.TrimSpace(string(removable)) == "1" {
			continue
		}
		devices = append(devices, name)
	}
	sort.Strings(devices)
	return devices, nil
}

// knownPseudoFilesystems are mounts excluded from space monitoring: RAM and
// kernel pseudo-filesystems, container overlays and squashfs images.
var knownPseudoFilesystems = map[string]bool{
	"tmpfs":       true,
	"devtmpfs":    true,
	"overlay":     true,
	"squashfs":    true,
	"proc":        true,
	"sysfs":       true,
	"devpts":      true,
	"cgroup":      true,
	"cgroup2":     true,
	"mqueue":      true,
	"shm":         true,
	"efivarfs":    true,
	"bpf":         true,
	"debugfs":     true,
	"tracefs":     true,
	"configfs":    true,
	"pstore":      true,
	"securityfs":  true,
	"fusectl":     true,
	"hugetlbfs":   true,
	"nsfs":        true,
	"ramfs":       true,
	"autofs":      true,
	"binfmt_misc": true,
}

// mountEntry is one line of /proc/mounts.
type mountEntry struct {
	Device     string
	MountPoint string
	FSType     string
}

// parseProcMounts reads /proc/mounts content and returns real local
// filesystem mounts: known pseudo-FS types, duplicates-by-mountpoint and
// mountpoints that are not top-level filesystems are filtered out. Network
// filesystems (nfs, cifs, ...) are excluded too — they are non-goals.
//
// Mounts of the same underlying device are collapsed to the first mount
// point: systemd sandboxes (ProtectSystem, ProtectHome) bind-mount the same
// filesystem at extra paths, and inside such a namespace /proc/mounts shows
// them as "/dev/sda2[/boot]" — without collapsing, one partition would be
// counted (and reported) once per protected path.
func parseProcMounts(content string) []mountEntry {
	var out []mountEntry
	seen := map[string]bool{}
	seenDevice := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		device, mountPoint, fsType := fields[0], unescapeMount(fields[1]), fields[2]
		if knownPseudoFilesystems[fsType] {
			continue
		}
		switch fsType {
		case "nfs", "nfs4", "cifs", "smbfs", "ncpfs", "9p", "fuse.sshfs":
			continue
		}
		// Devices like "tmpfs", "none", "cgroup" with exotic types are
		// already filtered; remaining pseudo entries have no /dev/ source.
		if device != "none" && !strings.HasPrefix(device, "/dev/") {
			continue
		}
		if seen[mountPoint] {
			continue
		}
		seen[mountPoint] = true
		base := bindMountDevice(device)
		if seenDevice[base] {
			continue
		}
		seenDevice[base] = true
		out = append(out, mountEntry{Device: device, MountPoint: mountPoint, FSType: fsType})
	}
	return out
}

// bindMountDevice strips the subpath suffix systemd uses for bind mounts of
// a subdirectory of a filesystem ("/dev/sda2[/boot]" → "/dev/sda2"), so
// same-device mounts collapse. Plain device names pass through unchanged.
func bindMountDevice(device string) string {
	if i := strings.Index(device, "["); i >= 0 {
		return device[:i]
	}
	return device
}

// unescapeMount converts the octal escapes of /proc/mounts (space as \040)
// back to plain text.
func unescapeMount(path string) string {
	if !strings.Contains(path, "\\") {
		return path
	}
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] == '\\' && i+3 < len(path) {
			if v, err := strconv.ParseUint(path[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(path[i])
	}
	return b.String()
}
