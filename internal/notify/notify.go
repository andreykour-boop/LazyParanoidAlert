package notify

import (
	"context"
	"log/slog"
	"time"

	"siren/internal/aggregate"
)

// Delivery backoff: doubling from the base delay, capped at the maximum.
// The pending queue is bounded: beyond queueCap the oldest formatted
// message is dropped, because an unbounded queue is a memory leak.
const (
	defaultBackoffBase = time.Second
	defaultBackoffMax  = 5 * time.Minute
	queueCap           = 5000
	// finalFlushTimeout bounds one last-chance send on shutdown.
	finalFlushTimeout = 2 * time.Second
	// finalFlushLimit caps how many pending messages the shutdown flush
	// attempts; the rest are logged as lost.
	finalFlushLimit = 10
)

// Notifier drains notices from the aggregation layer into Telegram: one
// in-flight message, retries with growing delay on transient failures,
// and a pending queue that keeps accumulating while the backoff sleeps.
type Notifier struct {
	client *Client
	log    *slog.Logger
	fmt    Formatter

	backoffBase time.Duration
	backoffMax  time.Duration
}

// New builds a Notifier over the given client. agentIP (optional) is the
// monitored machine's address shown in every alert header.
func New(client *Client, agentIP string, log *slog.Logger) *Notifier {
	if log == nil {
		log = slog.Default()
	}
	return &Notifier{
		client:      client,
		log:         log,
		fmt:         Formatter{AgentIP: agentIP},
		backoffBase: defaultBackoffBase,
		backoffMax:  defaultBackoffMax,
	}
}

// Run delivers every notice from in until ctx is done. It returns ctx.Err();
// undelivered messages get one bounded last-chance flush on the way out.
func (n *Notifier) Run(ctx context.Context, in <-chan aggregate.Notice) error {
	return n.RunWithDirect(ctx, in, nil)
}

// RunWithDirect is Run plus a direct channel for ready-made messages
// (planned health reports and reminders): they join the same pending queue
// and share its retry/backoff, bypassing the aggregation layer so identical
// planned texts are never collapsed as repeats.
func (n *Notifier) RunWithDirect(ctx context.Context, in <-chan aggregate.Notice, direct <-chan string) error {
	var pending []string
	defer func() { n.finalFlush(&pending) }()

	delay := n.backoffBase
	for {
		if len(pending) == 0 {
			select {
			case nt := <-in:
				pending = append(pending, n.fmt.Format(nt))
			case text, ok := <-direct:
				if !ok {
					direct = nil
					continue
				}
				pending = append(pending, text)
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		err := n.client.SendMessage(ctx, pending[0])
		switch {
		case err == nil:
			pending = pending[1:]
			delay = n.backoffBase
		case Retryable(err):
			if ra := RetryAfter(err); ra > 0 {
				// The server prescribed the delay; growth continues
				// from there if the limit persists.
				delay = max(ra, n.backoffBase)
			} else {
				delay = min(delay*2, n.backoffMax)
			}
			n.log.Warn("notify: delivery failed, backing off",
				"delay", delay, "pending", len(pending), "err", err)
			if !waitDraining(ctx, delay, in, direct, &pending, n.fmt) {
				return ctx.Err()
			}
		default:
			// Permanent failure (bad token, wrong chat): retrying would
			// stall the queue forever; the message is logged and dropped.
			n.log.Error("notify: message dropped, permanent API error", "err", err)
			pending = pending[1:]
		}
	}
}

// waitDraining sleeps for d while keep accumulating incoming notices, so
// events that arrive during a backoff window are never lost. It returns
// false when ctx is done.
func waitDraining(ctx context.Context, d time.Duration, in <-chan aggregate.Notice, direct <-chan string, pending *[]string, fm Formatter) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case nt := <-in:
			*pending = append(*pending, fm.Format(nt))
			for len(*pending) > queueCap {
				*pending = (*pending)[1:]
			}
		case text, ok := <-direct:
			if !ok {
				continue
			}
			*pending = append(*pending, text)
			for len(*pending) > queueCap {
				*pending = (*pending)[1:]
			}
		}
	}
}

// finalFlush attempts one last-chance delivery of the oldest pending
// messages after ctx is done, bounded in count and time; the rest are
// logged as lost.
func (n *Notifier) finalFlush(pending *[]string) {
	if len(*pending) == 0 {
		return
	}
	sent := 0
	for i, text := range *pending {
		if i >= finalFlushLimit {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), finalFlushTimeout)
		err := n.client.SendMessage(ctx, text)
		cancel()
		if err != nil {
			n.log.Warn("notify: final flush failed", "err", err)
			break
		}
		sent++
	}
	rest := len(*pending) - sent
	if rest > 0 {
		n.log.Warn("notify: messages lost on shutdown", "count", rest)
	}
	*pending = nil
}
