package featureflip

import (
	"encoding/json"
	"fmt"
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

func TestStreaming_NonOKStatusReturnsError(t *testing.T) {
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
	ss.reconnectDelay = 50 * time.Millisecond

	go ss.run()

	// Wait enough for multiple potential retries.
	time.Sleep(300 * time.Millisecond)
	ss.stop()

	// With a 401, the SDK should stop retrying after the first attempt.
	c := connections.Load()
	if c != 1 {
		t.Errorf("expected exactly 1 connection attempt for 401, got %d", c)
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
