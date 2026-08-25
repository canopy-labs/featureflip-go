package featureflip

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordingEventsServer returns an httptest server that records every batch it
// ACCEPTS and answers each request with the status the caller's status function
// returns, indexed by attempt number (1-based).
//
// A status of 0 means "kill the connection without answering", which is how the
// tests below produce a genuine transport fault rather than an HTTP answer.
func recordingEventsServer(t *testing.T, status func(attempt int) int) (*httptest.Server, func() [][]sdkEvent, func() int) {
	t.Helper()

	var mu sync.Mutex
	var batches [][]sdkEvent
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := int(attempts.Add(1))
		code := status(attempt)

		if code == 0 {
			// Hijack and close so the client sees an unexpected EOF: a transport
			// fault, which must be classified separately from an HTTP answer.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}

		body, _ := io.ReadAll(r.Body)
		var req recordEventsRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("server got unparseable body: %v", err)
		}
		// Only ACCEPTED batches are recorded — the tests below assert on what
		// actually got delivered, not on what was offered and rejected.
		if code >= 200 && code < 300 {
			mu.Lock()
			batches = append(batches, req.Events)
			mu.Unlock()
		}

		w.WriteHeader(code)
	}))
	t.Cleanup(server.Close)

	received := func() [][]sdkEvent {
		mu.Lock()
		defer mu.Unlock()
		out := make([][]sdkEvent, len(batches))
		copy(out, batches)
		return out
	}

	return server, received, func() int { return int(attempts.Load()) }
}

// bufferedKeys returns the flag keys currently sitting in the processor's
// buffer, read under the same lock the processor uses.
func bufferedKeys(ep *eventProcessor) []string {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	keys := make([]string, len(ep.buf))
	for i, e := range ep.buf {
		keys[i] = e.FlagKey
	}
	return keys
}

func keysOf(events []sdkEvent) []string {
	keys := make([]string, len(events))
	for i, e := range events {
		keys[i] = e.FlagKey
	}
	return keys
}

func equalKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func evt(key string) sdkEvent {
	return sdkEvent{Type: "Evaluation", FlagKey: key, Timestamp: "t"}
}

// A 503 is retryable: the batch must survive the failed flush and go out on the
// next one, ahead of anything enqueued in between.
func TestEventProcessor_ServerErrorKeepsBatchForNextFlush(t *testing.T) {
	server, received, _ := recordingEventsServer(t, func(attempt int) int {
		if attempt == 1 {
			return http.StatusServiceUnavailable
		}
		return http.StatusAccepted
	})

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 1000, 10*time.Second) // neither batch nor interval triggers
	ep.enqueue(evt("flag-1"))
	ep.enqueue(evt("flag-2"))

	ep.flush() // rejected with 503

	if got := bufferedKeys(ep); !equalKeys(got, []string{"flag-1", "flag-2"}) {
		t.Fatalf("after a 503 the buffer holds %v, want [flag-1 flag-2]", got)
	}

	ep.enqueue(evt("flag-3"))
	ep.flush() // accepted

	batches := received()
	if len(batches) != 1 {
		t.Fatalf("server accepted %d batches, want 1", len(batches))
	}
	if got := keysOf(batches[0]); !equalKeys(got, []string{"flag-1", "flag-2", "flag-3"}) {
		t.Errorf("re-sent batch = %v, want [flag-1 flag-2 flag-3] (re-queued events lead)", got)
	}
	if n := len(bufferedKeys(ep)); n != 0 {
		t.Errorf("buffer holds %d events after a successful flush, want 0", n)
	}
}

// A transport fault — no HTTP answer at all — is retryable for the same reason
// a 5xx is: the batch was never rejected, only undelivered.
func TestEventProcessor_NetworkErrorKeepsBatchForNextFlush(t *testing.T) {
	server, received, _ := recordingEventsServer(t, func(attempt int) int {
		if attempt == 1 {
			return 0 // connection killed mid-request
		}
		return http.StatusAccepted
	})

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 1000, 10*time.Second)
	ep.enqueue(evt("flag-1"))

	ep.flush() // transport fault

	if got := bufferedKeys(ep); !equalKeys(got, []string{"flag-1"}) {
		t.Fatalf("after a transport fault the buffer holds %v, want [flag-1]", got)
	}

	ep.flush() // accepted

	batches := received()
	if len(batches) != 1 {
		t.Fatalf("server accepted %d batches, want 1", len(batches))
	}
	if got := keysOf(batches[0]); !equalKeys(got, []string{"flag-1"}) {
		t.Errorf("re-sent batch = %v, want [flag-1]", got)
	}
}

// A 401 means the SDK key was rejected. The same batch will be rejected
// identically forever, so it is dropped rather than re-queued.
func TestEventProcessor_UnauthorizedDropsBatchWithoutRetrying(t *testing.T) {
	server, _, attempts := recordingEventsServer(t, func(int) int {
		return http.StatusUnauthorized
	})

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 1000, 10*time.Second)
	ep.enqueue(evt("flag-1"))
	ep.enqueue(evt("flag-2"))

	ep.flush()

	if n := len(bufferedKeys(ep)); n != 0 {
		t.Fatalf("buffer holds %d events after a 401, want 0 (dropped as non-retryable)", n)
	}

	ep.flush() // nothing left to send

	if got := attempts(); got != 1 {
		t.Errorf("server saw %d requests, want 1 — the rejected batch must not be retried", got)
	}
}

// A 429 is the server explicitly asking the caller to come back later.
func TestEventProcessor_TooManyRequestsKeepsBatchForNextFlush(t *testing.T) {
	server, _, _ := recordingEventsServer(t, func(int) int {
		return http.StatusTooManyRequests
	})

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 1000, 10*time.Second)
	ep.enqueue(evt("flag-1"))

	ep.flush()

	if got := bufferedKeys(ep); !equalKeys(got, []string{"flag-1"}) {
		t.Errorf("after a 429 the buffer holds %v, want [flag-1]", got)
	}
}

// Past the bound the OLDEST events are shed, so a long outage sheds the stale
// re-queued batches rather than starving the freshest analytics.
func TestEventProcessor_OverflowShedsOldestEvents(t *testing.T) {
	server, received, _ := recordingEventsServer(t, func(int) int {
		return http.StatusAccepted
	})

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	// Large batch size so enqueue never flushes — the bound is what trims here.
	ep := newEventProcessorWithBound(hc, 1000, 10*time.Second, 3)
	for _, key := range []string{"flag-1", "flag-2", "flag-3", "flag-4", "flag-5"} {
		ep.enqueue(evt(key))
	}

	if got := bufferedKeys(ep); !equalKeys(got, []string{"flag-3", "flag-4", "flag-5"}) {
		t.Fatalf("buffer holds %v, want [flag-3 flag-4 flag-5] (oldest shed)", got)
	}

	ep.flush()

	batches := received()
	if len(batches) != 1 {
		t.Fatalf("server accepted %d batches, want 1", len(batches))
	}
	if got := keysOf(batches[0]); !equalKeys(got, []string{"flag-3", "flag-4", "flag-5"}) {
		t.Errorf("sent batch = %v, want [flag-3 flag-4 flag-5]", got)
	}
}

// A re-queued batch is bounded too: it is pushed at the front, so the bound
// trims the front — the re-queued (stale) events go first.
func TestEventProcessor_RequeueRespectsTheBound(t *testing.T) {
	server, _, _ := recordingEventsServer(t, func(int) int {
		return http.StatusServiceUnavailable
	})

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessorWithBound(hc, 1000, 10*time.Second, 3)
	ep.enqueue(evt("old-1"))
	ep.enqueue(evt("old-2"))

	ep.flush() // 503 → old-1, old-2 return to the front

	ep.enqueue(evt("new-1"))
	ep.enqueue(evt("new-2"))

	if got := bufferedKeys(ep); !equalKeys(got, []string{"old-2", "new-1", "new-2"}) {
		t.Errorf("buffer holds %v, want [old-2 new-1 new-2] — the bound sheds the oldest", got)
	}
}

// Shutdown makes ONE final attempt and then lets go. Looping until the buffer
// empties would hang shutdown for as long as the endpoint stayed down.
func TestEventProcessor_StopTerminatesWhileTheEndpointIsFailing(t *testing.T) {
	server, _, attempts := recordingEventsServer(t, func(int) int {
		return http.StatusServiceUnavailable
	})

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 1000, 10*time.Second)
	ep.start()

	ep.enqueue(evt("flag-1"))
	ep.enqueue(evt("flag-2"))

	stopped := make(chan struct{})
	go func() {
		ep.stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return while the events endpoint was failing")
	}

	if got := attempts(); got != 1 {
		t.Errorf("server saw %d requests during shutdown, want exactly 1", got)
	}
	if n := len(bufferedKeys(ep)); n != 0 {
		t.Errorf("buffer holds %d events after stop, want 0 (the remainder is discarded)", n)
	}
}

// A batch that cannot be encoded will fail identically every time — caller
// metadata carrying a func value is the way to produce one — so it must be
// dropped rather than pinned at the front of the buffer forever.
func TestEventProcessor_UnencodableBatchIsDroppedNotRetried(t *testing.T) {
	server, _, attempts := recordingEventsServer(t, func(int) int {
		return http.StatusAccepted
	})

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 1000, 10*time.Second)
	ep.enqueue(sdkEvent{
		Type:      "Custom",
		FlagKey:   "poison",
		Timestamp: "t",
		Metadata:  map[string]any{"callback": func() {}},
	})

	ep.flush()

	if n := len(bufferedKeys(ep)); n != 0 {
		t.Errorf("buffer holds %d events after an encode failure, want 0", n)
	}
	if got := attempts(); got != 0 {
		t.Errorf("server saw %d requests, want 0 — the batch never reached the wire", got)
	}
}

// A re-queued batch leaves the buffer at or above batchSize, so without a
// backoff gate every subsequent enqueue would start another flush — turning a
// failing endpoint into one request per event, which is worse for the server
// than the dropping this change replaces.
func TestEventProcessor_SizeTriggerBacksOffAfterARetryableFailure(t *testing.T) {
	server, _, attempts := recordingEventsServer(t, func(int) int {
		return http.StatusServiceUnavailable
	})

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	// batchSize 1: every enqueue would otherwise trip the size trigger.
	// The long interval keeps the gate armed for the whole test.
	ep := newEventProcessor(hc, 1, 10*time.Second)
	for i := 0; i < 10; i++ {
		ep.enqueue(evt("flag-1"))
	}

	if got := attempts(); got != 1 {
		t.Errorf("server saw %d requests for 10 events, want exactly 1", got)
	}
	if n := len(bufferedKeys(ep)); n != 10 {
		t.Errorf("buffer holds %d events, want 10 — nothing was delivered", n)
	}
}

// A successful send clears the gate, so the size trigger resumes immediately
// once the endpoint recovers rather than waiting out a stale backoff.
func TestEventProcessor_SizeTriggerResumesAfterASuccessfulFlush(t *testing.T) {
	server, _, attempts := recordingEventsServer(t, func(attempt int) int {
		if attempt == 1 {
			return http.StatusServiceUnavailable
		}
		return http.StatusAccepted
	})

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 1, 10*time.Second)

	ep.enqueue(evt("flag-1")) // size trigger fires, 503, gate armed
	ep.enqueue(evt("flag-2")) // suppressed by the gate

	if got := attempts(); got != 1 {
		t.Fatalf("server saw %d requests before the manual flush, want 1", got)
	}

	// An explicit flush is never gated. It drains a batch per request, so the
	// two backlogged events go out as two requests, and both succeed.
	ep.flush()

	ep.enqueue(evt("flag-3")) // gate cleared, size trigger fires again

	if got := attempts(); got != 4 {
		t.Errorf("server saw %d requests, want 4 (503, then two batches of the manual flush, then the resumed size trigger)", got)
	}
	if n := len(bufferedKeys(ep)); n != 0 {
		t.Errorf("buffer holds %d events, want 0", n)
	}
}

// The gate is only armed once a flush has already FAILED, and the size trigger
// fires again long before the first round-trip returns — so concurrent enqueues
// need a latch as well, or each one starts its own flush.
func TestEventProcessor_SizeTriggerDoesNotStartConcurrentFlushes(t *testing.T) {
	release := make(chan struct{})
	var inFlight atomic.Int32
	var maxConcurrent atomic.Int32
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		n := inFlight.Add(1)
		for {
			seen := maxConcurrent.Load()
			if n <= seen || maxConcurrent.CompareAndSwap(seen, n) {
				break
			}
		}
		<-release // hold the request open so overlap is observable
		inFlight.Add(-1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 1, 10*time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ep.enqueue(evt("flag-1"))
		}()
	}

	// Let the goroutines pile up against the held request, then let it go.
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := maxConcurrent.Load(); got > 1 {
		t.Errorf("%d size-triggered flushes were in flight at once, want at most 1", got)
	}
	// The latch serializes the flushes, it does not merge them: with a batch
	// size of 1 every event is still its own request. What matters is that the
	// 20 requests happen one after another rather than 20 at once.
	if got := attempts.Load(); got != 20 {
		t.Errorf("server saw %d requests for 20 events at batch size 1, want 20", got)
	}
}

// One request per batch, not one for the whole buffer. Re-queuing is what lets
// the buffer grow far past the batch size, and a body carrying the whole
// backlog risks a 413 — which is non-retryable, so the backlog would be dropped
// by the very path added to preserve it.
func TestEventProcessor_FlushSendsAtMostOneBatchPerRequest(t *testing.T) {
	var healthy atomic.Bool
	server, received, _ := recordingEventsServer(t, func(int) int {
		if healthy.Load() {
			return http.StatusAccepted
		}
		return http.StatusServiceUnavailable
	})

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	// Batch size 2, long interval: the first pair trips the size trigger, fails
	// with a 503 and is re-queued, which arms the backoff gate — so the
	// remaining three pile up behind it into a five-event backlog.
	ep := newEventProcessor(hc, 2, 10*time.Second)
	for _, key := range []string{"flag-1", "flag-2", "flag-3", "flag-4", "flag-5"} {
		ep.enqueue(evt(key))
	}

	if got := bufferedKeys(ep); !equalKeys(got, []string{"flag-1", "flag-2", "flag-3", "flag-4", "flag-5"}) {
		t.Fatalf("backlog holds %v, want all five events", got)
	}

	healthy.Store(true)
	ep.flush()

	var delivered []string
	for i, batch := range received() {
		if len(batch) > 2 {
			t.Errorf("request %d carried %d events, want at most the batch size of 2", i+1, len(batch))
		}
		delivered = append(delivered, keysOf(batch)...)
	}

	if !equalKeys(delivered, []string{"flag-1", "flag-2", "flag-3", "flag-4", "flag-5"}) {
		t.Errorf("delivered %v, want all five events in order", delivered)
	}
	if n := len(bufferedKeys(ep)); n != 0 {
		t.Errorf("buffer holds %d events after draining the backlog, want 0", n)
	}
}

// A batch the server rejects permanently is dropped and the loop moves on: the
// buffer shrinks either way, so the loop still terminates, and one poison batch
// cannot block the backlog queued behind it.
func TestEventProcessor_NonRetryableBatchDoesNotBlockTheBacklog(t *testing.T) {
	server, received, _ := recordingEventsServer(t, func(attempt int) int {
		switch attempt {
		case 1:
			return http.StatusServiceUnavailable // builds the backlog
		case 2:
			return http.StatusBadRequest // first batch of the drain: permanently rejected
		default:
			return http.StatusAccepted
		}
	})

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 2, 10*time.Second)
	for _, key := range []string{"flag-1", "flag-2", "flag-3", "flag-4", "flag-5"} {
		ep.enqueue(evt(key))
	}

	ep.flush()

	var delivered []string
	for _, batch := range received() {
		delivered = append(delivered, keysOf(batch)...)
	}

	if !equalKeys(delivered, []string{"flag-3", "flag-4", "flag-5"}) {
		t.Errorf("delivered %v, want [flag-3 flag-4 flag-5] — only the rejected batch is lost", delivered)
	}
	if n := len(bufferedKeys(ep)); n != 0 {
		t.Errorf("buffer holds %d events, want 0", n)
	}
}
