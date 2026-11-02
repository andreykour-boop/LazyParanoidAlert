package doors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"siren/internal/event"
)

// fakeStream replays scripted results: values are lines, err ends the stream.
type fakeStream struct {
	mu     sync.Mutex
	lines  chan string
	closed bool
}

func newFakeStream(lines ...string) *fakeStream {
	fs := &fakeStream{lines: make(chan string, len(lines)+1)}
	for _, l := range lines {
		fs.lines <- l
	}
	return fs
}

// Next implements lineStream.
func (f *fakeStream) Next() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return "", errors.New("stream closed")
	}
	select {
	case l := <-f.lines:
		return l, nil
	default:
		return "", io.EOF
	}
}

// Close implements lineStream.
func (f *fakeStream) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func TestRunStreamsRestartsAfterEOF(t *testing.T) {
	calls := 0
	var mu sync.Mutex
	factory := func(context.Context) (lineStream, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return newFakeStream(fmt.Sprintf("line-%d", calls)), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	var got []string
	runStreams(ctx, slog.Default(), factory, func(line string) {
		mu.Lock()
		got = append(got, line)
		mu.Unlock()
		if len(got) == 3 {
			cancel() // enough proof of restarts
		}
	})
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("want 3 lines across restarts, got %v", got)
	}
	if calls < 3 {
		t.Fatalf("factory must be called per restart, got %d calls", calls)
	}
}

func TestRunStreamsBacksOffOnFactoryError(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	factory := func(context.Context) (lineStream, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls < 3 {
			return nil, errors.New("journalctl not started yet")
		}
		return newFakeStream("recovered"), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	var got []string
	done := make(chan struct{})
	go func() {
		runStreams(ctx, slog.Default(), factory, func(line string) {
			mu.Lock()
			got = append(got, line)
			mu.Unlock()
			cancel()
			close(done)
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runStreams did not recover from factory errors")
	}
	// Two failed opens must delay ~1s+2s before the third call succeeds.
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Fatalf("backoff not applied, elapsed %v", elapsed)
	}
	if len(got) != 1 || got[0] != "recovered" {
		t.Fatalf("got %v, want [recovered]", got)
	}
}

func TestRunStreamsStopsOnCtxCancelWhileBlocked(t *testing.T) {
	// A stream whose Next blocks forever models a quiet `journalctl -f`;
	// with real journalctl the ctx cancel kills the process and unblocks
	// the reader, which the fake models via its done channel.
	ctx, cancel := context.WithCancel(context.Background())
	blocking := newBlockingStream(ctx.Done())
	factory := func(context.Context) (lineStream, error) {
		return blocking, nil
	}
	done := make(chan struct{})
	go func() {
		runStreams(ctx, slog.Default(), factory, func(string) {})
		close(done)
	}()
	time.Sleep(100 * time.Millisecond) // let it block in Next
	cancel()
	blocking.Close() // models the process being killed by ctx cancel
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runStreams did not stop after ctx cancel")
	}
}

// blockingStream blocks in Next until released or done closes.
type blockingStream struct {
	releaseOnce sync.Once
	released    chan struct{}
	done        <-chan struct{}
}

func newBlockingStream(done <-chan struct{}) *blockingStream {
	return &blockingStream{released: make(chan struct{}), done: done}
}

func (b *blockingStream) Next() (string, error) {
	select {
	case <-b.released:
	case <-b.done:
	}
	return "", io.EOF
}

func (b *blockingStream) Close() error {
	b.releaseOnce.Do(func() { close(b.released) })
	return nil
}

func (b *blockingStream) release() {
	b.releaseOnce.Do(func() { close(b.released) })
}

func TestJournalLineToEvent(t *testing.T) {
	line := `{"MESSAGE":"Failed password for invalid user admin from 203.0.113.7 port 54321 ssh2","__REALTIME_TIMESTAMP":"1772222222222222"}`
	ev, ok, err := journalLineToEvent(line, "SRV-02")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !ok {
		t.Fatal("failed password record must produce an event")
	}
	if ev.Kind != event.KindAuthAttempt {
		t.Errorf("kind = %q", ev.Kind)
	}
	if ev.Account != "admin" {
		t.Errorf("account = %q", ev.Account)
	}
	if ev.SrcIP.String() != "203.0.113.7" {
		t.Errorf("ip = %q", ev.SrcIP)
	}
	if ev.Machine != "SRV-02" {
		t.Errorf("machine = %q", ev.Machine)
	}
	// Timestamp must come from the journal record, not from time.Now().
	if want := time.UnixMicro(1772222222222222); !ev.Time.Equal(want) {
		t.Errorf("time = %v, want %v", ev.Time, want)
	}
}

func TestJournalLineToEventNonAuth(t *testing.T) {
	line := `{"MESSAGE":"Server listening on 0.0.0.0 port 22.","__REALTIME_TIMESTAMP":"1772222222222222"}`
	if _, ok, err := journalLineToEvent(line, "SRV-02"); err != nil || ok {
		t.Fatalf("non-auth record must be skipped, got ok=%v err=%v", ok, err)
	}
}

func TestJournalLineToEventBrokenJSON(t *testing.T) {
	if _, _, err := journalLineToEvent("{broken", "SRV-02"); err == nil {
		t.Fatal("broken JSON must return an error for diagnostics")
	}
}
