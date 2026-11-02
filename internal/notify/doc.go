// Package notify delivers formatted alerts to a Telegram chat via the Bot
// API: a send queue with retry and exponential backoff on rate-limit
// responses, so queued events are never lost.
package notify
