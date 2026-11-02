package health

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeLookup returns a LookPath-like function that "finds" binaries whose
// names are listed in the found set.
func fakeLookup(found map[string]bool) func(string) (string, error) {
	return func(file string) (string, error) {
		base := file
		if i := strings.LastIndexAny(file, "/\\"); i >= 0 {
			base = file[i+1:]
		}
		if found[base] {
			return file, nil
		}
		return "", errors.New("not found")
	}
}

func splitPath(p string) []string {
	return strings.FieldsFunc(p, func(r rune) bool { return r == ':' || r == ';' })
}

func TestFindVendorCLI(t *testing.T) {
	// storcli present in the fake PATH -> detected first (priority order).
	cli := findVendorCLI(splitPath(`C:\bin;C:\usr\local\bin`), fakeLookup(map[string]bool{"storcli": true}))
	if cli == nil || cli.Name != "storcli" {
		t.Fatalf("cli = %+v, want storcli", cli)
	}
	if cli.SmartctlDeviceType != "megaraid" {
		t.Errorf("device type = %q, want megaraid", cli.SmartctlDeviceType)
	}

	// Only arcconf present.
	cli = findVendorCLI(splitPath("/opt:/usr/sbin"), fakeLookup(map[string]bool{"arcconf": true}))
	if cli == nil || cli.Name != "arcconf" {
		t.Fatalf("cli = %+v, want arcconf", cli)
	}

	// Nothing found -> nil (no controller level; L1 is just what smartctl sees).
	if cli := findVendorCLI(splitPath("/usr/bin"), fakeLookup(map[string]bool{})); cli != nil {
		t.Errorf("cli = %+v, want nil for empty PATH", cli)
	}

	// Unix-style PATH with storcli.
	cli = findVendorCLI(splitPath("/opt/storcli:/usr/bin"), fakeLookup(map[string]bool{"storcli": true}))
	if cli == nil || cli.Name != "storcli" {
		t.Fatalf("unix PATH: cli = %+v, want storcli", cli)
	}
}

// TestFindVendorCLIRealPath: detection works with the real exec.LookPath by
// building a directory that actually contains the binary.
func TestFindVendorCLIRealPath(t *testing.T) {
	dir := t.TempDir()
	name := "perccli"
	if runtime.GOOS == "windows" {
		name = "perccli.exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte{}, 0o755); err != nil {
		t.Fatalf("write fixture binary: %v", err)
	}
	cli := findVendorCLI([]string{dir}, nil)
	if cli == nil {
		t.Fatal("perccli not detected in real PATH dir")
	}
	if cli.Name != "perccli" {
		t.Errorf("cli = %q, want perccli", cli.Name)
	}
}

// TestVendorCLISourceCollect: disks behind the controller are parsed until
// an empty slot ends the enumeration; an unresponsive controller is a
// source error, never silence.
func TestVendorCLISourceCollect(t *testing.T) {
	s := &vendorCLISource{
		cli:      knownVendorCLIs[0], // storcli
		smartctl: "/usr/sbin/smartctl",
		probe: func(diskIndex int, deviceType string) (string, error) {
			if deviceType != "megaraid" {
				t.Errorf("deviceType = %q, want megaraid", deviceType)
			}
			switch diskIndex {
			case 0:
				return smartctlSATAOK, nil
			case 1:
				return smartctlSATAFail, nil
			default:
				return "", errors.New("no drive at index") // empty slot
			}
		},
	}

	disks, err := s.CollectL1()
	if err != nil {
		t.Fatalf("CollectL1: %v", err)
	}
	if len(disks) != 2 {
		t.Fatalf("disks = %+v, want 2", disks)
	}
	if disks[0].Name != "storcli:0" || disks[0].Status != StatusOK {
		t.Errorf("disk 0 wrong: %+v", disks[0])
	}
	if disks[1].Name != "storcli:1" || disks[1].Status != StatusFail {
		t.Errorf("disk 1 wrong: %+v", disks[1])
	}
}

func TestVendorCLISourceNoSmartctl(t *testing.T) {
	s := &vendorCLISource{
		cli:      knownVendorCLIs[0],
		smartctl: "", // smartctl absent
	}
	_, err := s.CollectL1()
	if err == nil {
		t.Fatal("expected error when smartctl is missing behind a controller")
	}
	if !strings.Contains(err.Error(), "storcli") {
		t.Errorf("error must name the controller tool: %v", err)
	}
}

func TestVendorCLISourceUnresponsive(t *testing.T) {
	s := &vendorCLISource{
		cli:      knownVendorCLIs[2], // arcconf
		smartctl: "/usr/sbin/smartctl",
		probe: func(int, string) (string, error) {
			return "", errors.New("controller does not respond")
		},
	}
	_, err := s.CollectL1()
	if err == nil {
		t.Fatal("expected error for unresponsive controller")
	}
	if !strings.Contains(err.Error(), "arcconf") {
		t.Errorf("error must name the controller: %v", err)
	}
}

func TestVendorCLISourceFoundButUnreadable(t *testing.T) {
	s := &vendorCLISource{
		cli:      knownVendorCLIs[0],
		smartctl: "/usr/sbin/smartctl",
		probe: func(int, string) (string, error) {
			return "", nil // controller found, no disks readable ever
		},
	}
	_, err := s.CollectL1()
	if err == nil {
		t.Fatal("expected error when controller yields no disks at all")
	}
	if !strings.Contains(err.Error(), "не прочитаны") {
		t.Errorf("error must explain the situation: %v", err)
	}
}
