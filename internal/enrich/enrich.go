package enrich

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"siren/internal/event"
)

// dnsTimeout bounds one reverse DNS query.
const dnsTimeout = 2 * time.Second

// Resolver resolves IP addresses to names. *net.Resolver satisfies it; tests
// replace it with a stub.
type Resolver interface {
	LookupAddr(ctx context.Context, addr string) (names []string, err error)
}

// ARPTable resolves an IPv4 address to the neighbor's MAC address.
type ARPTable interface {
	MAC(ip netip.Addr) (mac string, ok bool)
}

// Enricher augments events with source identity data. The manual name map
// from the config wins over reverse DNS; both lookup results are cached with
// a TTL, negative results included, so a flood of events from one source
// causes at most one DNS query per TTL.
type Enricher struct {
	nameMap  map[string]string
	resolver Resolver
	arp      ARPTable
	log      *slog.Logger
	hosts    ttlCache[string]
}

// New builds an Enricher. A nil resolver falls back to net.DefaultResolver,
// a nil ARPTable disables MAC enrichment. ttl applies to both the hostname
// and MAC caches.
func New(nameMap map[string]string, resolver Resolver, arp ARPTable, ttl time.Duration, log *slog.Logger) *Enricher {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if log == nil {
		log = slog.Default()
	}
	return &Enricher{
		nameMap:  nameMap,
		resolver: resolver,
		arp:      arp,
		log:      log,
		hosts:    newTTLCache[string](ttl),
	}
}

// Enrich fills ev.Hostname and ev.MAC in place when the source IP is known.
// An empty Hostname afterwards means "name not determined": the alert layer
// marks such events instead of inventing a placeholder.
func (e *Enricher) Enrich(ev *event.Event) {
	if !ev.SrcIP.IsValid() {
		return
	}
	name, _ := e.hostname(ev.SrcIP)
	ev.Hostname = name
	if ev.MAC == "" && e.arp != nil {
		if mac, ok := e.arp.MAC(ev.SrcIP); ok {
			ev.MAC = mac
		}
	}
}

// hostname resolves one IP: manual map first, then reverse DNS through the
// cache. ok reports whether any name is known.
func (e *Enricher) hostname(ip netip.Addr) (name string, ok bool) {
	key := ip.String()
	if name, ok := e.nameMap[key]; ok && name != "" {
		return name, true
	}
	if name, ok := e.hosts.get(key); ok {
		return name, name != ""
	}
	name = e.lookupAddr(key)
	e.hosts.put(key, name) // negative results are cached too
	return name, name != ""
}

// lookupAddr performs one reverse DNS query; any failure yields "".
func (e *Enricher) lookupAddr(addr string) string {
	ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
	defer cancel()
	names, err := e.resolver.LookupAddr(ctx, addr)
	if err != nil {
		e.log.Debug("enrich: reverse dns failed", "ip", addr, "err", err)
		return ""
	}
	if len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}

// ttlEntry is one cached value with its expiry time.
type ttlEntry[T any] struct {
	value     T
	expiresAt time.Time
}

// ttlCache is a minimal mutex-guarded cache with per-entry TTL.
type ttlCache[T any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]ttlEntry[T]
}

func newTTLCache[T any](ttl time.Duration) ttlCache[T] {
	return ttlCache[T]{
		ttl:     ttl,
		now:     time.Now,
		entries: make(map[string]ttlEntry[T]),
	}
}

func (c *ttlCache[T]) get(key string) (value T, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries[key]
	if !found || !c.now().Before(entry.expiresAt) {
		var zero T
		return zero, false
	}
	return entry.value, true
}

func (c *ttlCache[T]) put(key string, value T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = ttlEntry[T]{value: value, expiresAt: c.now().Add(c.ttl)}
}
