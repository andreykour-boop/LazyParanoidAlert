package enrich

import (
	"errors"
	"log/slog"
	"net/netip"
	"testing"
	"time"
)

// newFakeARPTable builds an arpTable with a scripted read function.
func newFakeARPTable(read func() (map[netip.Addr]string, error)) *arpTable {
	return &arpTable{
		ttl:  arpRefreshTTL,
		now:  time.Now,
		log:  slog.Default(),
		read: read,
	}
}

func TestARPTableCachesWithinTTL(t *testing.T) {
	calls := 0
	a := newFakeARPTable(func() (map[netip.Addr]string, error) {
		calls++
		return map[netip.Addr]string{netip.MustParseAddr("10.0.0.2"): "aa:bb:cc:dd:ee:02"}, nil
	})

	ip := netip.MustParseAddr("10.0.0.2")
	for i := 0; i < 5; i++ {
		mac, ok := a.MAC(ip)
		if !ok || mac != "aa:bb:cc:dd:ee:02" {
			t.Fatalf("MAC = %q, %v", mac, ok)
		}
	}
	if calls != 1 {
		t.Errorf("read calls = %d, want 1 within TTL", calls)
	}
}

func TestARPTableRefreshesAfterTTL(t *testing.T) {
	calls := 0
	base := time.Now()
	a := newFakeARPTable(func() (map[netip.Addr]string, error) {
		calls++
		if calls == 1 {
			return map[netip.Addr]string{netip.MustParseAddr("10.0.0.2"): "aa:bb:cc:dd:ee:02"}, nil
		}
		// The neighbor changed: new MAC after the TTL.
		return map[netip.Addr]string{netip.MustParseAddr("10.0.0.2"): "aa:bb:cc:dd:ee:99"}, nil
	})
	a.now = func() time.Time { return base }

	ip := netip.MustParseAddr("10.0.0.2")
	if mac, _ := a.MAC(ip); mac != "aa:bb:cc:dd:ee:02" {
		t.Fatalf("first MAC = %q", mac)
	}
	a.now = func() time.Time { return base.Add(arpRefreshTTL + time.Second) }
	if mac, _ := a.MAC(ip); mac != "aa:bb:cc:dd:ee:99" {
		t.Fatalf("MAC after TTL = %q, want refreshed", mac)
	}
	if calls != 2 {
		t.Errorf("read calls = %d, want 2", calls)
	}
}

func TestARPTableReadErrorKeepsSnapshot(t *testing.T) {
	calls := 0
	base := time.Now()
	a := newFakeARPTable(func() (map[netip.Addr]string, error) {
		calls++
		if calls == 1 {
			return map[netip.Addr]string{netip.MustParseAddr("10.0.0.3"): "aa:bb:cc:dd:ee:03"}, nil
		}
		return nil, errors.New("boom")
	})
	a.now = func() time.Time { return base }

	ip := netip.MustParseAddr("10.0.0.3")
	if mac, ok := a.MAC(ip); !ok || mac != "aa:bb:cc:dd:ee:03" {
		t.Fatalf("MAC = %q, %v", mac, ok)
	}
	// Stale + failing read: the previous snapshot stays resolvable.
	a.now = func() time.Time { return base.Add(arpRefreshTTL + time.Second) }
	if mac, ok := a.MAC(ip); !ok || mac != "aa:bb:cc:dd:ee:03" {
		t.Errorf("MAC after failed refresh = %q, %v; want old snapshot", mac, ok)
	}
}

func TestARPTableIPv6NotSupported(t *testing.T) {
	a := newFakeARPTable(func() (map[netip.Addr]string, error) {
		return map[netip.Addr]string{}, nil
	})
	if _, ok := a.MAC(netip.MustParseAddr("fe80::1")); ok {
		t.Error("IPv6 must not resolve: both sources are IPv4-only")
	}
	if _, ok := a.MAC(netip.Addr{}); ok {
		t.Error("zero IP must not resolve")
	}
}

// TestARPTableLive resolves a real neighbor of this host through the cache;
// skipped on hosts where the neighbor table is empty.
func TestARPTableLive(t *testing.T) {
	a := NewARPTable(slog.Default()).(*arpTable)
	a.refresh()
	a.mu.Lock()
	table := a.table
	a.mu.Unlock()
	if len(table) == 0 {
		t.Skip("host neighbor table is empty")
	}
	var ip netip.Addr
	var wantMAC string
	for candidate, mac := range table {
		ip, wantMAC = candidate, mac
		break
	}
	t.Logf("live entry: %v -> %s", ip, wantMAC)
	mac, ok := a.MAC(ip)
	if !ok || mac != wantMAC {
		t.Errorf("MAC(%v) = %q, %v; want %q", ip, mac, ok, wantMAC)
	}
	if len(mac) != 17 { // xx:xx:xx:xx:xx:xx
		t.Errorf("MAC %q is not normalized to colon-separated form", mac)
	}
}
