package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"siren/internal/aggregate"
	"siren/internal/event"
	"siren/internal/logsetup"
)

// stubAPI collects sendMessage payloads and scripts per-request answers.
type stubAPI struct {
	mu       sync.Mutex
	bodies   []string
	statuses []int // status per request; last value repeats
	retryAft []string
}

func (s *stubAPI) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func (s *stubAPI) text(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[i]
}

func (s *stubAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var payload struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	_ = json.Unmarshal(body, &payload)

	s.mu.Lock()
	s.bodies = append(s.bodies, payload.Text)
	idx := len(s.bodies) - 1
	var status int
	var retryAfter string
	if idx < len(s.statuses) {
		status = s.statuses[idx]
		if idx < len(s.retryAft) {
			retryAfter = s.retryAft[idx]
		}
	} else {
		status = s.statuses[len(s.statuses)-1]
	}
	s.mu.Unlock()

	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func newTestNotifier(t *testing.T, statuses []int, retryAft []string) (*Notifier, *stubAPI) {
	t.Helper()
	api := &stubAPI{statuses: statuses, retryAft: retryAft}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	client := &Client{
		Token:  "test-token",
		ChatID: "42",
		APIURL: server.URL,
		HTTP:   server.Client(),
		Log:    logsetup.Discard(),
	}
	n := New(client, "192.0.2.10", logsetup.Discard())
	n.backoffBase = 20 * time.Millisecond
	n.backoffMax = 80 * time.Millisecond
	return n, api
}

func TestNotifierRecoversAfter429(t *testing.T) {
	// Two rate-limit answers with Retry-After, then success forever.
	n, api := newTestNotifier(t, []int{429, 429, 200}, []string{"1", ""})
	in := make(chan aggregate.Notice, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- n.Run(ctx, in) }()

	for i := 0; i < 3; i++ {
		in <- aggregate.Notice{
			First: baseEvent(event.KindAuthAttempt), Last: baseEvent(event.KindAuthAttempt), Count: 1,
		}
	}

	// All three messages must be delivered despite the 429s.
	deadline := time.After(5 * time.Second)
	for api.count() < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d of 3 messages delivered", api.count())
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err == nil {
		t.Error("Run must return the ctx error")
	}
	for i, want := range []string{"ПОПЫТКА ВХОДА", "ПОПЫТКА ВХОДА", "ПОПЫТКА ВХОДА"} {
		if got := api.text(i); !strings.Contains(got, want) {
			t.Errorf("message %d = %q, want %q", i, got, want)
		}
	}
}

func TestNotifierKeepsAccumulatingDuringBackoff(t *testing.T) {
	// The first request hangs in a long 429 window: notices arriving
	// during the backoff sleep must be queued, not dropped.
	n, api := newTestNotifier(t, []int{429, 200}, []string{"1"})
	in := make(chan aggregate.Notice, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = n.Run(ctx, in) }()

	in <- aggregate.Notice{First: baseEvent(event.KindTrapConnect), Count: 1}
	// More arrive while the first is stuck in backoff.
	for i := 0; i < 4; i++ {
		in <- aggregate.Notice{First: baseEvent(event.KindTrapConnect), Count: 1}
	}

	deadline := time.After(5 * time.Second)
	for api.count() < 5 {
		select {
		case <-deadline:
			t.Fatalf("only %d of 5 messages delivered: accumulation broken", api.count())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestNotifierDropsPermanentError(t *testing.T) {
	// 400 (bad chat id) is permanent: the message is dropped, the next
	// one is delivered.
	n, api := newTestNotifier(t, []int{400, 200}, nil)
	in := make(chan aggregate.Notice, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = n.Run(ctx, in) }()

	in <- aggregate.Notice{First: baseEvent(event.KindAuthLogin), Count: 1}
	in <- aggregate.Notice{First: baseEvent(event.KindAuthLogin), Count: 1}

	deadline := time.After(5 * time.Second)
	for api.count() < 2 {
		select {
		case <-deadline:
			t.Fatalf("only %d requests made", api.count())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestNotifierRetriesOn5xx(t *testing.T) {
	n, api := newTestNotifier(t, []int{500, 500, 200}, nil)
	in := make(chan aggregate.Notice, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = n.Run(ctx, in) }()

	in <- aggregate.Notice{First: baseEvent(event.KindAuthAttempt), Count: 1}

	deadline := time.After(5 * time.Second)
	for api.count() < 1 {
		select {
		case <-deadline:
			t.Fatal("no delivery")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestRetryAfterParsing(t *testing.T) {
	if got := parseRetryAfter("7"); got != 7*time.Second {
		t.Errorf("header parse = %v", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("empty header = %v", got)
	}
	if got := retryAfterFromBody(`{"ok":false,"parameters":{"retry_after":12}}`); got != 12*time.Second {
		t.Errorf("body parse = %v", got)
	}
	if got := retryAfterFromBody(`{"ok":false}`); got != 0 {
		t.Errorf("body without parameters = %v", got)
	}
}

func TestRetryableClassification(t *testing.T) {
	cases := []struct {
		code int
		want bool
	}{
		{400, false},
		{401, false},
		{403, false},
		{404, false},
		{429, true},
		{500, true},
		{502, true},
	}
	for _, tc := range cases {
		err := &APIError{Code: tc.code, Body: "x"}
		if got := Retryable(err); got != tc.want {
			t.Errorf("Retryable(%d) = %v, want %v", tc.code, got, tc.want)
		}
	}
	if !Retryable(context.DeadlineExceeded) {
		t.Error("transport errors must be retryable")
	}
	if Retryable(nil) {
		t.Error("nil error is not retryable")
	}
}

func TestRetryAfterPrefersHeader(t *testing.T) {
	err := &APIError{Code: 429, RetryAfter: 3 * time.Second, Body: `{"parameters":{"retry_after":9}}`}
	if got := RetryAfter(err); got != 3*time.Second {
		t.Errorf("RetryAfter = %v, want header value 3s", got)
	}
	err = &APIError{Code: 429, Body: `{"parameters":{"retry_after":9}}`}
	if got := RetryAfter(err); got != 9*time.Second {
		t.Errorf("RetryAfter = %v, want body value 9s", got)
	}
}
