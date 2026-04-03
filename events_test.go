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

func TestEventProcessor_FlushOnBatchSize(t *testing.T) {
	var mu sync.Mutex
	var received []sdkEvent

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req recordEventsRequest
		json.Unmarshal(body, &req)
		mu.Lock()
		received = append(received, req.Events...)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 5, 10*time.Second) // long interval so only batch triggers
	ep.start()
	defer ep.stop()

	for i := 0; i < 5; i++ {
		ep.enqueue(sdkEvent{Type: "Evaluation", FlagKey: "flag-1", Timestamp: "t"})
	}

	// Give a moment for flush to complete
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count != 5 {
		t.Errorf("received %d events, want 5", count)
	}
}

func TestEventProcessor_FlushOnInterval(t *testing.T) {
	var calls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 1000, 100*time.Millisecond) // large batch so only interval triggers
	ep.start()

	ep.enqueue(sdkEvent{Type: "Evaluation", FlagKey: "flag-1", Timestamp: "t"})

	// Wait for at least one interval tick to flush
	time.Sleep(300 * time.Millisecond)

	ep.stop()

	if c := calls.Load(); c < 1 {
		t.Errorf("expected at least 1 flush call, got %d", c)
	}
}

func TestEventProcessor_ManualFlush(t *testing.T) {
	var mu sync.Mutex
	var received []sdkEvent

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req recordEventsRequest
		json.Unmarshal(body, &req)
		mu.Lock()
		received = append(received, req.Events...)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 1000, 10*time.Second) // neither batch nor interval triggers
	// Don't start background goroutine — test manual flush only

	ep.enqueue(sdkEvent{Type: "Evaluation", FlagKey: "flag-1", Timestamp: "t"})
	ep.enqueue(sdkEvent{Type: "Evaluation", FlagKey: "flag-2", Timestamp: "t"})

	ep.flush()

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count != 2 {
		t.Errorf("received %d events, want 2", count)
	}
}

func TestEventProcessor_StopFlushesRemaining(t *testing.T) {
	var mu sync.Mutex
	var received []sdkEvent

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req recordEventsRequest
		json.Unmarshal(body, &req)
		mu.Lock()
		received = append(received, req.Events...)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 1000, 10*time.Second) // large batch and long interval
	ep.start()

	ep.enqueue(sdkEvent{Type: "Evaluation", FlagKey: "flag-1", Timestamp: "t"})

	// Stop should flush remaining events
	ep.stop()

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count != 1 {
		t.Errorf("received %d events after stop, want 1", count)
	}
}

func TestEventProcessor_NilHTTPClient(t *testing.T) {
	ep := newEventProcessor(nil, 5, time.Second)

	// Should not panic
	ep.enqueue(sdkEvent{Type: "Evaluation", FlagKey: "flag-1", Timestamp: "t"})
	ep.flush()
	ep.stop()
}

func TestEventProcessor_EmptyFlush(t *testing.T) {
	var calls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	ep := newEventProcessor(hc, 100, 10*time.Second)

	// Flush with no events should not make HTTP call
	ep.flush()

	if c := calls.Load(); c != 0 {
		t.Errorf("flush with no events made %d calls, want 0", c)
	}
}
