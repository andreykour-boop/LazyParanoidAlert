package doors

import (
	"net/netip"
	"testing"
	"time"

	"siren/internal/event"
)

func TestParseSSHFailedPassword(t *testing.T) {
	cases := []struct {
		name, msg   string
		wantKind    event.Kind
		wantAccount string
		wantIP      string
	}{
		{
			name:     "invalid user marker",
			msg:      "Failed password for invalid user admin from 203.0.113.7 port 54321 ssh2",
			wantKind: event.KindAuthAttempt, wantAccount: "admin", wantIP: "203.0.113.7",
		},
		{
			name:     "existing user",
			msg:      "Failed password for root from 192.168.1.55 port 40000 ssh2",
			wantKind: event.KindAuthAttempt, wantAccount: "root", wantIP: "192.168.1.55",
		},
		{
			name:     "accepted password",
			msg:      "Accepted password for andrey from 192.0.2.10 port 51022 ssh2",
			wantKind: event.KindAuthLogin, wantAccount: "andrey", wantIP: "192.0.2.10",
		},
		{
			name:     "accepted publickey with suffix",
			msg:      "Accepted publickey for andrey from 192.0.2.10 port 51023 ssh2: RSA SHA256:abcdef",
			wantKind: event.KindAuthLogin, wantAccount: "andrey", wantIP: "192.0.2.10",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, account, ip, port, matched := ParseSSHMessage(tc.msg)
			if !matched {
				t.Fatal("message must match")
			}
			if kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
			if account != tc.wantAccount {
				t.Errorf("account = %q, want %q", account, tc.wantAccount)
			}
			if ip != tc.wantIP {
				t.Errorf("ip = %q, want %q", ip, tc.wantIP)
			}
			if port <= 0 || port > 65535 {
				t.Errorf("port = %d, want valid", port)
			}
		})
	}
}

func TestParseSSHUnmatched(t *testing.T) {
	msgs := []string{
		"Disconnected from user andrey 192.0.2.10 port 51022",
		"Received disconnect from 192.0.2.10 port 51022:11:",
		"Invalid user admin from 203.0.113.7 port 54321", // pre-auth, no password yet
		"Connection closed by authenticating user root 1.2.3.4 port 2222 [preauth]",
		"Server listening on 0.0.0.0 port 22.",
		"",
	}
	for _, msg := range msgs {
		if _, _, _, _, matched := ParseSSHMessage(msg); matched {
			t.Errorf("message %q must not match", msg)
		}
	}
}

func TestParseSSHMalformedSuffix(t *testing.T) {
	// Matched prefix but garbage tail: must not match, not panic.
	if _, _, _, _, matched := ParseSSHMessage("Failed password for admin from"); matched {
		t.Error("truncated message must not match")
	}
	if _, _, _, _, matched := ParseSSHMessage("Failed password for admin from 1.2.3.4 ssh2"); matched {
		t.Error("missing port must not match")
	}
}

const sample4625 = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'>
<System><Provider Name='Microsoft-Windows-Security-Auditing'/>
<EventID>4625</EventID><TimeCreated SystemTime='2026-10-28T12:30:00.500Z'/></System>
<EventData><Data Name='TargetUserName'>administrator</Data>
<Data Name='LogonType'>10</Data><Data Name='IpAddress'>192.168.1.55</Data>
<Data Name='IpPort'>51234</Data></EventData></Event>`

const sample4625NoIP = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'>
<System><EventID>4625</EventID><TimeCreated SystemTime='2026-10-28T12:31:00Z'/></System>
<EventData><Data Name='TargetUserName'>ivanov</Data>
<Data Name='LogonType'>10</Data><Data Name='IpAddress'>-</Data></EventData></Event>`

const sample4624Local = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'>
<System><EventID>4624</EventID><TimeCreated SystemTime='2026-10-28T12:32:00Z'/></System>
<EventData><Data Name='TargetUserName'>andrey</Data>
<Data Name='LogonType'>2</Data><Data Name='IpAddress'>-</Data></EventData></Event>`

const sample4624Remote = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'>
<System><EventID>4624</EventID><TimeCreated SystemTime='2026-10-28T12:33:00Z'/></System>
<EventData><Data Name='TargetUserName'>andrey</Data>
<Data Name='LogonType'>10</Data><Data Name='IpAddress'>192.0.2.10</Data></EventData></Event>`

// sample1149 mirrors the REAL 1149 layout (RemoteConnectionManager):
// the payload lives in UserData/EventXML as Param1..3, not in named
// EventData entries — Param1 = User, Param2 = Domain, Param3 = Address.
const sample1149 = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'>
<System><EventID>1149</EventID><TimeCreated SystemTime='2026-10-28T12:34:00Z'/></System>
<UserData><EventXML xmlns='Event_NS'><Param1>andrey</Param1>
<Param2>TEST-PC</Param2><Param3>192.0.2.10</Param3></EventXML></UserData></Event>`

func TestWindowsEventToEvent(t *testing.T) {
	t.Run("4625 attempt with IP", func(t *testing.T) {
		id, at, data, err := parseWindowsEventXML([]byte(sample4625))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		ev, ok := windowsEventToEvent(id, at, data, "WIN-PC01")
		if !ok {
			t.Fatal("event must be accepted")
		}
		if ev.Kind != event.KindAuthAttempt {
			t.Errorf("kind = %q", ev.Kind)
		}
		if ev.Account != "administrator" {
			t.Errorf("account = %q", ev.Account)
		}
		if ev.SrcIP.String() != "192.168.1.55" {
			t.Errorf("ip = %q", ev.SrcIP)
		}
		if ev.Protocol != "rdp" {
			t.Errorf("protocol = %q", ev.Protocol)
		}
		if ev.Time.IsZero() {
			t.Error("time must come from SystemTime")
		}
	})

	t.Run("4625 without IP is kept and marked", func(t *testing.T) {
		id, at, data, err := parseWindowsEventXML([]byte(sample4625NoIP))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		ev, ok := windowsEventToEvent(id, at, data, "WIN-PC01")
		if !ok {
			t.Fatal("event without IP must still be accepted")
		}
		if ev.SrcIP.IsValid() {
			t.Errorf("unknown IP must be zero value, got %q", ev.SrcIP)
		}
		if ev.Account != "ivanov" {
			t.Errorf("account = %q", ev.Account)
		}
	})

	t.Run("4624 local logon is filtered", func(t *testing.T) {
		id, at, data, _ := parseWindowsEventXML([]byte(sample4624Local))
		if _, ok := windowsEventToEvent(id, at, data, "WIN-PC01"); ok {
			t.Error("logon type 2 must be filtered out")
		}
	})

	t.Run("4624 remote logon accepted", func(t *testing.T) {
		id, at, data, _ := parseWindowsEventXML([]byte(sample4624Remote))
		ev, ok := windowsEventToEvent(id, at, data, "WIN-PC01")
		if !ok {
			t.Fatal("logon type 10 must be accepted")
		}
		if ev.Kind != event.KindAuthLogin {
			t.Errorf("kind = %q", ev.Kind)
		}
	})

	t.Run("1149 RDP connect", func(t *testing.T) {
		id, at, data, _ := parseWindowsEventXML([]byte(sample1149))
		ev, ok := windowsEventToEvent(id, at, data, "WIN-PC01")
		if !ok {
			t.Fatal("1149 must be accepted")
		}
		if ev.Kind != event.KindAuthLogin {
			t.Errorf("kind = %q", ev.Kind)
		}
		if ev.Account != "andrey" {
			t.Errorf("account = %q", ev.Account)
		}
		if ev.SrcIP != netip.MustParseAddr("192.0.2.10") {
			t.Errorf("ip = %q, want from Param3", ev.SrcIP)
		}
	})

	t.Run("unknown event id skipped", func(t *testing.T) {
		if _, ok := windowsEventToEvent(9999, time.Now(), map[string]string{}, "M"); ok {
			t.Error("unknown id must be skipped")
		}
	})
}

func TestParseWindowsEventXMLInvalid(t *testing.T) {
	if _, _, _, err := parseWindowsEventXML([]byte("<not-an-event>")); err == nil {
		t.Fatal("expected parse error for foreign XML")
	}
}

// TestParseWindowsEventXMLLocalTime: SystemTime comes in UTC ("Z"); the
// parsed time must carry the same instant expressed in the machine's local
// zone, so alerts show the owner's wall clock, not UTC.
func TestParseWindowsEventXMLLocalTime(t *testing.T) {
	xmlDoc := []byte(`<Event><System><EventID>4624</EventID>` +
		`<TimeCreated SystemTime='2026-10-30T13:03:52.500Z'/></System></Event>`)

	_, at, _, err := parseWindowsEventXML(xmlDoc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	utc, err := time.Parse(time.RFC3339, "2026-10-30T13:03:52.5Z")
	if err != nil {
		t.Fatalf("parse reference: %v", err)
	}
	if !at.Equal(utc) {
		t.Fatalf("instant changed: %v vs %v", at, utc)
	}
	if at.Location() != time.Local {
		t.Errorf("time location = %v, want local", at.Location())
	}
	// In a UTC+3 zone the wall clock must read 16:03, not 13:03.
	if _, offset := at.Zone(); offset == 3*3600 && at.Hour() != 16 {
		t.Errorf("wall clock = %02d:%02d, want 16:03 in UTC+3", at.Hour(), at.Minute())
	}
}

func TestAuthEventIPHandling(t *testing.T) {
	at := time.Now()
	ev := AuthEvent(event.KindAuthAttempt, "ssh", "SRV-02", "admin", "203.0.113.9", 55555, at)
	if ev.SrcIP.String() != "203.0.113.9" {
		t.Errorf("ip = %q", ev.SrcIP)
	}
	if ev.SrcPort != 55555 {
		t.Errorf("port = %d", ev.SrcPort)
	}
	// Unknown IP must stay the zero value, not an error.
	ev = AuthEvent(event.KindAuthAttempt, "ssh", "SRV-02", "admin", "not-an-ip", 1, at)
	if ev.SrcIP.IsValid() {
		t.Errorf("bad IP must yield zero value, got %q", ev.SrcIP)
	}
	// Service port stamping: the allowlist scopes entries to a service,
	// so door events must carry the well-known port even with a bad IP.
	if ev.DstPort != 22 {
		t.Errorf("ssh dst port = %d, want 22", ev.DstPort)
	}
	ev = AuthEvent(event.KindAuthLogin, "rdp", "WIN-PC01", "andrey", "203.0.113.9", 49152, at)
	if ev.DstPort != 3389 {
		t.Errorf("rdp dst port = %d, want 3389", ev.DstPort)
	}
	ev = AuthEvent(event.KindAuthLogin, "unknown-proto", "X", "u", "203.0.113.9", 1, at)
	if ev.DstPort != 0 {
		t.Errorf("unknown protocol dst port = %d, want 0", ev.DstPort)
	}
}

// TestWindowsEventsCarryServicePort: RDP door events (Security 4624/4625
// and RCM 1149) are stamped with 3389, so allowlist entries scoped to the
// service port match them.
func TestWindowsEventsCarryServicePort(t *testing.T) {
	cases := map[int]map[string]string{
		winEventFailedLogon: {"LogonType": winLogonTypeRemote, "TargetUserName": "admin", "IpAddress": "203.0.113.9"},
		winEventLogon:       {"LogonType": winLogonTypeRemote, "TargetUserName": "andrey", "IpAddress": "203.0.113.9"},
	}
	for id, data := range cases {
		ev, ok := windowsEventToEvent(id, time.Now(), data, "WIN-PC01")
		if !ok {
			t.Errorf("event %d: want ok", id)
			continue
		}
		if ev.DstPort != 3389 {
			t.Errorf("event %d dst port = %d, want 3389", id, ev.DstPort)
		}
		if !ev.SrcIP.IsValid() {
			t.Errorf("event %d: ip lost", id)
		}
	}
}
