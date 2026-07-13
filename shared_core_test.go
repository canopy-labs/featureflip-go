package featureflip

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestInit_FetchFailsThenSelfHeals verifies the non-terminal initial-fetch
// contract (GAP-2): a cold start while the eval-api is unreachable must not
// cache the init error and serve defaults forever. Initialization succeeds
// (degraded-but-recovering), the data source starts regardless, and once the
// service becomes reachable the already-running poller self-heals the store
// with no re-init required.
func TestInit_FetchFailsThenSelfHeals(t *testing.T) {
	var healthy atomic.Bool // false = failing initial fetch

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sdk/flags" {
			if !healthy.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			resp := getFlagsResponse{
				Environment: "test", Version: 1,
				Flags:    []flagDTO{{Key: "recovered-flag", Version: 1, Enabled: true, Type: "Boolean"}},
				Segments: []segmentDTO{},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	cfg.streaming = false // deterministic poll-based recovery
	cfg.pollInterval = 20 * time.Millisecond

	sc := newSharedCore("sdk-key", cfg)
	// Initial fetch fails, but initialization must NOT be terminal.
	if err := sc.initializeOnce(); err != nil {
		t.Fatalf("initializeOnce must be non-fatal on initial-fetch failure, got %v", err)
	}
	defer sc.release()

	if _, ok := sc.store.getFlag("recovered-flag"); ok {
		t.Fatal("flag should not be present before recovery")
	}

	// Service recovers: the already-running poller must self-heal the store.
	healthy.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := sc.store.getFlag("recovered-flag"); ok {
			return // success — recovered with no re-init
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("client did not self-heal after the eval-api recovered")
}
