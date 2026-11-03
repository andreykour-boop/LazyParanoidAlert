//go:build windows

package doors

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"siren/internal/event"
)

var (
	modwevtapi       = syscall.NewLazyDLL("wevtapi.dll")
	procEvtSubscribe = modwevtapi.NewProc("EvtSubscribe")
	procEvtQuery     = modwevtapi.NewProc("EvtQuery")
	procEvtNext      = modwevtapi.NewProc("EvtNext")
	procEvtRender    = modwevtapi.NewProc("EvtRender")
	procEvtClose     = modwevtapi.NewProc("EvtClose")
)

const (
	// EVT_SUBSCRIBE_FLAGS: only events arriving after the subscription.
	evtSubscribeToFutureEvents = 1
	// EVT_SUBSCRIBE_NOTIFY_ACTION values.
	evtSubscribeActionDeliver = 1
	evtSubscribeActionError   = 2
	// EVT_RENDER_FLAGS: render the event as an XML UTF-16 string.
	evtRenderEventXml = 1
	// EVT_QUERY_FLAGS: the path argument is a channel name, not a file.
	evtQueryChannelPath = 1
)

// isEvtNoMore reports the EvtNext end-of-stream errors: ERROR_NO_MORE_ITEMS
// (259) when the result set is exhausted, ERROR_NO_MORE_DATA (15007) as the
// same condition surfaced by some paths.
func isEvtNoMore(err error) bool {
	return err == syscall.Errno(259) || err == syscall.Errno(15007)
}

// errInsufficientBuffer is returned by EvtRender when sizing the output.
const errInsufficientBuffer = syscall.Errno(122)

// WinSource subscribes to Windows event channels and pushes agent events.
// Access to the Security channel needs elevated rights (Event Log Readers
// group or administrator); a denied subscription is logged and skipped so
// the remaining channels keep working.
type WinSource struct {
	Machine string
	Events  chan<- event.Event
	Log     *slog.Logger

	// rendered carries rendered event XML from the OS callback thread to
	// the parser goroutine; sized to absorb bursts without blocking the
	// callback.
	rendered chan string

	mu      sync.Mutex
	handles []uintptr
	subs    map[uintptr]*subContext
	subSeq  uintptr
}

// NewWinSource builds the source for one machine (the local one).
func NewWinSource(machine string, events chan<- event.Event, log *slog.Logger) *WinSource {
	return &WinSource{
		Machine:  machine,
		Events:   events,
		Log:      log,
		rendered: make(chan string, 256),
		subs:     make(map[uintptr]*subContext),
	}
}

// Run subscribes to the watched channels and serves until ctx is done.
func (s *WinSource) Run(ctx context.Context) error {
	go s.parseLoop(ctx)

	// RDP facts: Security log for failed/successful remote logons and the
	// RemoteConnectionManager for the client address.
	s.subscribe("Security", "*[System[(EventID=4624 or EventID=4625)]]")
	s.subscribe("Microsoft-Windows-TerminalServices-RemoteConnectionManager/Operational",
		"*[System[(EventID=1149)]]")

	<-ctx.Done()
	s.mu.Lock()
	for _, h := range s.handles {
		procEvtClose.Call(h)
	}
	s.handles = nil
	s.mu.Unlock()
	subMu.Lock()
	for id := range s.subs {
		delete(subRegistry, id)
	}
	subMu.Unlock()
	return ctx.Err()
}

// subscribe registers one channel query; failures are logged, not fatal.
func (s *WinSource) subscribe(channel, query string) {
	s.mu.Lock()
	s.subSeq++
	sub := &subContext{id: s.subSeq, src: s, channel: channel}
	s.subs[sub.id] = sub
	subRegistry[sub.id] = sub // the callback resolves ids through this map
	s.mu.Unlock()

	chPtr, err := syscall.UTF16PtrFromString(channel)
	if err != nil {
		s.Log.Error("doors: bad channel name", "channel", channel, "err", err)
		return
	}
	qPtr, err := syscall.UTF16PtrFromString(query)
	if err != nil {
		s.Log.Error("doors: bad query", "channel", channel, "err", err)
		return
	}
	h, _, callErr := procEvtSubscribe.Call(
		0, // local session
		0, // no signal event: callback mode
		uintptr(unsafe.Pointer(chPtr)),
		uintptr(unsafe.Pointer(qPtr)),
		0,      // no bookmark
		sub.id, // context id handed back by the callback
		subscribeCallbackAddr,
		evtSubscribeToFutureEvents,
	)
	if h == 0 {
		s.Log.Warn("doors: subscription failed, channel skipped",
			"channel", channel, "err", callErr)
		s.mu.Lock()
		delete(s.subs, sub.id)
		subMu.Lock()
		delete(subRegistry, sub.id)
		subMu.Unlock()
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.handles = append(s.handles, h)
	s.mu.Unlock()
	s.Log.Info("doors: subscribed", "channel", channel)
}

// subContext is the per-subscription payload resolved by the callback via
// the id registry; the OS thread only ever sees the numeric id.
type subContext struct {
	id      uintptr
	src     *WinSource
	channel string
}

// subscribeCallback is registered once; the OS calls it on its own thread
// for every matching event of every subscription. It must return
// ERROR_SUCCESS (0); any other value cancels the subscription.
func subscribeCallback(action, ctxID, hEvent uintptr) uintptr {
	subMu.Lock()
	sub := subRegistry[ctxID]
	subMu.Unlock()
	if sub == nil {
		fmt.Println("doors: callback for unknown subscription id", ctxID)
		return 0
	}
	switch action {
	case evtSubscribeActionError:
		sub.src.Log.Warn("doors: subscription error", "channel", sub.channel)
	case evtSubscribeActionDeliver:
		xmlStr, err := renderEventXML(hEvent)
		if err != nil {
			sub.src.Log.Warn("doors: render event failed", "channel", sub.channel, "err", err)
			return 0
		}
		// Never block the OS callback thread: drop on overflow and let the
		// aggregation window cover the loss.
		select {
		case sub.src.rendered <- xmlStr:
		default:
			sub.src.Log.Warn("doors: render queue full, event dropped", "channel", sub.channel)
		}
	}
	return 0
}

// subscribeCallbackAddr is the uintptr form of the Go callback for wevtapi.
var subscribeCallbackAddr = syscall.NewCallback(subscribeCallback)

// The subscription registry maps OS-provided context ids back to their Go
// payload, avoiding pointer round-trips across the syscall boundary.
var (
	subMu       sync.Mutex
	subRegistry = make(map[uintptr]*subContext)
)

// renderEventXML renders one event handle to its XML string.
func renderEventXML(hEvent uintptr) (string, error) {
	var used, propCount uint32
	ret, _, callErr := procEvtRender.Call(
		0,
		hEvent,
		evtRenderEventXml,
		0, 0,
		uintptr(unsafe.Pointer(&used)),
		uintptr(unsafe.Pointer(&propCount)),
	)
	if ret == 0 && callErr != errInsufficientBuffer {
		return "", fmt.Errorf("EvtRender size: %w", callErr)
	}
	buf := make([]uint16, used/2)
	ret, _, callErr = procEvtRender.Call(
		0,
		hEvent,
		evtRenderEventXml,
		uintptr(used),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&used)),
		uintptr(unsafe.Pointer(&propCount)),
	)
	if ret == 0 {
		return "", fmt.Errorf("EvtRender: %w", callErr)
	}
	return utf16ToString(buf[:used/2]), nil
}

// utf16ToString trims at the first NUL and converts.
func utf16ToString(s []uint16) string {
	for i, v := range s {
		if v == 0 {
			s = s[:i]
			break
		}
	}
	return string(utf16.Decode(s))
}

// QueryEventsXML runs a one-shot historical query against a channel and
// returns the matching events rendered as XML strings, newest first (the
// native order of EvtNext over a channel query). Unlike EvtSubscribe it
// reads past events, which needs no subscription rights: the System channel
// is readable by an ordinary user. The query is an XPath over the event
// schema, e.g. "*[System[(EventID=6006)]]".
func QueryEventsXML(channel, query string, limit uint32) ([]string, error) {
	if limit == 0 {
		return nil, nil
	}
	chPtr, err := syscall.UTF16PtrFromString(channel)
	if err != nil {
		return nil, fmt.Errorf("EvtQuery: bad channel name: %w", err)
	}
	qPtr, err := syscall.UTF16PtrFromString(query)
	if err != nil {
		return nil, fmt.Errorf("EvtQuery: bad query: %w", err)
	}
	h, _, callErr := procEvtQuery.Call(
		0, // local session
		uintptr(unsafe.Pointer(chPtr)),
		uintptr(unsafe.Pointer(qPtr)),
		evtQueryChannelPath,
	)
	if h == 0 {
		return nil, fmt.Errorf("EvtQuery %s: %w", channel, callErr)
	}
	defer procEvtClose.Call(h)

	var xmls []string
	buf := make([]uintptr, limit)
	for len(xmls) < int(limit) {
		size := uint32(int(limit) - len(xmls))
		var returned uint32
		ret, _, callErr := procEvtNext.Call(
			h,
			uintptr(size),
			uintptr(unsafe.Pointer(&buf[0])),
			0, 0,
			uintptr(unsafe.Pointer(&returned)),
		)
		if ret == 0 && returned == 0 {
			if isEvtNoMore(callErr) {
				break // clean end of stream
			}
			return xmls, fmt.Errorf("EvtNext: %w", callErr)
		}
		for _, hEvent := range buf[:returned] {
			xmlStr, err := renderEventXML(hEvent)
			procEvtClose.Call(hEvent)
			if err != nil {
				return xmls, fmt.Errorf("render queried event: %w", err)
			}
			xmls = append(xmls, xmlStr)
		}
	}
	return xmls, nil
}

// parseLoop converts rendered XML into agent events until ctx is done.
func (s *WinSource) parseLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case xmlStr := <-s.rendered:
			id, at, data, err := parseWindowsEventXML([]byte(xmlStr))
			if err != nil {
				s.Log.Warn("doors: event XML skipped", "err", err)
				continue
			}
			ev, ok := windowsEventToEvent(id, at, data, s.Machine)
			if !ok {
				// Matched the subscription but not the watch list (e.g.
				// 4624 with a non-remote logon type); kept for parser
				// diagnostics, never a silent drop.
				s.Log.Debug("doors: unmatched windows event", "id", id)
				continue
			}
			select {
			case s.Events <- ev:
			case <-ctx.Done():
				return
			}
		}
	}
}
