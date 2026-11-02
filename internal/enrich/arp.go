package enrich

import (
	"log/slog"
	"net/netip"
	"sync"
	"time"
)

// arpRefreshTTL bounds how often the host neighbor table is re-read.
const arpRefreshTTL = 30 * time.Second

// arpTable is the default ARPTable: it re-reads the host neighbor table at
// most once per TTL and serves lookups from the last snapshot. Read errors
// keep the previous snapshot and are throttled the same way.
type arpTable struct {
	ttl time.Duration
	now func() time.Time
	log *slog.Logger
	// read returns the current neighbor table; replaced in tests.
	read func() (map[netip.Addr]string, error)

	mu       sync.Mutex
	table    map[netip.Addr]string
	loadedAt time.Time
}

// NewARPTable builds the platform neighbor table source.
func NewARPTable(log *slog.Logger) ARPTable {
	if log == nil {
		log = slog.Default()
	}
	return &arpTable{
		ttl:  arpRefreshTTL,
		now:  time.Now,
		log:  log,
		read: readARPTable,
	}
}

// MAC implements ARPTable. Only IPv4 neighbors are tracked: both platform
// sources (iphlpapi GetIpNetTable, /proc/net/arp) are IPv4-only.
func (a *arpTable) MAC(ip netip.Addr) (string, bool) {
	if !ip.IsValid() || !ip.Is4() {
		return "", false
	}
	a.refresh()
	a.mu.Lock()
	defer a.mu.Unlock()
	mac, ok := a.table[ip]
	return mac, ok
}

// refresh re-reads the neighbor table when the snapshot is stale.
func (a *arpTable) refresh() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.loadedAt.IsZero() && a.now().Sub(a.loadedAt) < a.ttl {
		return
	}
	table, err := a.read()
	if err != nil {
		// Throttle failed reads the same way: one attempt per TTL, the
		// previous snapshot stays in force.
		a.log.Warn("enrich: arp table read failed", "err", err)
	} else {
		a.table = table
	}
	a.loadedAt = a.now()
}
