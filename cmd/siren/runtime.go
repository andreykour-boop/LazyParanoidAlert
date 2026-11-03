package main

import (
	"context"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"log/slog"

	"siren/internal/aggregate"
	"siren/internal/config"
	"siren/internal/enrich"
	"siren/internal/event"
	"siren/internal/health"
	"siren/internal/notify"
	"siren/internal/trap"
)

// Pipeline channel capacities: enough to absorb a scan burst without
// blocking the trap listeners; the aggregator collapses the burst anyway.
const (
	rawQueueLen = 256
	outQueueLen = 64
	// enrichTTL bounds hostname and MAC caches.
	enrichTTL = 5 * time.Minute
)

// runAgent wires every component into one pipeline and serves until ctx or
// a signal ends it:
//
//	traps ─┐
//	doors ─┴→ raw → allowlist+enrich → aggregator → notifier → Telegram
//
// The first component failure cancels the context and is returned.
func runAgent(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	raw := make(chan event.Event, rawQueueLen)
	filtered := make(chan event.Event, rawQueueLen)
	notices := make(chan aggregate.Notice, outQueueLen)

	var wg sync.WaitGroup
	firstErr := make(chan error, 1)

	// run launches one component; a non-shutdown error cancels everything.
	run := func(name string, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(ctx); err != nil && ctx.Err() == nil {
				log.Error("component failed, stopping agent", "component", name, "err", err)
				select {
				case firstErr <- err:
				default:
				}
				stop()
			}
		}()
	}

	// Decoy listeners.
	factory := trap.NewTrapFactory(cfg.DataDir)
	manager := trap.New(factory, raw, log)
	run("traps", func(ctx context.Context) error { return manager.Run(ctx, cfg.Traps) })

	// Authentication sensors on occupied ports (platform-specific).
	run("doors", func(ctx context.Context) error { return runDoors(ctx, cfg, raw, log) })

	// Allowlist filter and enrichment pump.
	allow := newAllowlist(cfg.Allowlist, log)
	enricher := enrich.New(cfg.NameMap, nil, enrich.NewARPTable(log), enrichTTL, log)
	run("enrich", func(ctx context.Context) error {
		return pumpEnrich(ctx, allow, enricher, cfg.AgentName, raw, filtered, log)
	})

	// Aggregation and delivery.
	aggregator := aggregate.New(time.Duration(cfg.AggregateWindowSeconds)*time.Second, log)
	run("aggregate", func(ctx context.Context) error { return aggregator.Run(ctx, filtered, notices) })

	client := &notify.Client{Token: cfg.Telegram.Token, ChatID: cfg.Telegram.ChatID, Log: log}
	if cfg.Telegram.APIURL != "" {
		client.APIURL = cfg.Telegram.APIURL
		log.Info("telegram api overridden", "api_url", cfg.Telegram.APIURL)
	}
	agentIP := resolveAgentIP()
	notifier := notify.New(client, agentIP, log)

	// Planned health texts (weekly report, space reminders) and the boot
	// alert go straight to the delivery queue. The notifier gets nil when
	// the health watch is disabled (channel blocks forever, the notifier
	// just never selects it); the boot sensor always uses the real
	// channel — its alert must survive a disabled health watch.
	direct := make(chan string, 16)
	notifDirect := direct
	healthEnabled := cfg.Health != nil && cfg.Health.Enabled
	if !healthEnabled {
		notifDirect = nil
	}
	run("notify", func(ctx context.Context) error {
		return notifier.RunWithDirect(ctx, notices, notifDirect)
	})

	// Boot sensor: one-shot at pipeline start, immediate alert on a new
	// machine boot through the direct channel (D2 of add-boot-events).
	run("boot", func(ctx context.Context) error {
		return runBoot(ctx, cfg, agentIP, direct, log)
	})

	// Disk health watch: collectors per platform, wall-clock schedule,
	// alarms through the common pipeline.
	if healthEnabled {
		h := cfg.Health
		l1, l2, vols := health.NewPlatformSources()
		collector := &health.Collector{L1Sources: l1, L2Sources: l2, VolumesSources: vols}
		runner := &health.Runner{
			Collect:       collector.Snapshot,
			DataDir:       cfg.DataDir,
			AgentLabel:    cfg.AgentName + " (" + agentIP + ")",
			Thresholds:    health.Thresholds{WarnPct: h.WarnPct, MinFreeGB: h.MinFreeGB},
			CheckHour:     h.CheckHour,
			ReportWeekday: h.ReportWeekday,
			Alarm: func(kind event.Kind, detail string) {
				ev := event.Event{Kind: kind, Time: time.Now(), Machine: cfg.AgentName, Detail: detail}
				select {
				case raw <- ev:
				case <-ctx.Done():
				}
			},
			Report: func(text string) {
				select {
				case direct <- text:
				case <-ctx.Done():
				}
			},
			Log: log,
		}
		run("health", runner.Run)
		log.Info("health watch enabled",
			"check_hour", h.CheckHour, "report_weekday", h.ReportWeekday,
			"warn_pct", h.WarnPct, "min_free_gb", h.MinFreeGB)
	} else {
		log.Info("health watch disabled")
	}

	log.Info("siren agent started",
		"agent", cfg.AgentName,
		"ip", agentIP,
		"traps", len(cfg.Traps),
		"window", cfg.AggregateWindowSeconds,
	)

	wg.Wait()
	select {
	case err := <-firstErr:
		return err
	default:
		return nil
	}
}

// pumpEnrich drops allowlisted sources, enriches the rest and forwards them
// to the aggregator.
func pumpEnrich(ctx context.Context, allow *allowlist, en *enrich.Enricher, agentName string, in <-chan event.Event, out chan<- event.Event, log *slog.Logger) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-in:
			// Dual-stack listeners hand out IPv4-mapped IPv6 addresses
			// ("::ffff:1.2.3.4"); unmapping restores the plain IPv4 form
			// that the allowlist, ARP and alerts expect.
			if ev.SrcIP.Is4In6() {
				ev.SrcIP = ev.SrcIP.Unmap()
			}
			if ev.Machine == "" {
				// Trap events carry no machine name; door events do.
				ev.Machine = agentName
			}
			if allow.match(ev.SrcIP, ev.DstPort) {
				log.Debug("event allowlisted", "ip", ev.SrcIP.String(), "port", ev.DstPort)
				continue
			}
			en.Enrich(&ev)
			select {
			case out <- ev:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// allowlistEntry is one parsed allowlist record: a source address or
// network plus the optional port scope (nil = every port).
type allowlistEntry struct {
	prefix netip.Prefix
	ports  []int
}

// allowlist holds parsed IPs and CIDR nets that never alert. An entry may
// carry a port scope: "host:22" suppresses only events tied to port 22,
// an entry without ports suppresses everything from the source.
type allowlist struct {
	entries []allowlistEntry
	log     *slog.Logger
}

func newAllowlist(entries []string, log *slog.Logger) *allowlist {
	a := &allowlist{log: log}
	for _, entry := range entries {
		// Parse with the same parser the config validation uses (design
		// D4), so the matcher and the validator can not drift apart. A
		// failure is unreachable for a validated config (edited on the
		// fly only): fail open towards visibility and log it.
		addr, ports, err := config.ParseAllowlistEntry(entry)
		if err != nil {
			log.Warn("allowlist entry ignored", "entry", entry, "err", err)
			continue
		}
		if strings.Contains(addr, "/") {
			prefix, err := netip.ParsePrefix(addr)
			if err != nil {
				log.Warn("allowlist entry ignored", "entry", entry, "err", err)
				continue
			}
			a.entries = append(a.entries, allowlistEntry{prefix: prefix.Masked(), ports: ports})
			continue
		}
		addrIP, err := netip.ParseAddr(addr)
		if err != nil {
			log.Warn("allowlist entry ignored", "entry", entry, "err", err)
			continue
		}
		a.entries = append(a.entries, allowlistEntry{
			prefix: netip.PrefixFrom(addrIP, addrIP.BitLen()),
			ports:  ports,
		})
	}
	return a
}

// match reports whether an event from ip tied to port is allowlisted.
// Unknown sources and invalid addresses are never allowlisted (they must
// stay visible); a port-scoped entry never matches an event without a
// port (port 0).
func (a *allowlist) match(ip netip.Addr, port int) bool {
	if !ip.IsValid() {
		return false
	}
	for _, e := range a.entries {
		if !e.prefix.Contains(ip) {
			continue
		}
		if len(e.ports) == 0 {
			return true
		}
		for _, p := range e.ports {
			if p == port {
				return true
			}
		}
	}
	return false
}
