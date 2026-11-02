package notify

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"siren/internal/aggregate"
	"siren/internal/event"
)

func baseEvent(kind event.Kind) event.Event {
	return event.Event{
		Kind:     kind,
		Time:     time.Date(2026, 10, 28, 18, 20, 31, 0, time.UTC),
		Machine:  "WIN-PC01",
		Protocol: "rdp",
		SrcIP:    netip.MustParseAddr("192.168.1.55"),
		SrcPort:  51234,
		DstPort:  3389,
		Account:  "andrey",
		Hostname: "office-lt.corp",
		MAC:      "aa:bb:cc:dd:ee:ff",
	}
}

func TestFormatAuthAttempt(t *testing.T) {
	ev := baseEvent(event.KindAuthAttempt)
	ev.Protocol = "ssh"
	ev.DstPort = 22
	n := aggregate.Notice{First: ev, Last: ev, Count: 1}
	got := Format(n)
	for _, want := range []string{
		"WIN-PC01: ПОПЫТКА ВХОДА",
		"время: 2026-10-28 18:20:31",
		"источник: 192.168.1.55 · office-lt.corp · aa:bb:cc:dd:ee:ff",
		"порт: 22 (ssh)",
		"учётка: andrey",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("attempt message missing %q:\n%s", want, got)
		}
	}
}

func TestFormatAuthLogin(t *testing.T) {
	ev := baseEvent(event.KindAuthLogin)
	n := aggregate.Notice{First: ev, Last: ev, Count: 1}
	got := Format(n)
	for _, want := range []string{
		"WIN-PC01: ВХОД",
		"порт: 3389 (rdp)",
		"учётка: andrey",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("login message missing %q:\n%s", want, got)
		}
	}
}

func TestFormatTrapConnect(t *testing.T) {
	ev := baseEvent(event.KindTrapConnect)
	ev.Protocol = "telnet"
	ev.DstPort = 2323
	ev.Account = ""
	n := aggregate.Notice{First: ev, Last: ev, Count: 1}
	got := Format(n)
	if !strings.Contains(got, "WIN-PC01: подключение к ловушке") {
		t.Errorf("trap header wrong:\n%s", got)
	}
	if !strings.Contains(got, "порт: 2323 (telnet)") {
		t.Errorf("trap port line wrong:\n%s", got)
	}
	if strings.Contains(got, "учётка:") {
		t.Errorf("trap message must have no account line:\n%s", got)
	}
}

func TestFormatDigest(t *testing.T) {
	first := baseEvent(event.KindAuthAttempt)
	first.Protocol, first.DstPort = "ssh", 22
	last := first
	last.Time = time.Date(2026, 10, 28, 18, 25, 2, 0, time.UTC)
	n := aggregate.Notice{
		First: first, Last: last, Count: 50,
		Window: 5 * time.Minute, Digest: true,
	}
	got := Format(n)
	for _, want := range []string{
		"ПОПЫТКА ВХОДА (+49 за 5m0s)",
		"последний повтор: 2026-10-28 18:25:02",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("digest message missing %q:\n%s", want, got)
		}
	}
}

func TestFormatUnknownSourceMarked(t *testing.T) {
	ev := baseEvent(event.KindAuthLogin)
	ev.SrcIP = netip.Addr{}
	ev.Hostname = ""
	ev.MAC = ""
	n := aggregate.Notice{First: ev, Last: ev, Count: 1}
	got := Format(n)
	if !strings.Contains(got, "источник: IP неизвестен · имя не определено") {
		t.Errorf("unknown source must be marked explicitly:\n%s", got)
	}
}

func TestFormatUnknownHostnameMarked(t *testing.T) {
	// IP known but no reverse DNS: the IP stays visible, the name is
	// explicitly marked rather than omitted.
	ev := baseEvent(event.KindAuthAttempt)
	ev.Hostname = ""
	n := aggregate.Notice{First: ev, Last: ev, Count: 1}
	got := Format(n)
	if !strings.Contains(got, "источник: 192.168.1.55 · имя не определено") {
		t.Errorf("missing hostname must be marked:\n%s", got)
	}
}

func TestFormatUnknownKindFallback(t *testing.T) {
	ev := baseEvent(event.Kind("weird_kind"))
	n := aggregate.Notice{First: ev, Last: ev, Count: 1}
	got := Format(n)
	if !strings.Contains(got, "weird_kind") {
		t.Errorf("unknown kind must fall back to its raw name:\n%s", got)
	}
}

func TestFormatAgentIPInHeader(t *testing.T) {
	ev := baseEvent(event.KindTrapConnect)
	ev.Protocol, ev.DstPort = "telnet", 2323
	ev.Account = ""
	n := aggregate.Notice{First: ev, Last: ev, Count: 1}
	got := Formatter{AgentIP: "198.51.100.10"}.Format(n)
	if !strings.Contains(got, "🚨 WIN-PC01 (198.51.100.10): подключение к ловушке") {
		t.Errorf("header must carry the agent address:\n%s", got)
	}
}

func TestFormatPasswordLine(t *testing.T) {
	ev := baseEvent(event.KindAuthAttempt)
	ev.Protocol, ev.DstPort = "ssh", 22
	ev.Password = "hunter2"
	n := aggregate.Notice{First: ev, Last: ev, Count: 1}
	got := Format(n)
	if !strings.Contains(got, "пароль: hunter2") {
		t.Errorf("attempted password must be shown:\n%s", got)
	}
	// Real-service facts never carry credentials: no line, no placeholder.
	ev2 := baseEvent(event.KindAuthAttempt)
	ev2.Protocol, ev2.DstPort = "rdp", 3389
	got2 := Format(aggregate.Notice{First: ev2, Last: ev2, Count: 1})
	if strings.Contains(got2, "пароль:") {
		t.Errorf("real-service fact must have no password line:\n%s", got2)
	}
}
