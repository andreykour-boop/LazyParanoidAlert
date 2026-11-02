package health

import (
	"errors"
	"strings"
	"testing"
)

// Fixture: /proc/mounts of a typical server with pseudo and network mounts.
const procMountsFixture = `sysfs /sys sysfs rw,nosuid,nodev,noexec,relatime 0 0
proc /proc proc rw,nosuid,nodev,noexec,relatime 0 0
udev /dev devtmpfs rw,nosuid,noexec,relatime 0 0
devpts /dev/pts devpts rw,nosuid,noexec,relatime 0 0
tmpfs /run tmpfs rw,nosuid,nodev,noexec,relatime 0 0
/dev/sda1 / ext4 rw,relatime,errors=remount-ro 0 0
/dev/sda2 /home xfs rw,relatime,attr2,inode64,noquota 0 0
/dev/sdb1 /data ext4 rw,relatime,data=ordered 0 0
/dev/md0 /srv ext4 rw,relatime,data=ordered 0 0
tmpfs /run/user/1000 tmpfs rw,nosuid,nodev,relatime,size=812288k 0 0
overlay /var/lib/docker/overlay2/abc/merged overlay rw,relatime 0 0
cgroup2 /sys/fs/cgroup cgroup2 rw,nosuid,nodev,noexec 0 0
server:/export /mnt/nas nfs4 rw,relatime 0 0
//filesrv/share /mnt/share cifs rw,relatime 0 0
squashfs /ro squashfs ro 0 0
/dev/sdc1 /mnt/with\040space ext4 rw 0 0
`

func TestParseProcMountsFiltersPseudoFS(t *testing.T) {
	entries := parseProcMounts(procMountsFixture)

	var got []string
	for _, e := range entries {
		got = append(got, e.MountPoint)
	}
	want := []string{"/", "/home", "/data", "/srv", "/mnt/with space"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("mounts = %q, want %q", got, want)
	}
}

func TestParseProcMountsTypes(t *testing.T) {
	entries := parseProcMounts(procMountsFixture)
	byMount := map[string]mountEntry{}
	for _, e := range entries {
		byMount[e.MountPoint] = e
	}
	if e := byMount["/"]; e.FSType != "ext4" || e.Device != "/dev/sda1" {
		t.Errorf("root entry wrong: %+v", e)
	}
	// md arrays come through with their /dev/mdN device — volume layer.
	if e := byMount["/srv"]; e.Device != "/dev/md0" {
		t.Errorf("md mount device wrong: %+v", e)
	}
	if e := byMount["/mnt/with space"]; e.Device != "/dev/sdc1" {
		t.Errorf("octal-escaped mount point not decoded: %+v", e)
	}
}

func TestParseProcMountsEmpty(t *testing.T) {
	if entries := parseProcMounts(""); len(entries) != 0 {
		t.Errorf("empty /proc/mounts must yield no entries, got %+v", entries)
	}
}

// TestParseProcMountsSameDeviceCollapsed: inside a systemd sandbox
// (ProtectSystem=full, ProtectHome=read-only) /proc/mounts shows bind mounts
// of the same filesystem at extra paths ("/dev/sda2[/boot]"); one partition
// must be counted once, at its first (real) mount point.
func TestParseProcMountsSameDeviceCollapsed(t *testing.T) {
	const sandboxMounts = `/dev/sda2 / ext4 rw,relatime 0 0
/dev/sda2[/boot] /boot ext4 rw,relatime 0 0
/dev/sda2[/etc] /etc ext4 rw,relatime 0 0
/dev/sda2[/home] /home ext4 rw,relatime 0 0
/dev/sda2[/root] /root ext4 rw,relatime 0 0
/dev/sda1 /boot/efi vfat rw,relatime 0 0
`
	var got []string
	for _, e := range parseProcMounts(sandboxMounts) {
		got = append(got, e.MountPoint)
	}
	want := []string{"/", "/boot/efi"} // distinct devices only
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("mounts = %q, want %q", got, want)
	}
}

// TestVolumesSourceCollect: statfs runs only for filtered mounts, vanished
// mounts are skipped, zero-capacity mounts skipped.
func TestVolumesSourceCollect(t *testing.T) {
	var statfed []string
	s := &volumesSource{
		mounts: func() ([]mountEntry, error) {
			return parseProcMounts(procMountsFixture), nil
		},
		statfs: func(path string) (uint64, uint64, error) {
			statfed = append(statfed, path)
			switch path {
			case "/":
				return gb(100), gb(50), nil
			case "/home":
				return gb(200), gb(4), nil
			case "/srv":
				return gb(1000), gb(900), nil
			case "/data":
				return 0, 0, errors.New("stale file handle") // vanished mid-scan
			case "/mnt/with space":
				return 0, 0, nil // zero capacity -> skip
			}
			t.Errorf("statfs called for unexpected path %q", path)
			return 0, 0, nil
		},
	}

	vols, err := s.CollectVolumes()
	if err != nil {
		t.Fatalf("CollectVolumes: %v", err)
	}
	if len(vols) != 3 {
		t.Fatalf("volumes = %+v, want 3", vols)
	}
	if vols[0].Mount != "/" || vols[0].TotalBytes != gb(100) || vols[0].FreeBytes != gb(50) {
		t.Errorf("root volume wrong: %+v", vols[0])
	}
	if vols[1].Mount != "/home" || vols[1].FreeBytes != gb(4) {
		t.Errorf("/home volume wrong: %+v", vols[1])
	}
	if vols[2].Mount != "/srv" {
		t.Errorf("/srv volume missing: %+v", vols[2])
	}
	// statfs must have been attempted exactly for the real mounts.
	if len(statfed) != 5 {
		t.Errorf("statfs attempts = %v, want 5 real mounts", statfed)
	}
}

func TestVolumesSourceMountsError(t *testing.T) {
	s := &volumesSource{
		mounts: func() ([]mountEntry, error) { return nil, errors.New("/proc/mounts unreadable") },
		statfs: func(string) (uint64, uint64, error) { return 0, 0, nil },
	}
	if _, err := s.CollectVolumes(); err == nil {
		t.Fatal("expected error when mounts are unreadable")
	}
}
