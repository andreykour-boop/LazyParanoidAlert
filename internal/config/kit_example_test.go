package config

import "testing"

// TestKitExampleLoads guards the shipped Linux kit template against schema
// drift: the file must always load and validate.
func TestKitExampleLoads(t *testing.T) {
	cfg, err := Load("../../dist/siren-linux-kit/config.example.json")
	if err != nil {
		t.Fatalf("kit config.example.json does not load: %v", err)
	}
	if cfg.Profile != ProfileServer {
		t.Errorf("profile = %q, want %q", cfg.Profile, ProfileServer)
	}
	if len(cfg.Traps) == 0 {
		t.Error("server profile must expand to traps")
	}
}
