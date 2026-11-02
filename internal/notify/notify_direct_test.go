package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"siren/internal/aggregate"
	"siren/internal/event"
)

// TestNotifierDirectDeliversIdenticalReports: planned health reports go
// through the direct channel; two identical texts in a row must BOTH be
// delivered — the aggregation/dedup layer that could collapse them is
// bypassed by design.
func TestNotifierDirectDeliversIdenticalReports(t *testing.T) {
	n, api := newTestNotifier(t, []int{200}, nil)
	in := make(chan aggregate.Notice, 1)
	direct := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- n.RunWithDirect(ctx, in, direct) }()

	report := "📅 Еженедельный отчёт о дисках — TEST-01"
	direct <- report
	direct <- report

	deadline := time.Now().Add(2 * time.Second)
	for api.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if got := api.count(); got != 2 {
		t.Fatalf("delivered %d messages, want 2 identical reports", got)
	}
	if api.text(0) != report || api.text(1) != report {
		t.Errorf("texts wrong: %q, %q", api.text(0), api.text(1))
	}
}

// TestNotifierDirectSharesQueueWithEvents: a direct message sent while the
// event queue is busy joins the same pending queue (order preserved, no
// loss).
func TestNotifierDirectSharesQueueWithEvents(t *testing.T) {
	n, api := newTestNotifier(t, []int{200}, nil)
	in := make(chan aggregate.Notice, 4)
	direct := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- n.RunWithDirect(ctx, in, direct) }()

	in <- aggregate.Notice{First: baseEvent(event.KindAuthAttempt), Last: baseEvent(event.KindAuthAttempt), Count: 1}
	direct <- "прямое сообщение"

	deadline := time.Now().Add(2 * time.Second)
	for api.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if got := api.count(); got != 2 {
		t.Fatalf("delivered %d, want 2 (event + direct)", got)
	}
	// Both texts must be delivered; the relative order of a buffered event
	// and a buffered direct message is not guaranteed (select fairness).
	directOK := api.text(0) == "прямое сообщение" || api.text(1) == "прямое сообщение"
	eventOK := strings.Contains(api.text(0), "ПОПЫТКА ВХОДА") || strings.Contains(api.text(1), "ПОПЫТКА ВХОДА")
	if !directOK || !eventOK {
		t.Errorf("texts wrong: %q / %q", api.text(0), api.text(1))
	}
}
