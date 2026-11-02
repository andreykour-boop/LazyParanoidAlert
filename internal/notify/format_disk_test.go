package notify

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"siren/internal/aggregate"
	"siren/internal/event"
)

// diskAlarm builds a health-watch alarm notice with no source IP (disk
// alarms are machine facts, not network facts).
func diskAlarm(kind event.Kind, detail string) aggregate.Notice {
	return aggregate.Notice{First: event.Event{
		Kind:    kind,
		Time:    time.Date(2026, 10, 30, 9, 15, 0, 0, time.Local),
		Machine: "TEST-PC",
		Detail:  detail,
	}}
}

// TestFormatDiskHealthAlert: header with agent name and IP (via
// Formatter.AgentIP), the alarm label and the multi-line change body.
func TestFormatDiskHealthAlert(t *testing.T) {
	f := Formatter{AgentIP: "192.0.2.26"}
	got := f.Format(diskAlarm(event.KindDiskHealthAlert,
		"диск sdb: статус ok -> warn (reallocated 0 -> 8)\nмассив md0 (md): состояние clean -> degraded"))
	if !strings.HasPrefix(got, "🚨 TEST-PC (192.0.2.26): ТРЕВОГА ДИСКОВ\n") {
		t.Errorf("header wrong:\n%s", got)
	}
	if !strings.Contains(got, "время: 2026-10-30 09:15:00") {
		t.Errorf("time line missing:\n%s", got)
	}
	if !strings.Contains(got, "источник: IP неизвестен") {
		t.Errorf("source line missing:\n%s", got)
	}
	if !strings.Contains(got, "диск sdb: статус ok -> warn") {
		t.Errorf("change body missing:\n%s", got)
	}
	if !strings.Contains(got, "массив md0 (md): состояние clean -> degraded") {
		t.Errorf("second change missing:\n%s", got)
	}
}

func TestFormatDiskSpaceAlert(t *testing.T) {
	f := Formatter{AgentIP: "192.0.2.26"}
	got := f.Format(diskAlarm(event.KindDiskSpaceAlert,
		"место на исходе: D:, всего 3600 ГБ, свободно 288 ГБ (8.0%), порог 10% (минимум 0 ГБ)"))
	if !strings.Contains(got, "МЕСТО НА ИСХОДЕ") {
		t.Errorf("space label missing:\n%s", got)
	}
	if !strings.Contains(got, "свободно 288 ГБ (8.0%)") {
		t.Errorf("volume detail missing:\n%s", got)
	}
}

// TestFormatRegularEventsKeepShape: the Detail extension must not alter
// formatting of regular trap/auth events.
func TestFormatRegularEventsKeepShape(t *testing.T) {
	f := Formatter{AgentIP: "192.0.2.26"}
	got := f.Format(aggregate.Notice{First: event.Event{
		Kind:     event.KindAuthAttempt,
		Time:     time.Date(2026, 10, 30, 9, 15, 0, 0, time.Local),
		Machine:  "TEST-PC",
		Protocol: "rdp",
		SrcIP:    netip.MustParseAddr("203.0.113.5"),
		DstPort:  3389,
		Account:  "admin",
	}})
	if strings.Contains(got, "пароль") || strings.Contains(got, "\n\n") {
		t.Errorf("regular event format changed:\n%s", got)
	}
	if !strings.Contains(got, "ПОПЫТКА ВХОДА") {
		t.Errorf("auth label missing:\n%s", got)
	}
}
