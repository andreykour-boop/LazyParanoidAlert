package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTemp writes content to a temporary JSON file and returns its path.
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

const validConfig = `{
  "agent_name": "WIN-PC01",
  "profile": "desktop",
  "telegram": {"token": "123:abc", "chat_id": "-100100"},
  "allowlist": ["192.168.1.10", "10.0.0.0/24"],
  "name_map": {"192.168.1.55": "pc-ivanov"}
}`

func TestLoadValid(t *testing.T) {
	cfg, err := Load(writeTemp(t, validConfig))
	if err != nil {
		t.Fatalf("Load valid config: %v", err)
	}
	if cfg.AgentName != "WIN-PC01" {
		t.Errorf("AgentName = %q, want WIN-PC01", cfg.AgentName)
	}
	if cfg.AggregateWindowSeconds != 300 {
		t.Errorf("AggregateWindowSeconds = %d, want default 300", cfg.AggregateWindowSeconds)
	}
	if cfg.DataDir != "data" {
		t.Errorf("DataDir = %q, want default \"data\"", cfg.DataDir)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want default \"info\"", cfg.LogLevel)
	}
	// Desktop profile must include the anomalous-for-a-workstation 80/443.
	ports := make(map[int]Trap, len(cfg.Traps))
	for _, tr := range cfg.Traps {
		ports[tr.Port] = tr
	}
	for _, want := range []int{22, 3389, 80, 443, 3306} {
		if _, ok := ports[want]; !ok {
			t.Errorf("desktop profile: port %d missing from traps", want)
		}
	}
	// Trusted-LAN background ports must never be traps.
	for _, banned := range []int{445, 135, 139, 53, 88, 389} {
		if tr, ok := ports[banned]; ok && tr.Enabled {
			t.Errorf("port %d must not be a trap in any profile", banned)
		}
	}
}

func TestLoadServerProfileNo80(t *testing.T) {
	cfg, err := Load(writeTemp(t, `{
		"agent_name": "SRV-02",
		"profile": "server",
		"telegram": {"token": "t", "chat_id": "c"}
	}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, tr := range cfg.Traps {
		if tr.Port == 80 || tr.Port == 443 {
			t.Errorf("server profile must not trap port %d", tr.Port)
		}
	}
}

func TestLoadExplicitTrapsWin(t *testing.T) {
	cfg, err := Load(writeTemp(t, `{
		"agent_name": "A",
		"traps": [{"port": 2222, "enabled": true, "emulator": "ssh"}],
		"telegram": {"token": "t", "chat_id": "c"}
	}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Traps) != 1 || cfg.Traps[0].Port != 2222 {
		t.Fatalf("explicit traps must be used as-is, got %+v", cfg.Traps)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]string{
		"no agent name":     `{"telegram":{"token":"t","chat_id":"c"},"traps":[{"port":80,"enabled":true,"emulator":"http"}]}`,
		"unknown profile":   `{"agent_name":"A","profile":"router","telegram":{"token":"t","chat_id":"c"}}`,
		"no traps":          `{"agent_name":"A","telegram":{"token":"t","chat_id":"c"}}`,
		"port out of range": `{"agent_name":"A","traps":[{"port":70000,"enabled":true,"emulator":"http"}],"telegram":{"token":"t","chat_id":"c"}}`,
		"duplicate port":    `{"agent_name":"A","traps":[{"port":80,"enabled":true,"emulator":"http"},{"port":80,"enabled":true,"emulator":"ftp"}],"telegram":{"token":"t","chat_id":"c"}}`,
		"unknown emulator":  `{"agent_name":"A","traps":[{"port":80,"enabled":true,"emulator":"smtp"}],"telegram":{"token":"t","chat_id":"c"}}`,
		"no token":          `{"agent_name":"A","traps":[{"port":80,"enabled":true,"emulator":"http"}],"telegram":{"chat_id":"c"}}`,
		"no chat id":        `{"agent_name":"A","traps":[{"port":80,"enabled":true,"emulator":"http"}],"telegram":{"token":"t"}}`,
		"bad window":        `{"agent_name":"A","traps":[{"port":80,"enabled":true,"emulator":"http"}],"telegram":{"token":"t","chat_id":"c"},"aggregate_window_seconds":-5}`,
		"bad allowlist":     `{"agent_name":"A","traps":[{"port":80,"enabled":true,"emulator":"http"}],"telegram":{"token":"t","chat_id":"c"},"allowlist":["not-an-ip"]}`,
		"bad log level":     `{"agent_name":"A","traps":[{"port":80,"enabled":true,"emulator":"http"}],"telegram":{"token":"t","chat_id":"c"},"log_level":"loud"}`,
		"broken json":       `{"agent_name":`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, body)); err == nil {
				t.Fatalf("expected error for case %q, got nil", name)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}
