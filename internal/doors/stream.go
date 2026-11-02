package doors

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"siren/internal/event"
)

// lineStream yields log lines one by one. Next returns io.EOF at a normal
// stream end; any other error marks the stream as failed. Close releases
// every resource and must be idempotent.
type lineStream interface {
	Next() (string, error)
	Close() error
}

// streamFactory opens a new stream bound to ctx: when ctx is done the
// stream must terminate on its own. It is called again after a stream ends
// or fails, which implements follow-mode restart.
type streamFactory func(ctx context.Context) (lineStream, error)

// Restart backoff for dying streams: doubling from 1s, capped at 30s, reset
// after a stream that yielded at least one line.
const (
	restartBackoffBase = time.Second
	restartBackoffMax  = 30 * time.Second
)

// runStreams follows a log source forever: opens streams via factory, feeds
// every line to onLine, and restarts with backoff when a stream ends or
// fails. Returns when ctx is done. onLine is called sequentially; it must
// not block for long.
func runStreams(ctx context.Context, log *slog.Logger, factory streamFactory, onLine func(string)) {
	backoff := restartBackoffBase
	for {
		if ctx.Err() != nil {
			return
		}
		st, err := factory(ctx)
		if err != nil {
			log.Warn("doors: open stream failed", "err", err)
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		got := false
		for {
			line, err := st.Next()
			if err != nil {
				_ = st.Close()
				if ctx.Err() != nil {
					return
				}
				if !got {
					log.Warn("doors: stream failed before yielding lines", "err", err)
				} else {
					log.Info("doors: stream ended, restarting", "err", err)
				}
				break
			}
			got = true
			backoff = restartBackoffBase
			onLine(line)
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

// nextBackoff doubles d up to restartBackoffMax.
func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > restartBackoffMax {
		return restartBackoffMax
	}
	return d
}

// sleepCtx waits for d or until ctx is done; reports whether the full
// duration elapsed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// journalLineToEvent converts one `journalctl -o json` record into an agent
// event. ok reports whether the record is an SSH authentication fact.
// Unrecognized sshd messages are skipped here; the caller logs raw records
// it could not decode for diagnostics.
func journalLineToEvent(line, machine string) (ev event.Event, ok bool, err error) {
	var rec struct {
		Message   string `json:"MESSAGE"`
		Timestamp string `json:"__REALTIME_TIMESTAMP"` // microseconds as a string
	}
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		return event.Event{}, false, fmt.Errorf("doors: decode journal record: %w", err)
	}
	kind, account, ip, port, matched := ParseSSHMessage(rec.Message)
	if !matched {
		return event.Event{}, false, nil
	}
	at := time.Now()
	if usec, err := strconv.ParseInt(rec.Timestamp, 10, 64); err == nil && usec > 0 {
		at = time.UnixMicro(usec)
	}
	return AuthEvent(kind, "ssh", machine, account, ip, port, at), true, nil
}
