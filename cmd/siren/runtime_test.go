package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"testing"
	"time"

	"siren/internal/enrich"
	"siren/internal/event"
	"siren/internal/logsetup"
)

func TestAllowlistMatch(t *testing.T) {
	a := newAllowlist([]string{"192.168.1.10", "10.0.0.0/24"}, logsetup.Discard())

	cases := []struct {
		ip   string
		port int
		want bool
	}{
		{"192.168.1.10", 22, true},  // exact entry, any port
		{"192.168.1.10", 0, true},   // no port: entry without ports still matches
		{"192.168.1.11", 22, false}, // same net, not listed
		{"10.0.0.77", 5900, true},   // inside CIDR
		{"10.0.1.77", 22, false},    // outside CIDR
		{"203.0.113.5", 22, false},  // foreign source
	}
	for _, tc := range cases {
		if got := a.match(netip.MustParseAddr(tc.ip), tc.port); got != tc.want {
			t.Errorf("match(%s, %d) = %v, want %v", tc.ip, tc.port, got, tc.want)
		}
	}
	// Unknown source must never be allowlisted: it has to stay visible.
	if a.match(netip.Addr{}, 22) {
		t.Error("zero IP must not match the allowlist")
	}
}

// TestAllowlistPortScope: a port-suffixed entry suppresses only events
// tied to the listed ports; an event without a port or on another port
// stays visible. CIDR entries scope the same way.
func TestAllowlistPortScope(t *testing.T) {
	a := newAllowlist([]string{"192.168.1.10:22", "10.0.0.0/24:22,3389", "[::1]:22"}, logsetup.Discard())

	cases := []struct {
		ip   string
		port int
		want bool
	}{
		{"192.168.1.10", 22, true},  // scoped port
		{"192.168.1.10", 23, false}, // other port stays visible
		{"192.168.1.10", 0, false},  // event without a port never matches a port-scoped entry
		{"10.0.0.77", 3389, true},   // CIDR entry, listed port
		{"10.0.0.77", 5900, false},  // CIDR entry, unlisted port
		{"10.0.1.77", 22, false},    // outside the CIDR
		{"::1", 22, true},           // bracketed IPv6 entry
		{"::1", 23, false},
	}
	for _, tc := range cases {
		ip := netip.MustParseAddr(tc.ip)
		if got := a.match(ip, tc.port); got != tc.want {
			t.Errorf("match(%s, %d) = %v, want %v", tc.ip, tc.port, got, tc.want)
		}
	}
}

// TestAllowlistBadEntryIgnored: a malformed entry (possible only when the
// config was edited on the fly) is logged and skipped, fail-open towards
// visibility.
func TestAllowlistBadEntryIgnored(t *testing.T) {
	a := newAllowlist([]string{"not-an-ip", "10.9.9.9"}, logsetup.Discard())
	if !a.match(netip.MustParseAddr("10.9.9.9"), 0) {
		t.Error("valid entry after a malformed one must still work")
	}
	if a.match(netip.MustParseAddr("10.0.0.1"), 0) {
		t.Error("malformed entry must not allowlist unrelated IPs")
	}
	// A port-suffixed entry with a bad port list is skipped entirely.
	bad := newAllowlist([]string{"10.9.9.9:0", "10.9.9.9:22,abc"}, logsetup.Discard())
	if bad.match(netip.MustParseAddr("10.9.9.9"), 22) {
		t.Error("entry with an invalid port list must not allowlist")
	}
}

func TestPumpUnmapsAndStampsMachine(t *testing.T) {
	en := enrich.New(nil, nil, nil, time.Minute, logsetup.Discard())
	allow := newAllowlist(nil, logsetup.Discard())
	in := make(chan event.Event, 4)
	out := make(chan event.Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pumpEnrich(ctx, allow, en, "TEST-PC", in, out, logsetup.Discard()) }()

	// A trap event from a dual-stack listener: v4-mapped IPv6, no machine.
	in <- event.Event{
		Kind:    event.KindTrapConnect,
		SrcIP:   netip.AddrFrom16(netip.AddrFrom4([4]byte{127, 0, 0, 1}).As16()), // ::ffff:127.0.0.1
		DstPort: 2323,
	}

	select {
	case ev := <-out:
		if got := ev.SrcIP.String(); got != "127.0.0.1" {
			t.Errorf("src ip = %q, want unmapped 127.0.0.1", got)
		}
		if ev.Machine != "TEST-PC" {
			t.Errorf("machine = %q, want stamped agent name", ev.Machine)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pump did not forward the event")
	}

	// A door event already carries its machine name: it must survive.
	in <- event.Event{
		Kind:    event.KindAuthAttempt,
		SrcIP:   netip.MustParseAddr("127.0.0.1"),
		Machine: "WIN-PC01",
		Account: "root",
	}
	select {
	case ev := <-out:
		if ev.Machine != "WIN-PC01" {
			t.Errorf("machine = %q, want the door-provided name", ev.Machine)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pump did not forward the door event")
	}

	cancel()
	if err := <-done; err == nil {
		t.Error("pump must return the ctx error on shutdown")
	}
}

// TestPumpPortScopedDoorEvents: end-to-end semantics of a service-scoped
// allowlist entry — a door event stamped with the service port (SSH → 22)
// is suppressed for a "host:22" entry, while the same host's RDP fact
// (3389) passes.
func TestPumpPortScopedDoorEvents(t *testing.T) {
	en := enrich.New(nil, nil, nil, time.Minute, logsetup.Discard())
	allow := newAllowlist([]string{"192.168.1.10:22"}, logsetup.Discard())
	in := make(chan event.Event, 4)
	out := make(chan event.Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pumpEnrich(ctx, allow, en, "TEST-PC", in, out, logsetup.Discard()) }()

	// SSH fact from the allowed host (doors stamp 22): suppressed.
	in <- event.Event{Kind: event.KindAuthLogin, SrcIP: netip.MustParseAddr("192.168.1.10"),
		Protocol: "ssh", Machine: "SRV-02", DstPort: 22}
	// RDP fact from the same host (3389): must stay visible.
	in <- event.Event{Kind: event.KindAuthLogin, SrcIP: netip.MustParseAddr("192.168.1.10"),
		Protocol: "rdp", Machine: "SRV-02", DstPort: 3389}

	select {
	case ev := <-out:
		if ev.Protocol != "rdp" || ev.DstPort != 3389 {
			t.Errorf("passed event = %s/%d, want rdp/3389", ev.Protocol, ev.DstPort)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RDP fact was dropped despite being scoped to 3389")
	}
	select {
	case ev := <-out:
		t.Errorf("SSH fact leaked the :22 scope: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}

	cancel()
	if err := <-done; err == nil {
		t.Error("pump must return the ctx error on shutdown")
	}
}

func TestPumpDropsAllowlisted(t *testing.T) {
	en := enrich.New(nil, nil, nil, time.Minute, logsetup.Discard())
	allow := newAllowlist([]string{"127.0.0.1:22"}, logsetup.Discard())
	in := make(chan event.Event, 4)
	out := make(chan event.Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pumpEnrich(ctx, allow, en, "TEST-PC", in, out, slog.Default()) }()

	// Port-scoped entry: the trap connect on port 22 is dropped, the one
	// on port 23 and the door event (no port) both pass.
	in <- event.Event{Kind: event.KindTrapConnect, SrcIP: netip.MustParseAddr("127.0.0.1"), DstPort: 22}
	in <- event.Event{Kind: event.KindTrapConnect, SrcIP: netip.MustParseAddr("127.0.0.1"), DstPort: 23}
	in <- event.Event{Kind: event.KindTrapConnect, SrcIP: netip.MustParseAddr("203.0.113.7"), DstPort: 22}

	// Two non-allowlisted events pass: port 23 and the foreign host.
	var gotIPs []string
	for i := 0; i < 2; i++ {
		select {
		case ev := <-out:
			gotIPs = append(gotIPs, ev.SrcIP.String()+" port "+fmt.Sprint(ev.DstPort))
		case <-time.After(2 * time.Second):
			t.Fatalf("expected event %d was dropped (got %v)", i+1, gotIPs)
		}
	}
	sort.Strings(gotIPs)
	if gotIPs[0] != "127.0.0.1 port 23" || gotIPs[1] != "203.0.113.7 port 22" {
		t.Errorf("passed events = %v, want port-23 local and foreign port-22", gotIPs)
	}
	// The scoped event must not leak.
	select {
	case ev := <-out:
		t.Errorf("allowlisted event leaked: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}

	// Door event (DstPort 0) from the scoped host: not suppressed.
	in <- event.Event{Kind: event.KindAuthAttempt, SrcIP: netip.MustParseAddr("127.0.0.1"), Machine: "TEST-PC"}
	select {
	case ev := <-out:
		if ev.DstPort != 0 {
			t.Errorf("door event dst port = %d, want 0", ev.DstPort)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("door event without a port must pass a port-scoped entry")
	}
}
