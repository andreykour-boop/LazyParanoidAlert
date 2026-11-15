package config

import "testing"

// TestKitExampleLoads guards the shipped Linux kit templates against schema
// drift: every kit's config example must load and validate.
func TestKitExampleLoads(t *testing.T) {
	for _, kit := range []string{
		"../../dist/siren-linux-amd64-kit",
		"../../dist/siren-linux-arm64-kit",
	} {
		cfg, err := Load(kit + "/config.example.json")
		if err != nil {
			t.Fatalf("%s: kit config.example.json does not load: %v", kit, err)
		}
		if cfg.Profile != ProfileServer {
			t.Errorf("%s: profile = %q, want %q", kit, cfg.Profile, ProfileServer)
		}
		if len(cfg.Traps) == 0 {
			t.Errorf("%s: server profile must expand to traps", kit)
		}
	}
}
