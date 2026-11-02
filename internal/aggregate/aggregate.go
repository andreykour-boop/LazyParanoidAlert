package aggregate

import (
	"context"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"siren/internal/event"
)

// flushInterval bounds how long a closed window waits before its digest is
// emitted.
const flushInterval = time.Second

// defaultDedupTTL collapses duplicate facts about the same authentication:
// Windows reports one RDP login as both event 1149 and 4624. Legitimate
// repeat logins of the same account within the TTL are counted once, which
// is an accepted trade-off for a siren.
const defaultDedupTTL = 10 * time.Second

// Notice is one deliverable unit: either the first event of a fresh window
// (Digest=false, Count=1) or a digest over a closed window (Digest=true,
// Count=total). First/Last carry the burst boundaries for formatting.
type Notice struct {
	First  event.Event
	Last   event.Event
	Count  int
	Window time.Duration
	Digest bool
}

// aggKey groups events of the same kind from one source IP; the zero IP
// ("source unknown") forms its own group per kind.
type aggKey struct {
	ip   netip.Addr
	kind event.Kind
}

// fingerprint identifies one authentication fact for deduplication.
type fingerprint struct {
	ip       netip.Addr
	kind     event.Kind
	protocol string
	account  string
}

// aggWindow accumulates repeats of one (source, kind) pair.
type aggWindow struct {
	first, last event.Event
	count       int
	openedAt    time.Time
}

// Aggregator collapses bursts of similar events into first notifications
// plus periodic digests. Windows are processed-time based: an event joins
// the window open for its (source, kind) pair or opens a new one.
type Aggregator struct {
	window   time.Duration
	dedupTTL time.Duration
	now      func() time.Time
	log      *slog.Logger

	mu      sync.Mutex
	windows map[aggKey]*aggWindow
	seen    map[fingerprint]time.Time
}

// New builds an Aggregator with the configured aggregation window.
func New(window time.Duration, log *slog.Logger) *Aggregator {
	if log == nil {
		log = slog.Default()
	}
	return &Aggregator{
		window:   window,
		dedupTTL: defaultDedupTTL,
		now:      time.Now,
		log:      log,
		windows:  make(map[aggKey]*aggWindow),
		seen:     make(map[fingerprint]time.Time),
	}
}

// Submit processes one event. It returns a Notice when the event must be
// delivered right away (the first of a window); nil means the event was
// counted into an open window or dropped as a duplicate.
func (a *Aggregator) Submit(ev event.Event) *Notice {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()

	fp := fingerprint{ip: ev.SrcIP, kind: ev.Kind, protocol: ev.Protocol, account: ev.Account}
	// Deduplication targets the Windows double-report only: a successful
	// RDP login is recorded as both RCM 1149 and Security 4624. Failed
	// attempts and trap connections are never deduplicated, so a burst of
	// identical brute-force attempts keeps counting.
	if ev.Kind == event.KindAuthLogin {
		if at, dup := a.seen[fp]; dup && now.Sub(at) < a.dedupTTL {
			return nil
		}
		a.seen[fp] = now
	}

	k := aggKey{ip: ev.SrcIP, kind: ev.Kind}
	if w, ok := a.windows[k]; ok {
		w.last = ev
		w.count++
		return nil
	}
	a.windows[k] = &aggWindow{first: ev, last: ev, count: 1, openedAt: now}
	return &Notice{First: ev, Last: ev, Count: 1}
}

// Flush closes every expired window. Windows holding a single event close
// silently (that event was already delivered on Submit); larger ones become
// digest notices. It also prunes expired dedup entries, bounding memory.
func (a *Aggregator) Flush() []*Notice {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()

	var notices []*Notice
	for k, w := range a.windows {
		if now.Sub(w.openedAt) < a.window {
			continue
		}
		if w.count > 1 {
			notices = append(notices, &Notice{
				First:  w.first,
				Last:   w.last,
				Count:  w.count,
				Window: a.window,
				Digest: true,
			})
		}
		delete(a.windows, k)
	}
	for fp, at := range a.seen {
		if now.Sub(at) >= a.dedupTTL {
			delete(a.seen, fp)
		}
	}
	return notices
}

// Run pipes events through Submit and expired windows through Flush until
// ctx is done; pending digests are delivered on shutdown so a burst in
// progress is not lost.
func (a *Aggregator) Run(ctx context.Context, in <-chan event.Event, out chan<- Notice) error {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	emit := func(n *Notice) error {
		// Try the buffer first: after ctx cancel both selects below would
		// be ready and Go could pick ctx.Done, silently dropping the
		// pending digest. Consumers must drain out until Run returns, so
		// the fallback only blocks when out is genuinely full.
		select {
		case out <- *n:
			return nil
		default:
		}
		select {
		case out <- *n:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	for {
		select {
		case <-ctx.Done():
			for _, n := range a.Flush() {
				if err := emit(n); err != nil {
					return err
				}
			}
			return ctx.Err()
		case ev := <-in:
			if n := a.Submit(ev); n != nil {
				if err := emit(n); err != nil {
					return err
				}
			}
		case <-ticker.C:
			for _, n := range a.Flush() {
				if err := emit(n); err != nil {
					return err
				}
			}
		}
	}
}
