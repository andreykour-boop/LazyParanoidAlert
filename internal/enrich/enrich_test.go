package enrich

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"

	"siren/internal/event"
)

// fakeResolver counts LookupAddr calls and returns scripted results.
type fakeResolver struct {
	mu    sync.Mutex
	calls int
	names []string
	err   error
}

func (f *fakeResolver) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.names, f.err
}

func (f *fakeResolver) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeARP returns a scripted MAC.
type fakeARP struct {
	mac string
	ok  bool
}

func (f fakeARP) MAC(netip.Addr) (string, bool) { return f.mac, f.ok }

func newTestEnricher(t *testing.T, nameMap map[string]string, res Resolver, arp ARPTable) *Enricher {
	t.Helper()
	e := New(nameMap, res, arp, time.Minute, slog.Default())
	return e
}

func TestEnrichManualMapWinsOverDNS(t *testing.T) {
	res := &fakeResolver{names: []string{"dns-box.lan."}}
	e := newTestEnricher(t, map[string]string{"192.168.1.10": "accounting-pc"}, res, fakeARP{})
	ev := &event.Event{SrcIP: netip.MustParseAddr("192.168.1.10")}
	e.Enrich(ev)
	if ev.Hostname != "accounting-pc" {
		t.Errorf("hostname = %q, want manual map name", ev.Hostname)
	}
	if res.callCount() != 0 {
		t.Errorf("manual map hit must not query DNS, got %d calls", res.callCount())
	}
}

func TestEnrichReverseDNSUsedWithoutManualEntry(t *testing.T) {
	res := &fakeResolver{names: []string{"printer.lan."}}
	e := newTestEnricher(t, map[string]string{"10.0.0.1": "other"}, res, fakeARP{})
	ev := &event.Event{SrcIP: netip.MustParseAddr("192.168.1.20")}
	e.Enrich(ev)
	if ev.Hostname != "printer.lan" {
		t.Errorf("hostname = %q, want PTR name without trailing dot", ev.Hostname)
	}
}

func TestEnrichUnknownNameMarkedByEmptyHostname(t *testing.T) {
	res := &fakeResolver{err: errors.New("no such host")}
	e := newTestEnricher(t, nil, res, fakeARP{})
	ev := &event.Event{SrcIP: netip.MustParseAddr("192.168.1.30")}
	e.Enrich(ev)
	if ev.Hostname != "" {
		t.Errorf("hostname = %q, want empty for undeterminable source", ev.Hostname)
	}
	// The IP must survive: the alert layer reports it with the "unknown" mark.
	if ev.SrcIP.String() != "192.168.1.30" {
		t.Errorf("ip = %q, want preserved", ev.SrcIP)
	}
}

func TestEnrichMACFromARP(t *testing.T) {
	res := &fakeResolver{err: errors.New("no such host")}
	e := newTestEnricher(t, nil, res, fakeARP{mac: "aa:bb:cc:dd:ee:ff", ok: true})
	ev := &event.Event{SrcIP: netip.MustParseAddr("192.168.1.30")}
	e.Enrich(ev)
	if ev.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("mac = %q, want from ARP", ev.MAC)
	}
}

func TestEnrichUnknownIPUntouched(t *testing.T) {
	res := &fakeResolver{}
	e := newTestEnricher(t, nil, res, fakeARP{})
	ev := &event.Event{Kind: event.KindAuthAttempt, Machine: "WIN-PC01"}
	e.Enrich(ev)
	if ev.Hostname != "" || ev.MAC != "" {
		t.Errorf("event without IP must stay untouched, got %q/%q", ev.Hostname, ev.MAC)
	}
	if res.callCount() != 0 {
		t.Error("no DNS query expected without IP")
	}
}

func TestEnrichHostnameCachedUntilTTL(t *testing.T) {
	res := &fakeResolver{names: []string{"nas.lan."}}
	e := newTestEnricher(t, nil, res, fakeARP{})
	ip := netip.MustParseAddr("192.168.1.40")

	ev := &event.Event{SrcIP: ip}
	e.Enrich(ev)
	e.Enrich(ev)
	e.Enrich(ev)
	if got := res.callCount(); got != 1 {
		t.Fatalf("resolver calls = %d, want 1 within TTL", got)
	}

	// Expire the cache entry: the next event queries DNS again.
	now := time.Now()
	e.hosts.now = func() time.Time { return now.Add(2 * time.Minute) }
	e.Enrich(ev)
	if got := res.callCount(); got != 2 {
		t.Fatalf("resolver calls after TTL = %d, want 2", got)
	}
}

func TestEnrichNegativeResultCached(t *testing.T) {
	res := &fakeResolver{err: errors.New("no such host")}
	e := newTestEnricher(t, nil, res, fakeARP{})
	ip := netip.MustParseAddr("192.168.1.50")
	for i := 0; i < 5; i++ {
		e.Enrich(&event.Event{SrcIP: ip})
	}
	if got := res.callCount(); got != 1 {
		t.Errorf("resolver calls = %d, want 1: failures must be cached too", got)
	}
}

func TestTTLCacheExpiry(t *testing.T) {
	c := newTTLCache[string](time.Minute)
	base := time.Now()
	c.now = func() time.Time { return base }
	c.put("k", "v")
	if _, ok := c.get("k"); !ok {
		t.Fatal("fresh entry must be found")
	}
	c.now = func() time.Time { return base.Add(time.Minute + time.Second) }
	if _, ok := c.get("k"); ok {
		t.Fatal("expired entry must not be found")
	}
}

func TestTTLCacheIndependentKeys(t *testing.T) {
	c := newTTLCache[int](time.Minute)
	c.put("a", 1)
	c.put("b", 2)
	if v, ok := c.get("a"); !ok || v != 1 {
		t.Errorf("a = %d, %v", v, ok)
	}
	if v, ok := c.get("b"); !ok || v != 2 {
		t.Errorf("b = %d, %v", v, ok)
	}
	if _, ok := c.get("c"); ok {
		t.Error("missing key must not be found")
	}
}
