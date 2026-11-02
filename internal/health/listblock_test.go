package health

import (
	"os"
	"path/filepath"
	"testing"
)

// TestListBlockDevicesFollowsSymlinks: on real Linux /sys/block entries are
// symlinks to the device directories ("/sys/block/sda -> ../devices/...");
// the walk must stat through the links, or every real disk is silently
// skipped. On Windows symlink creation needs privileges, so the test skips
// when the OS refuses.
func TestListBlockDevicesFollowsSymlinks(t *testing.T) {
	root := t.TempDir()

	mk := func(name string) {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "removable"), []byte("0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("sda")

	// Real disks appear as symlinks — the case the live host exposed.
	if err := os.Symlink(filepath.Join(root, "sda"), filepath.Join(root, "sdb")); err != nil {
		t.Skip("symlinks unavailable on this host:", err)
	}
	// Pseudo-devices stay filtered even through a symlink.
	mk("loop0")
	if err := os.Symlink(filepath.Join(root, "loop0"), filepath.Join(root, "loop9")); err != nil {
		t.Skip("symlinks unavailable on this host:", err)
	}

	devices, err := listBlockDevices(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 || devices[0] != "sda" || devices[1] != "sdb" {
		t.Errorf("devices = %v, want [sda sdb] — symlinked disks must not be skipped", devices)
	}
}
