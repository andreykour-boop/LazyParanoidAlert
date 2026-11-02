package trap

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"siren/internal/config"
	"siren/internal/event"
)

// collectEvents drains events into a slice, safe for concurrent senders.
type collector struct {
	ch     chan event.Event
	got    []event.Event
	done   chan struct{}
	cancel context.CancelFunc
}

func newCollector() *collector {
	c := &collector{ch: make(chan event.Event, 16), done: make(chan struct{})}
	var ctx context.Context
	ctx, c.cancel = context.WithCancel(context.Background())
	go func() {
		defer close(c.done)
		for {
			select {
			case ev := <-c.ch:
				c.got = append(c.got, ev)
			case <-ctx.Done():
				// Drain whatever is left buffered after cancel.
				for {
					select {
					case ev := <-c.ch:
						c.got = append(c.got, ev)
					default:
						return
					}
				}
			}
		}
	}()
	return c
}

// freePort asks the OS for a free TCP port and releases it immediately. The
// small race window until the test binds the port again is acceptable for
// localhost tests.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return port
}

// failingEmulator always errors and never reads or writes the connection.
var failingEmulator = EmulatorFunc(func(context.Context, net.Conn) error {
	return errors.New("emulator boom")
})

func TestManagerFreeAndOccupiedPorts(t *testing.T) {
	free := freePort(t)
	free2 := freePort(t)

	mgr := New(func(config.Trap) (Emulator, error) { return failingEmulator, nil },
		make(chan event.Event, 16), slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	traps := []config.Trap{
		{Port: free, Enabled: true, Emulator: "http"},
		{Port: free2, Enabled: true, Emulator: "ftp"},
	}
	errCh := make(chan error, 1)
	go func() { errCh <- mgr.Run(ctx, traps) }()

	// Wait until both listeners accept, then verify connectivity.
	dial := func(port int) net.Conn {
		t.Helper()
		var conn net.Conn
		var err error
		for i := 0; i < 50; i++ {
			conn, err = net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
			if err == nil {
				return conn
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("dial port %d: %v", port, err)
		return nil
	}
	c1 := dial(free)
	c2 := dial(free2)
	_ = c1.Close()
	_ = c2.Close()

	// Disabled trap: manager must not listen on it. The port is released,
	// so a successful dial would mean the manager bound the disabled trap.
	other := freePort(t)
	cancel()
	waitRun(t, errCh, "first Run")

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go mgr.Run(ctx2, []config.Trap{{Port: other, Enabled: false}})
	time.Sleep(300 * time.Millisecond)
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(other), 300*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("disabled port must not be listened on")
	}
}

// waitRun asserts that a Manager.Run goroutine returned into errCh without
// error within 5 seconds.
func waitRun(t *testing.T, errCh <-chan error, label string) {
	t.Helper()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("%s returned error: %v", label, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return in time", label)
	}
}

func TestManagerOccupiedPortSkipped(t *testing.T) {
	// Occupy a port ourselves; the manager must skip it and still serve
	// another free port.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold port: %v", err)
	}
	defer func() { _ = held.Close() }()
	occupied := held.Addr().(*net.TCPAddr).Port
	free := freePort(t)

	mgr := New(func(config.Trap) (Emulator, error) { return failingEmulator, nil },
		make(chan event.Event, 16), slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Run(ctx, []config.Trap{
		{Port: occupied, Enabled: true, Emulator: "http"},
		{Port: free, Enabled: true, Emulator: "http"},
	})

	// The free port becomes connectable; the occupied one stays served by
	// its real holder, not by the trap.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(free), time.Second); err == nil {
			_ = conn.Close()
			cancel()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("free port was not served")
}

func TestManagerEmulatorFailureDoesNotKillListener(t *testing.T) {
	port := freePort(t)
	col := newCollector()
	defer col.cancel()

	mgr := New(func(config.Trap) (Emulator, error) { return failingEmulator, nil },
		col.ch, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Run(ctx, []config.Trap{{Port: port, Enabled: true, Emulator: "http"}})

	// Two consecutive connections: the first emulator error must not stop
	// the second from being accepted and reported.
	dial := func() {
		var conn net.Conn
		var err error
		for i := 0; i < 50; i++ {
			conn, err = net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
			if err == nil {
				// Read EOF/close eagerly so handleConn finishes.
				_, _ = bufio.NewReader(conn).ReadByte()
				_ = conn.Close()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("dial port %d: %v", port, err)
	}
	dial()
	dial()

	deadline := time.After(5 * time.Second)
	got := 0
	for got < 2 {
		select {
		case <-col.done:
			t.Fatal("collector stopped early")
		case <-deadline:
			t.Fatalf("want 2 events, got %d", got)
		default:
		}
		if len(col.got) > got {
			got = len(col.got)
		}
		time.Sleep(10 * time.Millisecond)
	}

	ev := col.got[0]
	if ev.Kind != event.KindTrapConnect {
		t.Errorf("event kind = %q", ev.Kind)
	}
	if ev.DstPort != port {
		t.Errorf("event dst port = %d, want %d", ev.DstPort, port)
	}
	if ev.Protocol != "http" {
		t.Errorf("event protocol = %q", ev.Protocol)
	}
	if !ev.SrcIP.IsValid() {
		t.Error("event source IP must be present")
	}
	if ev.SrcPort == 0 {
		t.Error("event source port must be present")
	}
	if ev.Time.IsZero() {
		t.Error("event time must be set")
	}
}
