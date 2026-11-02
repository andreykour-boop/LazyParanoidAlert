package aggregate

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"siren/internal/event"
)

// bruteForceEvent builds one failed-login event from src.
func bruteForceEvent(src string, port int) event.Event {
	return event.Event{
		Kind:     event.KindAuthAttempt,
		Time:     time.Now(),
		Machine:  "SRV-02",
		Protocol: "ssh",
		SrcIP:    netip.MustParseAddr(src),
		SrcPort:  port,
		DstPort:  22,
		Account:  "root",
	}
}

// newTestAggregator builds an aggregator with a controllable clock.
func newTestAggregator(window time.Duration) (*Aggregator, *time.Time) {
	base := time.Now()
	a := New(window, slog.Default())
	a.now = func() time.Time { return base }
	return a, &base
}

func TestBruteforceCollapsesToFifty(t *testing.T) {
	a, base := newTestAggregator(5 * time.Minute)

	// 50 failed attempts from one IP: exactly one immediate notice.
	var firsts, counted int
	for i := 0; i < 50; i++ {
		if n := a.Submit(bruteForceEvent("203.0.113.7", 50000+i)); n != nil {
			firsts++
		} else {
			counted++
		}
	}
	if firsts != 1 || counted != 49 {
		t.Fatalf("firsts=%d counted=%d, want 1 and 49", firsts, counted)
	}

	// Window closes: one digest with the total, nothing else.
	*base = base.Add(6 * time.Minute)
	notices := a.Flush()
	if len(notices) != 1 {
		t.Fatalf("digests = %d, want 1", len(notices))
	}
	n := notices[0]
	if !n.Digest || n.Count != 50 {
		t.Errorf("digest = %+v, want count 50", n)
	}
	if n.First.SrcIP.String() != "203.0.113.7" || n.Last.SrcPort != 50049 {
		t.Errorf("digest boundaries wrong: first ip %v, last port %d", n.First.SrcIP, n.Last.SrcPort)
	}
}

func TestSingleEventNoDigest(t *testing.T) {
	a, base := newTestAggregator(time.Minute)
	if n := a.Submit(bruteForceEvent("192.168.1.10", 40000)); n == nil {
		t.Fatal("first event must be delivered immediately")
	}
	*base = base.Add(2 * time.Minute)
	if notices := a.Flush(); len(notices) != 0 {
		t.Errorf("single event must not produce a digest, got %d", len(notices))
	}
	// The window is closed: a new event starts a fresh first notice.
	if n := a.Submit(bruteForceEvent("192.168.1.10", 40001)); n == nil {
		t.Error("event after closed window must be delivered as first")
	}
}

func TestIndependentSourcesAndKinds(t *testing.T) {
	a, _ := newTestAggregator(time.Minute)
	ev1 := bruteForceEvent("192.168.1.10", 40000)
	ev2 := bruteForceEvent("192.168.1.11", 40000)
	ev3 := bruteForceEvent("192.168.1.10", 40001)
	ev3.Kind = event.KindTrapConnect
	ev3.Protocol = "http"
	ev3.Account = ""

	for _, ev := range []event.Event{ev1, ev2, ev3} {
		if n := a.Submit(ev); n == nil {
			t.Fatalf("event %+v must open its own window", ev)
		}
	}
}

func TestDedupDropsRepeatedFact(t *testing.T) {
	a, base := newTestAggregator(time.Minute)
	ev := event.Event{
		Kind: event.KindAuthLogin, Protocol: "rdp",
		SrcIP: netip.MustParseAddr("192.168.1.10"), Account: "andrey",
		Time: time.Now(), Machine: "WIN-PC01",
	}
	// One RDP login arrives as both 1149 and 4624: identical fingerprint.
	if n := a.Submit(ev); n == nil {
		t.Fatal("first fact must be delivered")
	}
	if n := a.Submit(ev); n != nil {
		t.Error("duplicate fact within dedup TTL must be dropped")
	}
	a.mu.Lock()
	w := a.windows[aggKey{ip: ev.SrcIP, kind: ev.Kind}]
	a.mu.Unlock()
	if w.count != 1 {
		t.Errorf("window count = %d, want 1: duplicate must not count", w.count)
	}
	// After the TTL the same fact is a fresh event: it must join the
	// window as a counted repeat.
	*base = base.Add(a.dedupTTL + time.Second)
	if n := a.Submit(ev); n != nil {
		t.Error("fact after dedup TTL joins the open window, not a new notice")
	}
	a.mu.Lock()
	w = a.windows[aggKey{ip: ev.SrcIP, kind: ev.Kind}]
	a.mu.Unlock()
	if w.count != 2 {
		t.Errorf("window count = %d, want 2 after dedup expiry", w.count)
	}
}

func TestDedupKeepsDifferentAccounts(t *testing.T) {
	a, _ := newTestAggregator(time.Minute)
	ev := bruteForceEvent("203.0.113.7", 51000)
	ev.Kind = event.KindAuthLogin
	if n := a.Submit(ev); n == nil {
		t.Fatal("first must be delivered")
	}
	ev.Account = "admin"
	if n := a.Submit(ev); n != nil {
		t.Error("different account is a different fact, must be counted")
	}
	a.mu.Lock()
	w := a.windows[aggKey{ip: ev.SrcIP, kind: ev.Kind}]
	a.mu.Unlock()
	if w.count != 2 {
		t.Errorf("window count = %d, want 2: different accounts must not dedup", w.count)
	}
}

func TestDedupDoesNotTouchAttemptsAndTraps(t *testing.T) {
	a, _ := newTestAggregator(time.Minute)
	// A burst of identical failed attempts must keep counting: dedup is
	// scoped to successful logins only.
	for i := 0; i < 5; i++ {
		a.Submit(bruteForceEvent("203.0.113.7", 51000+i))
	}
	a.mu.Lock()
	w := a.windows[aggKey{ip: netip.MustParseAddr("203.0.113.7"), kind: event.KindAuthAttempt}]
	a.mu.Unlock()
	if w == nil || w.count != 5 {
		t.Errorf("window count = %+v, want 5 attempts counted", w)
	}
}

func TestDedupMapPrunedOnFlush(t *testing.T) {
	a, base := newTestAggregator(time.Minute)
	for i := 0; i < 100; i++ {
		ev := bruteForceEvent("203.0.113.7", 52000+i)
		ev.Kind = event.KindAuthLogin
		ev.Account = "user" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		a.Submit(ev)
	}
	if len(a.seen) != 100 {
		t.Fatalf("seen = %d entries, want 100", len(a.seen))
	}
	*base = base.Add(2 * time.Minute)
	a.Flush()
	if len(a.seen) != 0 {
		t.Errorf("seen = %d entries after prune, want 0", len(a.seen))
	}
}

func TestWindowsMapBoundedOverLongRun(t *testing.T) {
	a, base := newTestAggregator(30 * time.Second)
	// 50 rounds x 1000 sources with 20 repeats each; every round crosses a
	// window boundary, so windows and dedup entries are created and must be
	// cleaned up again.
	for round := 0; round < 50; round++ {
		for i := 0; i < 1000; i++ {
			src := fmt.Sprintf("10.%d.%d.%d", round%250+1, i/250, i%250+1)
			for r := 0; r < 20; r++ {
				a.Submit(bruteForceEvent(src, 60000+r))
			}
		}
		*base = base.Add(31 * time.Second)
		a.Flush()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.windows) > 1000 {
		t.Errorf("windows map grew unbounded: %d", len(a.windows))
	}
	if len(a.seen) > 1000 {
		t.Errorf("dedup map grew unbounded: %d", len(a.seen))
	}
}

func TestRunDeliversNoticesAndFinalFlush(t *testing.T) {
	a, base := newTestAggregator(time.Minute)
	in := make(chan event.Event, 64)
	out := make(chan Notice, 64)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, in, out) }()

	for i := 0; i < 50; i++ {
		in <- bruteForceEvent("203.0.113.9", 53000+i)
	}

	// First notice arrives, the rest are counted.
	select {
	case n := <-out:
		if n.Digest || n.Count != 1 {
			t.Errorf("first notice = %+v", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no first notice")
	}
	// No other notices until the window closes.
	select {
	case n := <-out:
		t.Errorf("unexpected early notice: %+v", n)
	case <-time.After(1500 * time.Millisecond):
	}

	// Close the window, then shut down: the pending digest must be
	// delivered by the final flush. out is buffered wide enough for every
	// notice, so Run never blocks: wait for its exit first, then drain.
	*base = base.Add(2 * time.Minute)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Run must return the ctx error on shutdown")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not exit after ctx cancel")
	}
	select {
	case n := <-out:
		if !n.Digest || n.Count != 50 {
			t.Errorf("digest = %+v, want count 50", n)
		}
	case <-time.After(time.Second):
		t.Fatal("no digest after Run exit")
	}
}
