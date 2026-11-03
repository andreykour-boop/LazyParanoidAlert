//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
)

// bootEventFixture builds a rendered System-channel event XML like the one
// EvtQuery hands over. params are Name/Content pairs (param1..paramN).
func bootEventFixture(id int, systemTime string, params ...[2]string) string {
	var b strings.Builder
	b.WriteString(`<Event xmlns="http://schemas.microsoft.com/win/2004/08/events/event">`)
	b.WriteString(`<System><Provider Name="User32"/><EventID>`)
	b.WriteString(itoa(id))
	b.WriteString(`</EventID><TimeCreated SystemTime="`)
	b.WriteString(systemTime)
	b.WriteString(`"/><Channel>System</Channel><Computer>DESKTOP-EXAMPLE</Computer></System>`)
	if len(params) > 0 {
		b.WriteString(`<EventData>`)
		for _, p := range params {
			b.WriteString(`<Data Name="` + p[0] + `">` + p[1] + `</Data>`)
		}
		b.WriteString(`</EventData>`)
	}
	b.WriteString(`</Event>`)
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

var (
	t100 = time.Date(2026, 11, 3, 5, 40, 0, 0, time.UTC)
	t101 = t100.Add(2 * time.Second)
	t103 = t100.Add(3 * time.Second)
)

// Russian 1074: process+computer, computer, action, user, reason.
const (
	fx1074RestartRU = `C:\Windows\system32\svchost.exe (DESKTOP-EXAMPLE)`
	fxUserRU        = `desktop\andrey`
	fxReasonRU      = `Причина: Код причины: 0x8000003b`
)

func TestBootPrevSessionEvents(t *testing.T) {
	bootTime := t103.Add(5 * time.Minute) // current boot: after the fixtures
	xmls := []string{
		bootEventFixture(1074, t100.Format(time.RFC3339Nano), [2]string{"param1", fx1074RestartRU}),
		bootEventFixture(6006, t103.Format(time.RFC3339Nano)),
		bootEventFixture(1074, t103.Add(time.Hour).Format(time.RFC3339Nano), [2]string{"param3", "Перезагрузка"}), // current session: dropped
		"<broken",
	}
	events := bootPrevSessionEvents(xmls, bootTime)
	if len(events) != 2 {
		t.Fatalf("kept %d events, want 2 (previous session only): %+v", len(events), events)
	}
	if events[0].ID != 1074 || events[1].ID != 6006 {
		t.Errorf("wrong events kept: %+v", events)
	}
	if !events[0].At.Equal(t100) {
		t.Errorf("event time lost: %v vs %v", events[0].At, t100)
	}
	if events[0].Params["param1"] != fx1074RestartRU {
		t.Errorf("params lost: %+v", events[0].Params)
	}
}

func TestClassifyWindowsSessionCleanReboot(t *testing.T) {
	events := []bootWinEvent{
		{ID: 1074, At: t100, Params: map[string]string{
			"param1": fx1074RestartRU,
			"param2": "DESKTOP-EXAMPLE",
			"param3": "Перезагрузка компьютера",
			"param4": fxUserRU,
			"param5": fxReasonRU,
		}},
		{ID: 6006, At: t101},
		{ID: 41, At: t100.Add(-48 * time.Hour)}, // old crash, superseded
	}
	f := classifyWindowsSession(events, t103)
	if !f.Known || !f.Clean {
		t.Errorf("clean session misclassified: %+v", f)
	}
	if f.Action != "reboot" {
		t.Errorf("action = %q, want reboot", f.Action)
	}
	if f.LastAt != t101 {
		t.Errorf("LastAt = %v, want the 6006 time %v", f.LastAt, t101)
	}
	for _, want := range []string{fxUserRU, "svchost.exe", fxReasonRU} {
		if !strings.Contains(f.Initiator, want) {
			t.Errorf("initiator %q misses %q", f.Initiator, want)
		}
	}
}

func TestClassifyWindowsSessionDirty(t *testing.T) {
	events := []bootWinEvent{
		{ID: 6008, At: t100},                     // unexpected shutdown marker
		{ID: 41, At: t100.Add(-1 * time.Second)}, // Kernel-Power, older
	}
	f := classifyWindowsSession(events, t103)
	if !f.Known {
		t.Error("classification must be known even for a dirty session")
	}
	if f.Clean {
		t.Errorf("dirty session misclassified as clean: %+v", f)
	}
	if f.Action != "" {
		t.Errorf("dirty session must have no action, got %q", f.Action)
	}
	if f.LastAt != t100 {
		t.Errorf("LastAt = %v, want the freshest trace %v", f.LastAt, t100)
	}
	if f.Initiator != "" {
		t.Errorf("dirty session must have no initiator, got %q", f.Initiator)
	}
}

func TestClassifyWindowsSessionCleanShutdownNoInitiator(t *testing.T) {
	events := []bootWinEvent{{ID: 6006, At: t101}}
	f := classifyWindowsSession(events, t103)
	if !f.Known || !f.Clean {
		t.Errorf("6006-only session misclassified: %+v", f)
	}
	if f.Action != "shutdown" {
		t.Errorf("6006-only action = %q, want shutdown", f.Action)
	}
	if f.LastAt != t101 {
		t.Errorf("LastAt = %v, want %v", f.LastAt, t101)
	}
	if f.Initiator != "" {
		t.Errorf("absent initiator must stay empty, got %q", f.Initiator)
	}
}

func TestBootEventActionBilingual(t *testing.T) {
	cases := []struct {
		param3 string
		want   string
	}{
		{"Перезагрузка компьютера", "reboot"},
		{"Restart of computer", "reboot"},
		{"Завершение работы", "shutdown"},
		{"Power off", "shutdown"},
		{"Shut down (Planned)", "shutdown"},
		{"Operating System: Upgrade (Planned)", ""},
	}
	for _, c := range cases {
		if got := bootEventAction(map[string]string{"param3": c.param3}); got != c.want {
			t.Errorf("action(%q) = %q, want %q", c.param3, got, c.want)
		}
	}
}

func TestBootEventUserRejectsNonAccounts(t *testing.T) {
	params := map[string]string{
		"param1": `C:\Windows\system32\svchost.exe (PC)`, // path
		"param2": "PC",                                   // no backslash
		"param5": `Причина: код C:\temp\log.txt`,         // reason with a path
	}
	if got := bootEventUser(params); got != "" {
		t.Errorf("user extraction picked %q, want empty", got)
	}
	params["param4"] = `DESKTOP\andrey`
	if got := bootEventUser(params); got != `DESKTOP\andrey` {
		t.Errorf("user = %q, want DESKTOP\\andrey", got)
	}
}
