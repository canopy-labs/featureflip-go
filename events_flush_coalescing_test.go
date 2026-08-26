package featureflip

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A second flush must not open its own drain loop while one is already running.
//
// Two concurrent drains mean two request streams against the endpoint the
// backoff gate exists to protect, and — the sharper problem — a success in one
// clears the gate a failure in the other has just armed (#2477).
//
// The only externally visible evidence of a second drain is a second request
// arriving while the first is still unanswered, so the server parks its first
// request and records the greatest number ever in flight at once.
func TestEventProcessor_ConcurrentFlushRunsOnlyOneDrain(t *testing.T) {
	var inFlight, peak atomic.Int32
	gate := make(chan struct{})
	firstArrived := make(chan struct{})
	var arriveOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		depth := inFlight.Add(1)
		for {
			old := peak.Load()
			if depth <= old || peak.CompareAndSwap(old, depth) {
				break
			}
		}
		defer inFlight.Add(-1)

		held := false
		arriveOnce.Do(func() {
			held = true
			close(firstArrived)
		})
		if held {
			<-gate
		}

		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	// Batch size 1 so the buffer below needs six round-trips to drain: plenty of
	// room for a second loop to interleave if one is allowed to start.
	ep := newEventProcessor(hc, 1, time.Hour)
	// Seeded directly rather than through enqueue: at this batch size every
	// enqueue would fire the size trigger and start a drain of its own, so the
	// test would be parked in setup before it had started either flush.
	ep.mu.Lock()
	for i := 0; i < 6; i++ {
		ep.buf = append(ep.buf, evt("flag"))
	}
	ep.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ep.flush()
	}()

	// The first request is parked inside the handler, so the drain loop is
	// provably mid-flight and anything arriving next came from a second one.
	<-firstArrived

	var released atomic.Bool
	var secondSawRelease atomic.Bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		ep.flush()
		secondSawRelease.Store(released.Load())
	}()

	// Room for the second caller to misbehave: uncoalesced it slices a batch off
	// the front and posts it, which the peak counter catches.
	time.Sleep(100 * time.Millisecond)

	released.Store(true)
	close(gate)
	wg.Wait()

	if got := peak.Load(); got != 1 {
		t.Fatalf("peak concurrent event requests = %d, want 1 — a second drain loop ran", got)
	}
	if !secondSawRelease.Load() {
		t.Fatal("the second flush returned before the in-flight drain finished; a caller that asked for a flush must wait for it")
	}
	if got := bufferedKeys(ep); len(got) != 0 {
		t.Fatalf("buffer still holds %v after both flushes returned", got)
	}
}

// stop() must never be the call that gets coalesced away: it is the last drain
// there will ever be, so returning early would discard the buffer unsent.
func TestEventProcessor_StopDrainsEvenWhileAFlushIsInFlight(t *testing.T) {
	var accepted atomic.Int32
	gate := make(chan struct{})
	firstArrived := make(chan struct{})
	var arriveOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		held := false
		arriveOnce.Do(func() {
			held = true
			close(firstArrived)
		})
		if held {
			<-gate
		}
		accepted.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 1, time.Hour)
	// Seeded directly, for the same reason as above.
	ep.mu.Lock()
	ep.buf = append(ep.buf, evt("flag-1"), evt("flag-2"))
	ep.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ep.flush()
	}()
	<-firstArrived

	// Release as stop() runs, so stop() genuinely overlaps the in-flight drain
	// rather than waiting it out first.
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(gate)
	}()

	ep.stop()
	wg.Wait()

	if got := accepted.Load(); got != 2 {
		t.Fatalf("server accepted %d event request(s), want 2 — stop() lost events to coalescing", got)
	}
}
