package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"siren/internal/config"
	"siren/internal/notify"
)

func TestPlanBoot(t *testing.T) {
	cases := []struct {
		name   string
		exists bool
		prev   string
		cur    string
		want   bootAction
	}{
		{"no state", false, "", "id-2", bootFirstRun},
		{"corrupted identity", true, "", "id-2", bootFirstRun},
		{"same identity", true, "id-1", "id-1", bootSameBoot},
		{"case-insensitive match", true, "ID-1", "id-1", bootSameBoot},
		{"new identity", true, "id-1", "id-2", bootNewBoot},
		{"whitespace only differs", true, " id-1 ", "id-1", bootSameBoot},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := planBoot(c.exists, c.prev, c.cur); got != c.want {
				t.Errorf("planBoot = %v, want %v", got, c.want)
			}
		})
	}
}

func TestBootStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := loadBootState(dir); ok || err != nil {
		t.Fatalf("missing state: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	st := bootState{Identity: "id-1", At: time.Now().Truncate(time.Second)}
	if err := saveBootState(dir, st); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := loadBootState(dir)
	if !ok || err != nil {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got.Identity != st.Identity || !got.At.Equal(st.At) {
		t.Errorf("round trip lost data: %+v vs %+v", got, st)
	}
	// Corrupted JSON reads as an error, not a panic.
	if err := os.WriteFile(bootStatePath(dir), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadBootState(dir); err == nil {
		t.Error("corrupted state must return an error")
	}
}

// bootTestHooks swaps the platform hooks for fakes and restores them.
func bootTestHooks(t *testing.T, identity func() (string, error), classify func() (notify.BootFacts, error)) {
	t.Helper()
	oi, oc := bootIdentityFn, bootClassifyFn
	bootIdentityFn, bootClassifyFn = identity, classify
	t.Cleanup(func() { bootIdentityFn, bootClassifyFn = oi, oc })
}

func TestRunBootMatrix(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dir := t.TempDir()
	cfg := &config.Config{AgentName: "TEST-PC", DataDir: dir}
	if !filepath.IsAbs(dir) {
		t.Fatal("temp dir must be absolute for agentDataDir passthrough")
	}
	direct := make(chan string, 4)
	ctx := context.Background()

	idFail := func() (string, error) { return "", errors.New("no identity") }
	classifyFail := func() (notify.BootFacts, error) { return notify.BootFacts{}, errors.New("no journal") }

	// 1. Identity unavailable: no alert, no state.
	bootTestHooks(t, idFail, nil)
	if err := runBoot(ctx, cfg, "", direct, log); err != nil {
		t.Fatalf("identity failure must not error: %v", err)
	}
	if _, ok, _ := loadBootState(dir); ok {
		t.Error("identity failure must not write state")
	}

	// 2. First run: silent record, no alert.
	bootTestHooks(t, func() (string, error) { return "id-1", nil }, nil)
	if err := runBoot(ctx, cfg, "", direct, log); err != nil {
		t.Fatalf("first run: %v", err)
	}
	select {
	case txt := <-direct:
		t.Errorf("first run must not alert, got %q", txt)
	default:
	}
	st, ok, _ := loadBootState(dir)
	if !ok || st.Identity != "id-1" {
		t.Errorf("first run must record identity, got %+v", st)
	}

	// 3. Same identity (service restart): silent, state untouched.
	before, _, _ := loadBootState(dir)
	if err := runBoot(ctx, cfg, "", direct, log); err != nil {
		t.Fatalf("same boot: %v", err)
	}
	select {
	case txt := <-direct:
		t.Errorf("service restart must not alert, got %q", txt)
	default:
	}
	after, _, _ := loadBootState(dir)
	if !after.At.Equal(before.At) {
		t.Error("same boot must not rewrite state")
	}

	// 4. New boot, classification unavailable: level-1 fallback alert.
	bootTestHooks(t, func() (string, error) { return "id-2", nil }, nil)
	if err := runBoot(ctx, cfg, "192.0.2.26", direct, log); err != nil {
		t.Fatalf("new boot (no classify): %v", err)
	}
	txt := recvDirect(t, direct)
	if !strings.Contains(txt, "детали прошлой сессии недоступны") {
		t.Errorf("fallback alert text wrong: %q", txt)
	}
	if !strings.Contains(txt, "TEST-PC") {
		t.Errorf("alert must name the machine: %q", txt)
	}
	st, _, _ = loadBootState(dir)
	if st.Identity != "id-2" {
		t.Errorf("new boot must save identity, got %q", st.Identity)
	}

	// 5. New boot, classification fails: still level-1 fallback, no error.
	bootTestHooks(t, func() (string, error) { return "id-3", nil }, classifyFail)
	if err := runBoot(ctx, cfg, "", direct, log); err != nil {
		t.Fatalf("new boot (classify error): %v", err)
	}
	if txt := recvDirect(t, direct); !strings.Contains(txt, "недоступны") {
		t.Errorf("classification failure must degrade to fallback: %q", txt)
	}
}

func recvDirect(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case txt := <-ch:
		return txt
	default:
		t.Fatal("expected an alert in the direct channel")
		return ""
	}
}

// TestAgentDataDir: relative data_dir resolves against the executable
// directory (the contract the service relies on), absolute passes through.
func TestAgentDataDir(t *testing.T) {
	cfg := &config.Config{DataDir: `C:\abs\data`}
	if got := agentDataDir(cfg); got != `C:\abs\data` {
		t.Errorf("absolute passthrough: %q", got)
	}
	exe, _ := os.Executable()
	cfg.DataDir = "data"
	want := filepath.Join(filepath.Dir(exe), "data")
	if got := agentDataDir(cfg); got != want {
		t.Errorf("relative resolution: got %q, want %q", got, want)
	}
	if got := agentDataDir(nil); got != "" {
		t.Errorf("nil config: %q", got)
	}
}
