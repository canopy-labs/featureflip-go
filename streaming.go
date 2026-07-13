package featureflip

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

// streamStatusError is returned when the SSE endpoint responds with a non-200
// status code, carrying the status code for retry decisions.
type streamStatusError struct {
	statusCode int
}

func (e *streamStatusError) Error() string {
	return fmt.Sprintf("featureflip: stream: unexpected status %d", e.statusCode)
}

// maxSSELineSize caps a single SSE line. The eval-api emits the entire config
// snapshot as one `data:` line (SdkController.WriteSnapshotDirectAsync), and that
// snapshot is the first event on connect — so bufio.Scanner's 64 KiB default cap
// silently freezes the whole stream for any environment whose config serializes
// past it. Grow the ceiling generously to fit a large environment's config while
// still bounding memory against a runaway (newline-less) line. The polling
// fallback (json.Decoder, no line limit) backstops any config that would still
// exceed this.
const maxSSELineSize = 16 * 1024 * 1024 // 16 MiB

// streamSource connects to the evaluation API's SSE stream and updates the
// store in real time when flags or segments change.
type streamSource struct {
	hc                *httpClient
	store             *store
	onUpdate          func(key string)
	ctx               context.Context
	cancel            context.CancelFunc
	reconnectDelay    time.Duration // base backoff delay
	maxReconnectDelay time.Duration // backoff ceiling
	fallbackThreshold int           // consecutive failures before polling fallback (Task 4)
	connectTimeout    time.Duration
	fallbackMu        sync.Mutex  // guards fallbackPoll (run()'s goroutine vs. stop() racing concurrently)
	fallbackPoll      *pollSource // active polling fallback, nil when none (Task 4)
}

// newStreamSource creates a new SSE stream source.
func newStreamSource(hc *httpClient, store *store, onUpdate func(key string)) *streamSource {
	ctx, cancel := context.WithCancel(context.Background())
	return &streamSource{
		hc:                hc,
		store:             store,
		onUpdate:          onUpdate,
		ctx:               ctx,
		cancel:            cancel,
		reconnectDelay:    3 * time.Second,
		maxReconnectDelay: 30 * time.Second,
		fallbackThreshold: 5,
		connectTimeout:    5 * time.Second,
	}
}

// run starts the SSE connection loop. It reconnects forever with capped
// exponential backoff and NEVER gives up while the source is alive — a 4xx
// (including 401/403) is treated as transient, not terminal, because during an
// outage it is indistinguishable from a real revocation and going dark would
// require a customer restart. After fallbackThreshold consecutive failures it
// also starts a polling fallback (see startFallbackPolling). Runs until stop().
func (ss *streamSource) run() {
	consecutiveFailures := 0
	for {
		reached, _ := ss.connect()

		select {
		case <-ss.ctx.Done():
			return
		default:
		}

		if reached {
			// The stream delivered at least one event (even if it then dropped):
			// a genuine recovery. A bare 200 that delivered nothing is NOT counted
			// here, so a silent/broken stream still arms the polling fallback.
			consecutiveFailures = 0
			ss.stopFallbackPolling()
		} else {
			consecutiveFailures++
			if consecutiveFailures >= ss.fallbackThreshold {
				ss.startFallbackPolling()
			}
		}

		select {
		case <-ss.ctx.Done():
			return
		case <-time.After(ss.backoffDelay(consecutiveFailures)):
		}
	}
}

// backoffDelay returns the base delay for a healthy reconnect (failures<=1) and
// exponentially increasing, jittered delay up to maxReconnectDelay otherwise.
func (ss *streamSource) backoffDelay(failures int) time.Duration {
	if failures <= 1 {
		return ss.reconnectDelay
	}
	d := ss.reconnectDelay
	for i := 1; i < failures; i++ {
		d *= 2
		// `d <= 0` guards against int64 overflow if maxReconnectDelay is ever
		// wired to config and set high enough that doubling wraps negative —
		// which would otherwise busy-loop the reconnect on a negative delay.
		if d <= 0 || d >= ss.maxReconnectDelay {
			return withJitter(ss.maxReconnectDelay)
		}
	}
	return withJitter(d)
}

// withJitter returns a value in [d/2, d] to de-correlate reconnects across many
// SDK instances (thundering-herd avoidance after a shared outage).
func withJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	half := int64(d / 2)
	return time.Duration(half + rand.Int63n(half+1))
}

// connect opens a single SSE connection and reads events until the connection
// closes or the context is cancelled. reached reports whether the stream was
// live AND actually delivered at least one event frame (the `sync` snapshot, a
// delta, or a ping) — NOT merely that a 200 was received. run() uses it to
// decide backoff/fallback state, so a stream that returns 200 but delivers
// nothing (a dead proxy, or a snapshot too large to read) counts as a failure
// and lets the polling fallback arm, rather than resetting the failure counter
// forever. The bool is independent of whether an error also occurred (e.g. a
// read error after events were already delivered).
func (ss *streamSource) connect() (reached bool, err error) {
	req, err := ss.hc.newStreamRequest()
	if err != nil {
		return false, err
	}
	req = req.WithContext(ss.ctx)

	client := ss.hc.streamHTTPClient(ss.connectTimeout)
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return false, &streamStatusError{statusCode: resp.StatusCode}
	}

	// 200 OK: we have a live stream — the server also sends a `sync` snapshot
	// as its first event (see handleEvent). Read events until close/cancel.
	scanner := bufio.NewScanner(resp.Body)
	// The connect-time snapshot arrives as a single (potentially large) `data:`
	// line; lift the default 64 KiB token cap so it doesn't choke the stream.
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSELineSize)
	var eventType, data string
	delivered := false

	for scanner.Scan() {
		select {
		case <-ss.ctx.Done():
			return delivered, nil
		default:
		}

		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			// SSE spec: multiple data: lines in one event are joined with "\n".
			// The server currently sends a single line, but a chunked snapshot
			// would arrive as several — overwriting would silently drop all but
			// the last, corrupting the payload.
			chunk := strings.TrimPrefix(line, "data: ")
			if data == "" {
				data = chunk
			} else {
				data += "\n" + chunk
			}
		} else if line == "" {
			// Empty line = end of event.
			if eventType != "" && data != "" {
				ss.handleEvent(eventType, data)
				// A complete event frame proves the stream is genuinely alive and
				// delivering (this includes server pings), so it counts as a real
				// recovery for run()'s fallback bookkeeping.
				delivered = true
			}
			eventType = ""
			data = ""
		}
	}
	return delivered, scanner.Err()
}

// handleEvent processes a single SSE event based on its type.
func (ss *streamSource) handleEvent(eventType, data string) {
	switch eventType {
	case "flag.created", "flag.updated":
		var evt streamEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			return
		}
		if evt.Key == "" {
			return
		}
		flag, err := ss.hc.getFlag(evt.Key)
		if err != nil {
			return
		}
		ss.store.setFlag(*flag)
		if ss.onUpdate != nil {
			ss.onUpdate(evt.Key)
		}

	case "flag.deleted":
		var evt streamEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			return
		}
		if evt.Key == "" {
			return
		}
		ss.store.removeFlag(evt.Key)
		if ss.onUpdate != nil {
			ss.onUpdate(evt.Key)
		}

	case "segment.updated":
		resp, err := ss.hc.getFlags()
		if err != nil {
			return
		}
		ss.store.setAll(resp.Flags, resp.Segments)
		if ss.onUpdate != nil {
			ss.onUpdate("")
		}

	case "sync":
		// Full config snapshot sent by the server on (re)connect. Replace the
		// entire store so flags changed — or deleted — during a disconnect are
		// re-synced. Full replace, never merge (mirrors polling + segment.updated).
		var resp getFlagsResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			return
		}
		ss.store.setAll(resp.Flags, resp.Segments)
		if ss.onUpdate != nil {
			ss.onUpdate("")
		}
	}
}

// startFallbackPolling begins periodic full-config polling alongside stream
// retries, so a persistently-broken stream still gets corrective full
// re-fetches. Idempotent — a poller runs at most once.
func (ss *streamSource) startFallbackPolling() {
	ss.fallbackMu.Lock()
	defer ss.fallbackMu.Unlock()
	// Bail if already running, OR if stop() has begun (cancel() runs before
	// stopFallbackPolling(), so a non-nil ctx.Err() means a post-stop start
	// would leak a poller nobody ever stops — its context is not ss.ctx).
	if ss.fallbackPoll != nil || ss.ctx.Err() != nil {
		return
	}
	// Poll at the base reconnect interval (tests use sub-second; prod ~3s).
	ps := newPollSource(ss.hc, ss.store, ss.reconnectDelay)
	go ps.run()
	ss.fallbackPoll = ps
}

// stopFallbackPolling stops the polling fallback if one is running.
func (ss *streamSource) stopFallbackPolling() {
	ss.fallbackMu.Lock()
	defer ss.fallbackMu.Unlock()
	if ss.fallbackPoll != nil {
		ss.fallbackPoll.stop()
		ss.fallbackPoll = nil
	}
}

// stop cancels the SSE connection, the reconnection loop, and any polling fallback.
func (ss *streamSource) stop() {
	ss.cancel()
	ss.stopFallbackPolling()
}
