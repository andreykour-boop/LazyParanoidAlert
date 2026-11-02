package trap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"time"

	"siren/internal/config"
	"siren/internal/event"
)

// Emulator answers one accepted connection the way the decoyed service
// would. Handle owns the connection for its duration and closes it.
type Emulator interface {
	Handle(ctx context.Context, conn net.Conn) error
}

// EmulatorFunc adapts a function to the Emulator interface.
type EmulatorFunc func(ctx context.Context, conn net.Conn) error

// Handle implements Emulator.
func (f EmulatorFunc) Handle(ctx context.Context, conn net.Conn) error {
	return f(ctx, conn)
}

// Factory builds the emulator for a configured trap; the config package
// guarantees emulator name validity, so an unknown name is a programming
// error.
type Factory func(t config.Trap) (Emulator, error)

// Manager owns the decoy listeners: one accept loop goroutine per enabled
// trap, one handler goroutine per connection. Run blocks until ctx is
// canceled and every listener has stopped.
type Manager struct {
	factory Factory
	events  chan<- event.Event
	log     *slog.Logger
}

// New returns a Manager that reports each accepted connection to events and
// logs its own diagnostics to log.
func New(factory Factory, events chan<- event.Event, log *slog.Logger) *Manager {
	return &Manager{factory: factory, events: events, log: log}
}

// Run opens a listener for every enabled trap and serves until ctx is done.
// A port that cannot be opened (occupied, privileged) is skipped with a log
// warning: one unusable port never stops the agent. Run returns nil on clean
// shutdown.
func (m *Manager) Run(ctx context.Context, traps []config.Trap) error {
	var listeners []net.Listener
	for _, t := range traps {
		if !t.Enabled {
			continue
		}
		emu, err := m.factory(t)
		if err != nil {
			m.log.Warn("trap emulator unavailable, skipping port",
				"port", t.Port, "emulator", t.Emulator, "err", err)
			continue
		}
		// Emulators that observe client credentials (SSH) report extra
		// auth facts through the same event channel.
		if rep, ok := emu.(CredentialReporter); ok {
			rep.SetReporter(m.emitEvent, m.log)
		}
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(t.Port))
		if err != nil {
			m.log.Warn("trap port not opened, skipping",
				"port", t.Port, "emulator", t.Emulator, "err", err)
			continue
		}
		listeners = append(listeners, ln)
		go m.acceptLoop(ctx, ln, t, emu)
	}
	m.log.Info("trap listeners started", "count", len(listeners))

	// Every acceptLoop exits when ctx is done; listeners are closed here so
	// blocked Accept calls return immediately.
	<-ctx.Done()
	for _, ln := range listeners {
		_ = ln.Close()
	}
	return nil
}

// emitEvent forwards one emulator-produced event to the pipeline, dropping
// it only when the agent is shutting down.
func (m *Manager) emitEvent(ev event.Event) {
	select {
	case m.events <- ev:
	case <-time.After(2 * time.Second):
		m.log.Warn("trap event dropped, pipeline busy", "kind", ev.Kind, "port", ev.DstPort)
	}
}

// acceptLoop accepts connections until the listener closes. Each connection
// is handled in its own goroutine: an emulator error on one connection must
// not affect the listener or other connections.
func (m *Manager) acceptLoop(ctx context.Context, ln net.Listener, t config.Trap, emu Emulator) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			// Listener closed by Run during shutdown: normal exit.
			return
		}
		go m.handleConn(ctx, conn, t, emu)
	}
}

// handleConn reports the connection fact and hands the connection to the
// emulator. Errors are logged, never propagated: the listener keeps serving.
func (m *Manager) handleConn(ctx context.Context, conn net.Conn, t config.Trap, emu Emulator) {
	defer func() {
		if err := conn.Close(); err != nil {
			m.log.Debug("trap conn close", "port", t.Port, "err", err)
		}
	}()

	remote := conn.RemoteAddr()
	ta, ok := remote.(*net.TCPAddr)
	if !ok {
		m.log.Warn("trap got non-TCP remote address", "port", t.Port, "remote", remote.String())
		return
	}

	ev := event.Event{
		Kind:     event.KindTrapConnect,
		Time:     time.Now(),
		Protocol: t.Emulator,
		SrcIP:    ta.AddrPort().Addr(),
		SrcPort:  int(ta.AddrPort().Port()),
		DstPort:  t.Port,
	}
	select {
	case m.events <- ev:
	case <-ctx.Done():
		return
	}

	if err := emu.Handle(ctx, conn); err != nil {
		// A client disconnect mid-dialog is normal traffic; anything else
		// deserves an Info record for diagnostics.
		if errors.Is(err, errClientClosed) {
			m.log.Debug("trap client disconnected", "port", t.Port)
		} else {
			m.log.Info("trap emulator finished with error", "port", t.Port, "err", err)
		}
	}
}
