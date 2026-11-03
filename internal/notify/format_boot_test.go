package notify

import (
	"strings"
	"testing"
	"time"
)

var bootTestNow = time.Date(2026, 11, 3, 8, 12, 0, 0, time.Local)

func TestBootAlertTextCleanReboot(t *testing.T) {
	f := BootFacts{
		Known:     true,
		Clean:     true,
		Action:    "reboot",
		Initiator: `ANDREY\Andrey (explorer.exe, «Перезагрузка»)`,
		LastAt:    bootTestNow.Add(-9*time.Hour - 31*time.Minute),
		Now:       bootTestNow,
	}
	txt := BootAlertText("TEST-PC", "192.0.2.26", f)
	for _, want := range []string{
		"🔔 TEST-PC (192.0.2.26): машина ПЕРЕЗАГРУЗИЛАСЬ",
		"время загрузки:",
		"завершена штатно в",
		"инициатор: ANDREY\\Andrey (explorer.exe, «Перезагрузка»)",
		"простой: 9 ч 31 мин",
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("clean reboot text misses %q:\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "АВАРИЙНОЕ") {
		t.Errorf("clean reboot must not be marked as dirty:\n%s", txt)
	}
}

func TestBootAlertTextPowerOnAfterShutdown(t *testing.T) {
	f := BootFacts{Known: true, Clean: true, Action: "shutdown", LastAt: bootTestNow.Add(-2 * time.Hour), Now: bootTestNow}
	txt := BootAlertText("TEST-PC", "", f)
	if !strings.Contains(txt, "машина включена после ВЫКЛЮЧЕНИЯ") {
		t.Errorf("shutdown headline wrong:\n%s", txt)
	}
	if !strings.Contains(txt, "простой: 2 ч 0 мин") {
		t.Errorf("downtime wrong:\n%s", txt)
	}
	// Empty initiator is omitted without breaking the alert.
	if strings.Contains(txt, "инициатор:") {
		t.Errorf("absent initiator must be omitted:\n%s", txt)
	}
}

func TestBootAlertTextDirty(t *testing.T) {
	last := bootTestNow.Add(-47 * time.Hour)
	f := BootFacts{Known: true, Clean: false, LastAt: last, Now: bootTestNow}
	txt := BootAlertText("SRV-10-5", "198.51.100.10", f)
	for _, want := range []string{
		"машина загрузилась",
		"АВАРИЙНОЕ завершение",
		"потеря питания / BSOD / паника",
		"последняя активность:",
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("dirty boot text misses %q:\n%s", want, txt)
		}
	}
	// Dirty boots have no reliable shutdown time: no downtime line.
	if strings.Contains(txt, "простой:") {
		t.Errorf("dirty boot must not claim a downtime:\n%s", txt)
	}
	// Initiator on a dirty boot is absent by construction.
	if strings.Contains(txt, "инициатор:") {
		t.Errorf("dirty boot must not show an initiator:\n%s", txt)
	}
}

func TestBootAlertTextFallback(t *testing.T) {
	f := BootFacts{Now: bootTestNow}
	txt := BootAlertText("TEST-PC", "", f)
	if !strings.Contains(txt, "машина загрузилась") || !strings.Contains(txt, "детали прошлой сессии недоступны") {
		t.Errorf("fallback text wrong:\n%s", txt)
	}
	if strings.Contains(txt, "инициатор") || strings.Contains(txt, "простой") {
		t.Errorf("fallback must carry the bare fact only:\n%s", txt)
	}
}
