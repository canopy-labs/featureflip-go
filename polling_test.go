package featureflip

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPolling_FetchesPeriodically(t *testing.T) {
	var calls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		resp := getFlagsResponse{
			Environment: "test",
			Version:     1,
			Flags:       []flagDTO{{Key: "flag-1", Version: 1, Enabled: true}},
			Segments:    []segmentDTO{{Key: "seg-1", Version: 1}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()

	ps := newPollSource(hc, s, 100*time.Millisecond)

	go ps.run()

	// Wait for at least 2 interval ticks (plus the initial poll).
	time.Sleep(350 * time.Millisecond)

	ps.stop()

	c := calls.Load()
	if c < 2 {
		t.Errorf("expected at least 2 poll calls, got %d", c)
	}

	// Verify data was stored.
	flag, ok := s.getFlag("flag-1")
	if !ok {
		t.Fatal("flag-1 should be in the store")
	}
	if !flag.Enabled {
		t.Error("flag-1 should be enabled")
	}
	_, ok = s.getSegment("seg-1")
	if !ok {
		t.Error("seg-1 should be in the store")
	}
}

func TestPolling_StopsCleanly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := getFlagsResponse{
			Environment: "test",
			Version:     1,
			Flags:       []flagDTO{},
			Segments:    []segmentDTO{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()

	ps := newPollSource(hc, s, 100*time.Millisecond)

	done := make(chan struct{})
	go func() {
		ps.run()
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	ps.stop()

	select {
	case <-done:
		// Good — run() returned.
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not return after stop()")
	}
}

func TestPolling_HandlesServerErrors(t *testing.T) {
	var calls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := calls.Add(1)
		if c == 1 {
			// First call fails.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Second call succeeds.
		resp := getFlagsResponse{
			Environment: "test",
			Version:     1,
			Flags:       []flagDTO{{Key: "flag-1", Version: 1, Enabled: true}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)
	s := newStore()

	ps := newPollSource(hc, s, 100*time.Millisecond)

	go ps.run()

	// Wait for first poll (error) + one interval tick (success).
	time.Sleep(250 * time.Millisecond)

	ps.stop()

	// Should recover and have data after second poll.
	_, ok := s.getFlag("flag-1")
	if !ok {
		t.Error("flag-1 should be in the store after recovery from server error")
	}
}
