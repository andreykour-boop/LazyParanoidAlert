//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"siren/internal/config"
)

func TestPlanFirstRun(t *testing.T) {
	exe := installExePath()

	cases := []struct {
		name    string
		exePath string
		svc     string
		cfgOK   bool
		want    firstRunActions
	}{
		{
			name: "fresh machine, exe from elsewhere", exePath: `C:\Users\u\Downloads\siren.exe`,
			svc: "не установлена", cfgOK: false,
			want: firstRunActions{copySelf: true, bootstrapConfig: true, installService: true},
		},
		{
			name: "relaunched from install dir", exePath: exe,
			svc: "не установлена", cfgOK: false,
			want: firstRunActions{bootstrapConfig: true, installService: true},
		},
		{
			name: "repeat launch: nothing repeats", exePath: exe,
			svc: "работает", cfgOK: true,
			want: firstRunActions{},
		},
		{
			name: "update: replace the installed copy", exePath: `C:\Users\u\Downloads\siren-new.exe`,
			svc: "работает", cfgOK: true,
			want: firstRunActions{copySelf: true, stopService: true},
		},
		{
			name: "update while stopped", exePath: `C:\Users\u\Downloads\siren-new.exe`,
			svc: "остановлена", cfgOK: true,
			want: firstRunActions{copySelf: true},
		},
		{
			name: "exe in place, service removed earlier", exePath: exe,
			svc: "не установлена", cfgOK: true,
			want: firstRunActions{installService: true},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planFirstRun(c.exePath, c.svc, cfgPathFor(t, c.cfgOK))
			if got != c.want {
				t.Errorf("plan = %+v, want %+v", got, c.want)
			}
		})
	}
}

// cfgPathFor returns a real config path: an existing temp file when
// exists is true, a path in a missing directory otherwise.
func cfgPathFor(t *testing.T, exists bool) string {
	t.Helper()
	if !exists {
		return filepath.Join(t.TempDir(), "no-such-dir", "config.json")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigIsTemplate(t *testing.T) {
	if !configIsTemplate(nil) {
		t.Error("nil config counts as template")
	}
	tpl := &config.Config{Telegram: config.Telegram{Token: placeholderToken, ChatID: placeholderChatID}}
	if !configIsTemplate(tpl) {
		t.Error("placeholder credentials count as template")
	}
	empty := &config.Config{}
	if !configIsTemplate(empty) {
		t.Error("empty credentials count as template")
	}
	real := &config.Config{Telegram: config.Telegram{Token: "123456:AA-real", ChatID: "-42"}}
	if configIsTemplate(real) {
		t.Error("real credentials must not count as template")
	}
}

func TestDefaultAgentConfigIsValid(t *testing.T) {
	// Save validates: the bootstrap config must pass the real validator
	// or the first run would produce a config the agent cannot load.
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, defaultAgentConfig("TEST-PC")); err != nil {
		t.Fatalf("save bootstrap config: %v", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("load bootstrap config: %v", err)
	}
	if got.AgentName != "TEST-PC" || got.Profile != config.ProfileDesktop {
		t.Errorf("identity lost: %+v", got)
	}
	if len(got.Traps) == 0 {
		t.Error("profile traps lost")
	}
}

func TestBootstrapConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := bootstrapConfig(path); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	host, _ := os.Hostname()
	if cfg.AgentName != host {
		t.Errorf("agent_name = %q, want hostname %q", cfg.AgentName, host)
	}
	if !configIsTemplate(cfg) {
		t.Error("bootstrap config must carry placeholders")
	}

	// An existing config is never overwritten.
	cfg.Telegram.Token = "123456:AA-real"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save modified: %v", err)
	}
	if err := bootstrapConfig(path); err != nil {
		t.Fatalf("bootstrap over existing: %v", err)
	}
	again, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again.Telegram.Token != "123456:AA-real" {
		t.Error("existing config was overwritten by bootstrap")
	}
}

func TestHasConfigFlag(t *testing.T) {
	for _, ok := range [][]string{
		{"-config", "x.json"},
		{"--config=x.json"},
		{"-config=y.json"},
		{"a", "-config", "b"},
	} {
		if !hasConfigFlag(ok) {
			t.Errorf("hasConfigFlag(%v) = false", ok)
		}
	}
	for _, no := range [][]string{
		{""},
		{"-console"},
		{"-configx"},
	} {
		if hasConfigFlag(no) {
			t.Errorf("hasConfigFlag(%v) = true", no)
		}
	}
}

func TestInstallPaths(t *testing.T) {
	if dir := installDir(); !strings.HasSuffix(dir, `siren`) {
		t.Errorf("installDir = %q", dir)
	}
	if exe := installExePath(); filepath.Dir(exe) != installDir() || filepath.Base(exe) != "siren.exe" {
		t.Errorf("installExePath = %q", exe)
	}
	if cfg := installCfgPath(); filepath.Dir(cfg) != installDir() || filepath.Base(cfg) != "config.json" {
		t.Errorf("installCfgPath = %q", cfg)
	}
}
