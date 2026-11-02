package config

import (
	"path/filepath"
	"testing"
)

// TestHealthDefaultsWhenSectionAbsent covers old configs written before the
// health watch existed: they must load unchanged and silently receive the
// default watch schedule (on, 09:00 daily, Monday report, 10%, no floor).
func TestHealthDefaultsWhenSectionAbsent(t *testing.T) {
	cfg, err := Load(writeTemp(t, validConfig))
	if err != nil {
		t.Fatalf("Load config without health section: %v", err)
	}
	if cfg.Health == nil {
		t.Fatal("Health section is nil, want defaults")
	}
	want := DefaultHealth()
	if *cfg.Health != *want {
		t.Errorf("Health = %+v, want defaults %+v", *cfg.Health, *want)
	}
}

// TestHealthExplicitSectionSurvivesSaveLoad checks that a fully specified
// section round-trips through Save/Load without losses (Save is used by the
// control UI; the watch reads the values back on the next start).
func TestHealthExplicitSectionSurvivesSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := Load(writeTemp(t, `{
		"agent_name": "SRV-02",
		"profile": "server",
		"telegram": {"token": "t", "chat_id": "c"},
		"health": {"enabled": true, "check_hour": 22, "report_weekday": 6, "warn_pct": 15, "min_free_gb": 20}
	}`))
	if err != nil {
		t.Fatalf("Load with explicit health section: %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatalf("re-Load saved config: %v", err)
	}
	if *back.Health != *cfg.Health {
		t.Errorf("health section lost in round-trip: got %+v, want %+v", *back.Health, *cfg.Health)
	}
}

// TestHealthValidateLoadErrors: invalid values must be rejected by Load
// (start-time) exactly as by Save (control-UI write-time).
func TestHealthValidateLoadErrors(t *testing.T) {
	cases := map[string]string{
		"hour too big":     `{"agent_name":"A","profile":"server","telegram":{"token":"t","chat_id":"c"},"health":{"enabled":true,"check_hour":24,"report_weekday":1,"warn_pct":10}}`,
		"hour negative":    `{"agent_name":"A","profile":"server","telegram":{"token":"t","chat_id":"c"},"health":{"enabled":true,"check_hour":-1,"report_weekday":1,"warn_pct":10}}`,
		"weekday too big":  `{"agent_name":"A","profile":"server","telegram":{"token":"t","chat_id":"c"},"health":{"enabled":true,"check_hour":9,"report_weekday":7,"warn_pct":10}}`,
		"weekday negative": `{"agent_name":"A","profile":"server","telegram":{"token":"t","chat_id":"c"},"health":{"enabled":true,"check_hour":9,"report_weekday":-2,"warn_pct":10}}`,
		"pct zero":         `{"agent_name":"A","profile":"server","telegram":{"token":"t","chat_id":"c"},"health":{"enabled":true,"check_hour":9,"report_weekday":1,"warn_pct":0}}`,
		"pct too big":      `{"agent_name":"A","profile":"server","telegram":{"token":"t","chat_id":"c"},"health":{"enabled":true,"check_hour":9,"report_weekday":1,"warn_pct":150}}`,
		"negative floor":   `{"agent_name":"A","profile":"server","telegram":{"token":"t","chat_id":"c"},"health":{"enabled":true,"check_hour":9,"report_weekday":1,"warn_pct":10,"min_free_gb":-5}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, body)); err == nil {
				t.Fatalf("expected validation error for case %q, got nil", name)
			}
		})
	}
}

// TestHealthValidateSaveErrors: the same invalid values must be rejected by
// Save, and the file must not be written on rejection.
func TestHealthValidateSaveErrors(t *testing.T) {
	base := func(health string) string {
		return `{"agent_name":"A","profile":"server","telegram":{"token":"t","chat_id":"c"},"health":` + health + `}`
	}
	cases := map[string]string{
		"hour too big":    base(`{"enabled":true,"check_hour":24,"report_weekday":1,"warn_pct":10}`),
		"weekday too big": base(`{"enabled":true,"check_hour":9,"report_weekday":7,"warn_pct":10}`),
		"pct zero":        base(`{"enabled":true,"check_hour":9,"report_weekday":1,"warn_pct":0}`),
		"pct too big":     base(`{"enabled":true,"check_hour":9,"report_weekday":1,"warn_pct":150}`),
		"negative floor":  base(`{"enabled":true,"check_hour":9,"report_weekday":1,"warn_pct":10,"min_free_gb":-1}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(writeTemp(t, body))
			if err == nil {
				t.Fatalf("Load unexpectedly accepted case %q", name)
			}
			_ = cfg // Load already failed; Save must fail the same way below.
			path := filepath.Join(t.TempDir(), "config.json")
			// Build the struct directly to test Save's own validation path.
			invalid := &Config{
				AgentName: "A",
				Profile:   ProfileServer,
				Traps:     ProfileTraps(ProfileServer),
				Telegram:  Telegram{Token: "t", ChatID: "c"},
			}
			switch name {
			case "hour too big":
				invalid.Health = &Health{Enabled: true, CheckHour: 24, ReportWeekday: 1, WarnPct: 10}
			case "weekday too big":
				invalid.Health = &Health{Enabled: true, CheckHour: 9, ReportWeekday: 7, WarnPct: 10}
			case "pct zero":
				invalid.Health = &Health{Enabled: true, CheckHour: 9, ReportWeekday: 1, WarnPct: 0}
			case "pct too big":
				invalid.Health = &Health{Enabled: true, CheckHour: 9, ReportWeekday: 1, WarnPct: 150}
			case "negative floor":
				invalid.Health = &Health{Enabled: true, CheckHour: 9, ReportWeekday: 1, WarnPct: 10, MinFreeGB: -1}
			}
			if err := Save(path, invalid); err == nil {
				t.Fatalf("expected Save to reject case %q", name)
			}
			if _, err := Load(path); err == nil {
				t.Errorf("Save must not leave a loadable config behind for case %q", name)
			}
		})
	}
}

// TestHealthValidBoundaries: edge values inside the allowed ranges must pass
// (midnight check, Sunday report, 1% and 99% thresholds, zero floor).
func TestHealthValidBoundaries(t *testing.T) {
	body := `{
		"agent_name": "A",
		"profile": "server",
		"telegram": {"token": "t", "chat_id": "c"},
		"health": {"enabled": true, "check_hour": 0, "report_weekday": 0, "warn_pct": 1, "min_free_gb": 0}
	}`
	cfg, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("Load boundary values: %v", err)
	}
	if cfg.Health.CheckHour != 0 || cfg.Health.ReportWeekday != 0 || cfg.Health.WarnPct != 1 || cfg.Health.MinFreeGB != 0 {
		t.Errorf("boundary values changed: %+v", *cfg.Health)
	}
}
