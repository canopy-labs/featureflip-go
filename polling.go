package featureflip

import (
	"context"
	"log"
	"time"
)

// pollSource periodically fetches all flag and segment configurations
// from the evaluation API and updates the store.
type pollSource struct {
	hc       *httpClient
	store    *store
	interval time.Duration
	ctx      context.Context
	cancel   context.CancelFunc
}

// newPollSource creates a new polling data source.
func newPollSource(hc *httpClient, store *store, interval time.Duration) *pollSource {
	ctx, cancel := context.WithCancel(context.Background())
	return &pollSource{
		hc:       hc,
		store:    store,
		interval: interval,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// run starts the polling loop. It polls immediately on start and then at each
// interval tick. Runs until stop() is called.
func (ps *pollSource) run() {
	ps.poll()

	ticker := time.NewTicker(ps.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ps.ctx.Done():
			return
		case <-ticker.C:
			ps.poll()
		}
	}
}

// poll fetches all flags and segments and updates the store. The store is left
// untouched on any failure, so a bad response can never partially replace good
// config — see streaming_sync_guard_test.go for why that guard is load-bearing
// (#2288).
func (ps *pollSource) poll() {
	resp, err := ps.hc.getFlags()
	if err != nil {
		// Only contract violations are announced. A transport failure is
		// self-healing — the next tick retries — and logging every blip would
		// flood the logs of a briefly-offline client. A decode failure does not
		// self-heal: the server keeps sending the same payload, so the client
		// serves stale config (or caller defaults) indefinitely. Staying silent
		// about that is how the enums-as-integers server bug went unnoticed for
		// so long (#2279); the streaming path already says so in its own words.
		if isMalformedPayload(err) {
			log.Printf("[featureflip] discarding malformed flags payload: %v", err)
		}
		return
	}
	ps.store.setAll(resp.Flags, resp.Segments)
}

// stop cancels the polling loop.
func (ps *pollSource) stop() {
	ps.cancel()
}
