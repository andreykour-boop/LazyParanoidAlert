// Package doors collects authentication facts on ports occupied by real
// services: failed and successful logins for RDP on Windows (Security events
// 4624/4625, RemoteConnectionManager event 1149) and SSH on Linux (journald
// records of the ssh unit). Every fact carries the source IP, account and
// time when available.
package doors

import (
	"encoding/xml"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"siren/internal/event"
)

// Windows event IDs watched by the agent.
const (
	winEventFailedLogon = 4625
	winEventLogon       = 4624
	winEventRDPConnect  = 1149
)

// Logon type 10 marks interactive remote sessions (RDP); every other type is
// local or service background noise and is ignored by the siren.
const winLogonTypeRemote = "10"

// ParseSSHMessage extracts an authentication fact from one sshd log message
// (the MESSAGE field of a journald record). matched reports whether the
// message describes a failed or successful login; unrecognized messages
// (key exchanges, disconnects, server banners) return matched=false.
func ParseSSHMessage(msg string) (kind event.Kind, account, ip string, port int, matched bool) {
	rest, ok := strings.CutPrefix(msg, "Failed password for ")
	if ok {
		account = rest
		// "Failed password for invalid user X" repeats the marker.
		if cut, ok := strings.CutPrefix(account, "invalid user "); ok {
			account = cut
		}
		kind, matched = event.KindAuthAttempt, true
	} else if rest, ok := strings.CutPrefix(msg, "Accepted "); ok {
		// "Accepted password|publickey for X from ..."
		sp := strings.SplitN(rest, " for ", 2)
		if len(sp) != 2 {
			return "", "", "", 0, false
		}
		account = sp[1]
		kind, matched = event.KindAuthLogin, true
	} else {
		return "", "", "", 0, false
	}

	from := " from "
	fi := strings.Index(account, from)
	if fi < 0 {
		return "", "", "", 0, false
	}
	ipPart := account[fi+len(from):]
	account = account[:fi]

	// ipPart: "1.2.3.4 port 5555 ssh2[...]".
	fields := strings.Fields(ipPart)
	if len(fields) < 3 || fields[1] != "port" {
		return "", "", "", 0, false
	}
	port, err := strconv.Atoi(fields[2])
	if err != nil {
		return "", "", "", 0, false
	}
	return kind, account, fields[0], port, true
}

// winEvent is the System section of a rendered Windows event XML.
type winEvent struct {
	XMLName xml.Name `xml:"Event"`
	System  struct {
		EventID     int `xml:"EventID"`
		TimeCreated struct {
			SystemTime time.Time `xml:"SystemTime,attr"`
		} `xml:"TimeCreated"`
	} `xml:"System"`
	EventData struct {
		Data []struct {
			Name    string `xml:"Name,attr"`
			Content string `xml:",chardata"`
		} `xml:"Data"`
	} `xml:"EventData"`
	// UserData: RCM event 1149 (RemoteConnectionManager) carries its
	// payload as EventXML/Param1..3, not as named EventData entries:
	// Param1 = User, Param2 = Domain, Param3 = client Address.
	UserData struct {
		EventXML struct {
			Param1 string `xml:"Param1"`
			Param2 string `xml:"Param2"`
			Param3 string `xml:"Param3"`
		} `xml:"EventXML"`
	} `xml:"UserData"`
}

// parseWindowsEventXML renders one Windows event XML document into its ID,
// creation time and named EventData fields. SystemTime arrives in UTC
// (trailing Z); it is converted to the machine's local time here, so every
// downstream consumer (alerts, digests) shows the owner's wall clock.
func parseWindowsEventXML(raw []byte) (id int, at time.Time, data map[string]string, err error) {
	var ev winEvent
	if err := xml.Unmarshal(raw, &ev); err != nil {
		return 0, time.Time{}, nil, fmt.Errorf("doors: parse event XML: %w", err)
	}
	data = make(map[string]string, len(ev.EventData.Data))
	for _, d := range ev.EventData.Data {
		data[d.Name] = d.Content
	}
	// RCM 1149: map UserData/EventXML params onto the field names the
	// converter expects, so User/Address work the same as Security events.
	if len(ev.EventData.Data) == 0 {
		x := ev.UserData.EventXML
		if x.Param1 != "" || x.Param2 != "" || x.Param3 != "" {
			data["User"] = x.Param1
			data["Domain"] = x.Param2
			data["Address"] = x.Param3
		}
	}
	return ev.System.EventID, ev.System.TimeCreated.SystemTime.Local(), data, nil
}

// ParseEventXML is the exported form of parseWindowsEventXML for callers
// outside the package that render their own event XML (the boot sensor's
// historical System-channel query).
func ParseEventXML(raw string) (id int, at time.Time, data map[string]string, err error) {
	return parseWindowsEventXML([]byte(raw))
}

// windowsEventToEvent converts a rendered Windows event into an agent event.
// Security 4624/4625 are filtered by Logon Type 10 (remote interactive);
// 1149 carries the RDP client address. ok reports whether the event belongs
// to the siren's watch list; non-matching events are silently skipped.
func windowsEventToEvent(id int, at time.Time, data map[string]string, machine string) (ev event.Event, ok bool) {
	ev = event.Event{Time: at, Machine: machine, Protocol: "rdp", DstPort: servicePort("rdp")}
	switch id {
	case winEventFailedLogon:
		if data["LogonType"] != winLogonTypeRemote {
			return event.Event{}, false
		}
		ev.Kind = event.KindAuthAttempt
		ev.Account = data["TargetUserName"]
		ev.SrcIP = parseIPorUnknown(data["IpAddress"])
		return ev, true
	case winEventLogon:
		if data["LogonType"] != winLogonTypeRemote {
			return event.Event{}, false
		}
		ev.Kind = event.KindAuthLogin
		ev.Account = data["TargetUserName"]
		ev.SrcIP = parseIPorUnknown(data["IpAddress"])
		return ev, true
	case winEventRDPConnect:
		ev.Kind = event.KindAuthLogin
		ev.Account = data["User"]
		ev.SrcIP = parseIPorUnknown(data["Address"])
		return ev, true
	}
	return event.Event{}, false
}

// parseIPorUnknown maps Windows IP fields, which use "-" for unknown or
// non-network sources, to the zero netip.Addr.
func parseIPorUnknown(s string) netip.Addr {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return netip.Addr{}
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return addr
}

// servicePort maps a door protocol to its well-known service port, so the
// allowlist can scope entries to a service ("host:22" also covers real SSH
// logins, whose events carry no trap port). Non-standard service ports are
// out of scope; a host with a relocated SSH service uses an entry without
// ports.
func servicePort(protocol string) int {
	switch protocol {
	case "ssh":
		return 22
	case "rdp":
		return 3389
	}
	return 0
}

// AuthEvent assembles the final agent event from one authentication fact.
// Unknown IP stays the zero value; the alert layer marks such events instead
// of dropping them.
func AuthEvent(kind event.Kind, protocol, machine, account, ip string, port int, at time.Time) event.Event {
	ev := event.Event{
		Kind:     kind,
		Time:     at,
		Machine:  machine,
		Protocol: protocol,
		Account:  account,
		SrcPort:  port,
		DstPort:  servicePort(protocol),
	}
	if addr, err := netip.ParseAddr(ip); err == nil {
		ev.SrcIP = addr
	}
	return ev
}
