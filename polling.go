package featureflip

import (
	"context"
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

// poll fetches all flags and segments and updates the store. Errors are
// silently ignored — the next poll will retry.
func (ps *pollSource) poll() {
	resp, err := ps.hc.getFlags()
	if err != nil {
		return
	}
	ps.store.setAll(resp.Flags, resp.Segments)
}

// stop cancels the polling loop.
func (ps *pollSource) stop() {
	ps.cancel()
}
