package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	src := &Config{
		AgentName:              "TEST-PC",
		Profile:                ProfileDesktop,
		Telegram:               Telegram{Token: "1:aa", ChatID: "-42"},
		AggregateWindowSeconds: 120,
		Allowlist:              []string{"192.168.1.0/24", "10.0.0.5:22,3389", "[::1]:22"},
		NameMap:                map[string]string{"10.0.0.5": "nas"},
		DataDir:                "data",
		LogLevel:               "debug",
		LogFile:                "siren.log",
	}
	if err := Save(path, src); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if got.AgentName != src.AgentName || got.Profile != src.Profile {
		t.Errorf("identity fields lost: %+v", got)
	}
	if got.AggregateWindowSeconds != 120 {
		t.Errorf("window = %d, want 120", got.AggregateWindowSeconds)
	}
	if len(got.Traps) == 0 {
		t.Error("profile traps lost on save")
	}
	if got.Traps[0].Port != 21 || !got.Traps[0].Enabled {
		t.Errorf("first trap = %+v, want enabled 21/ftp", got.Traps[0])
	}
	if len(got.Allowlist) != 3 || got.NameMap["10.0.0.5"] != "nas" {
		t.Errorf("allowlist/name_map lost: %v %v", got.Allowlist, got.NameMap)
	}
	// Entries with a port suffix must round-trip verbatim (the string is
	// the storage format; the port list is parsed at match time).
	if got.Allowlist[1] != "10.0.0.5:22,3389" || got.Allowlist[2] != "[::1]:22" {
		t.Errorf("port-suffixed entries mangled: %v", got.Allowlist)
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	bad := &Config{AgentName: "X", Traps: []Trap{{Port: 70000, Enabled: true, Emulator: EmulatorFTP}},
		Telegram: Telegram{Token: "t", ChatID: "c"}}
	if err := Save(path, bad); err == nil {
		t.Fatal("Save must reject an out-of-range port")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("failed Save must not leave a config file behind")
	}
	// A .tmp sibling must be cleaned up too.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("failed Save must not leave a temp file behind")
	}
}

func TestSaveOverExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	first := &Config{AgentName: "ONE", Profile: ProfileServer,
		Telegram: Telegram{Token: "t", ChatID: "1"}}
	if err := Save(path, first); err != nil {
		t.Fatalf("first save: %v", err)
	}
	second := &Config{AgentName: "TWO", Profile: ProfileServer,
		Telegram: Telegram{Token: "t", ChatID: "1"}}
	if err := Save(path, second); err != nil {
		t.Fatalf("overwrite save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.AgentName != "TWO" {
		t.Errorf("agent_name = %q, want TWO", got.AgentName)
	}
}
