package featureflip

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreaming_ReceivesFlagUpdate(t *testing.T) {
	// Mock the flag endpoint that the stream handler will call after receiving an event.
	flagServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sdk/flags/my-flag":
			flag := flagDTO{Key: "my-flag", Version: 2, Enabled: true, Type: "Boolean"}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(flag)
		case "/v1/sdk/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming not supported", http.StatusInternalServerError)
				return
			}

			// Send a flag-updated event.
			evt := streamEvent{Key: "my-flag", Version: 2}
			data, _ := json.Marshal(evt)
			fmt.Fprintf(w, "event: flag.updated\n")
			fmt.Fprintf(w, "data: %s\n", string(data))
			fmt.Fprintf(w, "\n")
			flusher.Flush()

			// Keep the connection open briefly so the client can process the event.
			time.Sleep(200 * time.Millisecond)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer flagServer.Close()

	cfg := defaultConfig()
	cfg.baseURL = flagServer.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()

	var updatedKey string
	var mu sync.Mutex
	onUpdate := func(key string) {
		mu.Lock()
		updatedKey = key
		mu.Unlock()
	}

	ss := newStreamSource(hc, s, onUpdate)
	ss.reconnectDelay = 50 * time.Millisecond

	go ss.run()
	defer ss.stop()

	// Wait for the event to be processed.
	time.Sleep(500 * time.Millisecond)

	// Verify the flag was stored.
	flag, ok := s.getFlag("my-flag")
	if !ok {
		t.Fatal("my-flag should be in the store after SSE update")
	}
	if flag.Version != 2 {
		t.Errorf("flag version = %d, want 2", flag.Version)
	}
	if !flag.Enabled {
		t.Error("flag should be enabled")
	}

	// Verify the callback was called.
	mu.Lock()
	key := updatedKey
	mu.Unlock()
	if key != "my-flag" {
		t.Errorf("onUpdate key = %q, want my-flag", key)
	}
}

func TestStreaming_SegmentChange(t *testing.T) {
	flagServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sdk/flags":
			resp := getFlagsResponse{
				Environment: "test",
				Version:     1,
				Flags:       []flagDTO{{Key: "flag-1", Version: 1, Enabled: true}},
				Segments:    []segmentDTO{{Key: "seg-1", Version: 1}},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		case "/v1/sdk/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming not supported", http.StatusInternalServerError)
				return
			}

			fmt.Fprintf(w, "event: segment.updated\n")
			fmt.Fprintf(w, "data: {}\n")
			fmt.Fprintf(w, "\n")
			flusher.Flush()

			time.Sleep(200 * time.Millisecond)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer flagServer.Close()

	cfg := defaultConfig()
	cfg.baseURL = flagServer.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()

	ss := newStreamSource(hc, s, nil)
	ss.reconnectDelay = 50 * time.Millisecond

	go ss.run()
	defer ss.stop()

	time.Sleep(500 * time.Millisecond)

	// Verify all flags and segments were loaded.
	_, ok := s.getFlag("flag-1")
	if !ok {
		t.Error("flag-1 should be in the store after segment-change")
	}
	_, ok = s.getSegment("seg-1")
	if !ok {
		t.Error("seg-1 should be in the store after segment-change")
	}
}

func TestStreaming_ReconnectsOnClose(t *testing.T) {
	var connections atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sdk/stream" {
			connections.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			// Immediately close by returning — this simulates a server disconnect.
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()

	ss := newStreamSource(hc, s, nil)
	ss.reconnectDelay = 100 * time.Millisecond

	go ss.run()

	// Wait for at least 2 reconnect cycles.
	time.Sleep(350 * time.Millisecond)

	ss.stop()

	c := connections.Load()
	if c < 2 {
		t.Errorf("expected at least 2 connection attempts, got %d", c)
	}
}

func TestStreaming_SkipsEmptyKey(t *testing.T) {
	var flagFetches atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/sdk/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming not supported", http.StatusInternalServerError)
				return
			}

			// Send event with empty key.
			fmt.Fprintf(w, "event: flag.updated\n")
			fmt.Fprintf(w, "data: {\"key\":\"\",\"version\":1}\n")
			fmt.Fprintf(w, "\n")
			flusher.Flush()

			time.Sleep(200 * time.Millisecond)
		case r.URL.Path == "/v1/sdk/flags/" || len(r.URL.Path) > len("/v1/sdk/flags/"):
			flagFetches.Add(1)
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()

	ss := newStreamSource(hc, s, nil)
	ss.reconnectDelay = 50 * time.Millisecond

	go ss.run()
	time.Sleep(300 * time.Millisecond)
	ss.stop()

	if fetches := flagFetches.Load(); fetches != 0 {
		t.Errorf("expected 0 flag fetches for empty key, got %d", fetches)
	}
}

func TestStreaming_FlagDeleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sdk/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming not supported", http.StatusInternalServerError)
				return
			}
			evt := streamEvent{Key: "delete-me", Version: 1}
			data, _ := json.Marshal(evt)
			fmt.Fprintf(w, "event: flag.deleted\n")
			fmt.Fprintf(w, "data: %s\n", string(data))
			fmt.Fprintf(w, "\n")
			flusher.Flush()
			time.Sleep(200 * time.Millisecond)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()
	s.setFlag(flagDTO{Key: "delete-me", Version: 1, Enabled: true, Type: "Boolean"})

	var updatedKey string
	var mu sync.Mutex
	onUpdate := func(key string) {
		mu.Lock()
		updatedKey = key
		mu.Unlock()
	}

	ss := newStreamSource(hc, s, onUpdate)
	ss.reconnectDelay = 50 * time.Millisecond
	go ss.run()
	defer ss.stop()

	time.Sleep(500 * time.Millisecond)

	_, ok := s.getFlag("delete-me")
	if ok {
		t.Error("delete-me should be removed from store after flag.deleted")
	}

	mu.Lock()
	key := updatedKey
	mu.Unlock()
	if key != "delete-me" {
		t.Errorf("onUpdate key = %q, want delete-me", key)
	}
}

func TestStreaming_StopCancelsContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sdk/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				return
			}
			// Keep the connection alive until client disconnects.
			for {
				_, err := fmt.Fprintf(w, ": keep-alive\n\n")
				if err != nil {
					return
				}
				flusher.Flush()
				time.Sleep(50 * time.Millisecond)
			}
		}
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()

	ss := newStreamSource(hc, s, nil)

	done := make(chan struct{})
	go func() {
		ss.run()
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	ss.stop()

	select {
	case <-done:
		// Good — run() returned.
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not return after stop()")
	}
}

func TestStreaming_401RetriesInsteadOfGivingUpPermanently(t *testing.T) {
	// Regression guard for the LaunchDarkly failure mode: a 401 during an
	// outage (auth-proxy blip / Management API down) must NOT be terminal.
	var connections atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sdk/stream" {
			connections.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("unauthorized"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("bad-key", cfg)
	s := newStore()

	ss := newStreamSource(hc, s, nil)
	ss.reconnectDelay = 20 * time.Millisecond

	go ss.run()
	time.Sleep(300 * time.Millisecond)
	ss.stop()

	// Must keep retrying, not stop after the first attempt (old buggy behavior).
	if c := connections.Load(); c < 2 {
		t.Errorf("expected repeated reconnect attempts on 401, got %d", c)
	}
}

func TestStreaming_429RetriesInsteadOfPermanentFailure(t *testing.T) {
	var connections atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sdk/stream" {
			connections.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()

	ss := newStreamSource(hc, s, nil)
	ss.reconnectDelay = 50 * time.Millisecond

	go ss.run()

	time.Sleep(300 * time.Millisecond)
	ss.stop()

	// 429 is transient — should retry, not treat as permanent.
	c := connections.Load()
	if c < 2 {
		t.Errorf("expected at least 2 connection attempts for 429, got %d", c)
	}
}

func TestStreaming_ServerErrorRetriesWithBackoff(t *testing.T) {
	var connections atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sdk/stream" {
			connections.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()

	ss := newStreamSource(hc, s, nil)
	ss.reconnectDelay = 50 * time.Millisecond

	go ss.run()

	// Wait for retries — should still reconnect on 5xx.
	time.Sleep(300 * time.Millisecond)
	ss.stop()

	c := connections.Load()
	if c < 2 {
		t.Errorf("expected at least 2 connection attempts for 500, got %d", c)
	}
}

func TestStreaming_FallsBackToPollingWhenStreamStaysDown(t *testing.T) {
	// Stream endpoint always fails; the flags endpoint works. After the
	// fallback threshold, polling must kick in and populate the store.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sdk/stream":
			w.WriteHeader(http.StatusInternalServerError)
		case "/v1/sdk/flags":
			resp := getFlagsResponse{
				Environment: "test",
				Version:     1,
				Flags:       []flagDTO{{Key: "poll-flag", Version: 1, Enabled: true, Type: "Boolean"}},
				Segments:    []segmentDTO{},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()

	ss := newStreamSource(hc, s, nil)
	ss.reconnectDelay = 20 * time.Millisecond
	ss.fallbackThreshold = 2

	go ss.run()
	defer ss.stop()

	// Give it enough time to exceed the threshold and run a poll.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := s.getFlag("poll-flag"); ok {
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("polling fallback never populated the store after stream stayed down")
}

func TestStreaming_SyncSnapshotLargerThan64KiB(t *testing.T) {
	// Regression (critical): the connect-time `sync` snapshot is emitted by the
	// eval-api as a single `data:` line. bufio.Scanner's default 64 KiB token cap
	// makes scanner.Scan() fail with ErrTooLong on any environment whose config
	// serializes past that ceiling — and because the snapshot is the FIRST event,
	// no subsequent ping or delta is ever read either, so the store freezes on the
	// one-time init fetch forever. The snapshot must be read and applied regardless
	// of size.
	const flagCount = 800
	flags := make([]flagDTO, flagCount)
	for i := range flags {
		flags[i] = flagDTO{
			Key:     fmt.Sprintf("flag-%04d-with-a-reasonably-long-key-to-pad-the-payload", i),
			Version: 1,
			Enabled: true,
			Type:    "Boolean",
		}
	}
	snapshot := getFlagsResponse{Environment: "test", Version: 1, Flags: flags}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if len(data) <= 64*1024 {
		t.Fatalf("test snapshot must exceed 64 KiB to exercise the scanner cap, got %d bytes", len(data))
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sdk/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "no flush", http.StatusInternalServerError)
				return
			}
			fmt.Fprintf(w, "event: sync\n")
			fmt.Fprintf(w, "data: %s\n\n", string(data))
			flusher.Flush()
			time.Sleep(200 * time.Millisecond)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()

	ss := newStreamSource(hc, s, nil)
	ss.reconnectDelay = 50 * time.Millisecond
	go ss.run()
	defer ss.stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := s.getFlag("flag-0000-with-a-reasonably-long-key-to-pad-the-payload"); ok {
			return // success — the oversized snapshot line was read and applied
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("large sync snapshot (>64 KiB) was never applied — scanner choked on the oversized line")
}

func TestStreaming_ArmsPollingFallbackWhenStreamDeliversNoEvents(t *testing.T) {
	// Regression (M2): a stream that returns 200 but never delivers a single event
	// (a dead proxy, or a snapshot too large to read) must NOT count as a healthy
	// recovery. Otherwise consecutiveFailures resets on every loop, the polling
	// fallback never arms, and a permanently-silent stream masks a frozen store
	// forever. The flags endpoint works, so once the fallback correctly arms the
	// store gets populated.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sdk/stream":
			// 200 OK, but deliver nothing and close immediately.
			w.Header().Set("Content-Type", "text/event-stream")
			return
		case "/v1/sdk/flags":
			resp := getFlagsResponse{
				Environment: "test",
				Version:     1,
				Flags:       []flagDTO{{Key: "poll-flag", Version: 1, Enabled: true, Type: "Boolean"}},
				Segments:    []segmentDTO{},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()

	ss := newStreamSource(hc, s, nil)
	ss.reconnectDelay = 20 * time.Millisecond
	ss.fallbackThreshold = 2

	go ss.run()
	defer ss.stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := s.getFlag("poll-flag"); ok {
			return // fallback armed and populated the store
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("polling fallback never armed for a stream that reached 200 but delivered no events")
}

func TestStreaming_SyncReplacesStore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sdk/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "no flush", http.StatusInternalServerError)
				return
			}
			// A connect-time snapshot containing only flag-new.
			resp := getFlagsResponse{
				Environment: "test",
				Version:     5,
				Flags:       []flagDTO{{Key: "flag-new", Version: 5, Enabled: true, Type: "Boolean"}},
				Segments:    []segmentDTO{},
			}
			data, _ := json.Marshal(resp)
			fmt.Fprintf(w, "event: sync\n")
			fmt.Fprintf(w, "data: %s\n\n", string(data))
			flusher.Flush()
			time.Sleep(200 * time.Millisecond)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()
	// Pre-seed a stale flag that the snapshot does NOT contain.
	s.setFlag(flagDTO{Key: "flag-stale", Version: 1, Enabled: true, Type: "Boolean"})

	var mu sync.Mutex
	var updateKey string
	ss := newStreamSource(hc, s, func(k string) { mu.Lock(); updateKey = k; mu.Unlock() })
	ss.reconnectDelay = 50 * time.Millisecond

	go ss.run()
	defer ss.stop()
	time.Sleep(300 * time.Millisecond)

	if _, ok := s.getFlag("flag-new"); !ok {
		t.Error("flag-new should be present after sync snapshot")
	}
	if _, ok := s.getFlag("flag-stale"); ok {
		t.Error("flag-stale should be GONE after a full-replace sync (not merged)")
	}
	mu.Lock()
	k := updateKey
	mu.Unlock()
	if k != "" {
		t.Errorf("onUpdate key = %q, want \"\" (full refresh)", k)
	}
}

func TestStreaming_ConcatenatesMultiLineData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sdk/flags/my-flag":
			flag := flagDTO{Key: "my-flag", Version: 2, Enabled: true, Type: "Boolean"}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(flag)
		case "/v1/sdk/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming not supported", http.StatusInternalServerError)
				return
			}
			// Split one JSON payload across TWO data: lines. Per the SSE spec these
			// must be joined with "\n" — the concatenation is valid JSON, but the
			// last line alone ("version":2}) is not, so an overwrite would drop it.
			fmt.Fprintf(w, "event: flag.updated\n")
			fmt.Fprintf(w, "data: {\"key\":\"my-flag\",\n")
			fmt.Fprintf(w, "data: \"version\":2}\n")
			fmt.Fprintf(w, "\n")
			flusher.Flush()
			time.Sleep(200 * time.Millisecond)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()

	ss := newStreamSource(hc, s, nil)
	ss.reconnectDelay = 50 * time.Millisecond
	go ss.run()
	defer ss.stop()

	time.Sleep(500 * time.Millisecond)

	flag, ok := s.getFlag("my-flag")
	if !ok {
		t.Fatal("my-flag should be stored — the multi-line data: payload must be joined with \\n")
	}
	if flag.Version != 2 {
		t.Errorf("flag version = %d, want 2", flag.Version)
	}
}

func TestBackoffDelay_DoesNotOverflowToNegative(t *testing.T) {
	// Simulate maxReconnectDelay wired high enough that the exponential doubling
	// would overflow time.Duration (int64) before the cap check fires. The delay
	// must never come back <= 0 (which would busy-loop the reconnect).
	ss := &streamSource{
		reconnectDelay:    1 << 50,
		maxReconnectDelay: time.Duration(math.MaxInt64),
	}
	for failures := 2; failures <= 40; failures++ {
		d := ss.backoffDelay(failures)
		if d <= 0 {
			t.Fatalf("backoffDelay(%d) = %v, want a positive duration (overflow guard failed)", failures, d)
		}
		if d > ss.maxReconnectDelay {
			t.Fatalf("backoffDelay(%d) = %v exceeds max %v", failures, d, ss.maxReconnectDelay)
		}
	}
}
