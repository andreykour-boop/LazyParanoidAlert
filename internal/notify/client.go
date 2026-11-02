package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// apiBase is the public Telegram Bot API endpoint; overridden in tests.
const apiBase = "https://api.telegram.org"

// HTTP call bounds: Telegram answers within seconds; a stuck connection
// must not hold the delivery queue longer than the timeout.
const (
	sendTimeout    = 10 * time.Second
	maxBodyReading = 4 * 1024
)

// APIError is a structured Telegram API failure; RetryAfter carries the
// server's requested delay for HTTP 429 responses.
type APIError struct {
	Code       int
	RetryAfter time.Duration
	Body       string
}

func (e *APIError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("telegram: HTTP %d, retry after %s: %s", e.Code, e.RetryAfter, e.Body)
	}
	return fmt.Sprintf("telegram: HTTP %d: %s", e.Code, e.Body)
}

// Client sends messages to one Telegram chat.
type Client struct {
	Token  string
	ChatID string
	// APIURL overrides apiBase; empty means the public endpoint.
	APIURL string
	// HTTP is the transport; nil means a default client with timeouts.
	HTTP *http.Client
	Log  *slog.Logger
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: sendTimeout}
}

func (c *Client) logger() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func (c *Client) base() string {
	if c.APIURL != "" {
		return strings.TrimRight(c.APIURL, "/")
	}
	return apiBase
}

// SendMessage posts one text message; it performs a single attempt, the
// retry policy lives in the queue (Notifier).
func (c *Client) SendMessage(ctx context.Context, text string) error {
	payload, err := json.Marshal(map[string]string{
		"chat_id": c.ChatID,
		"text":    text,
	})
	if err != nil {
		return fmt.Errorf("telegram: encode payload: %w", err)
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", c.base(), c.Token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("telegram: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("telegram: post: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyReading))
	if resp.StatusCode != http.StatusOK {
		return &APIError{
			Code:       resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			Body:       strings.TrimSpace(string(body)),
		}
	}
	var ack struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(body, &ack); err != nil {
		return fmt.Errorf("telegram: decode response: %w", err)
	}
	if !ack.OK {
		return errors.New("telegram: API answered ok=false")
	}
	return nil
}

// parseRetryAfter reads the Retry-After header (seconds); Telegram also
// returns retry_after in the JSON body, which the queue reads from the
// APIError body when the header is missing.
func parseRetryAfter(header string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// retryAfterFromBody extracts parameters.retry_after from a 429 response
// body, complementing parseRetryAfter.
func retryAfterFromBody(body string) time.Duration {
	var parsed struct {
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return 0
	}
	if parsed.Parameters.RetryAfter < 0 {
		return 0
	}
	return time.Duration(parsed.Parameters.RetryAfter) * time.Second
}

// RetryAfter returns the delay requested by the server for a rate-limit
// response; zero when the error carries none.
func RetryAfter(err error) time.Duration {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusTooManyRequests {
		return 0
	}
	if apiErr.RetryAfter > 0 {
		return apiErr.RetryAfter
	}
	return retryAfterFromBody(apiErr.Body)
}

// Retryable reports whether the delivery should be attempted again:
// rate limits, server faults and transport errors are transient; other
// client errors (bad token, wrong chat) would fail forever.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == http.StatusTooManyRequests || apiErr.Code >= 500
	}
	return true // transport errors: timeouts, refused connections
}
