package featureflip

import (
	"log"
	"sync"
	"time"
)

// defaultMaxEventQueueSize is the upper bound on buffered events.
//
// Only reachable once requeue starts returning batches faster than they drain —
// i.e. a sustained outage of the events endpoint. Past the bound the OLDEST
// events are shed, which caps memory and keeps the freshest analytics. It also
// means a long outage sheds the re-queued (stale) batches first rather than
// starving new events, so the SDK degrades to the old drop-everything behaviour
// instead of hoarding data it cannot send.
const defaultMaxEventQueueSize = 10_000

// eventProcessor batches SDK events and flushes them to the evaluation API
// either when the batch is full or on a periodic interval.
type eventProcessor struct {
	hc           *httpClient
	batchSize    int
	interval     time.Duration
	maxQueueSize int

	mu     sync.Mutex
	buf    []sdkEvent
	closed bool

	// nextAutoFlushAt is the instant before which the batch-size trigger must
	// not start another flush.
	//
	// A re-queued batch leaves the buffer at or above batchSize, so without this
	// gate every subsequent enqueue would start another flush — turning a
	// failing endpoint into one request per event, which is worse for the server
	// than the dropping it replaces. The periodic flush is the retry vehicle;
	// this only suppresses the size trigger between its ticks. Guarded by mu.
	nextAutoFlushAt time.Time

	// autoFlushInFlight is true while a size-triggered flush is running.
	//
	// The backoff gate alone is not enough: it is only armed once a flush has
	// already FAILED, and the size trigger fires again long before the first
	// HTTP round-trip returns. Without this latch a tight loop of tracked events
	// starts a concurrent flush per event. Guarded by mu.
	autoFlushInFlight bool

	// drainDone is non-nil while a drain loop is running and is closed when it
	// finishes, so a concurrent flush can wait for it instead of starting a
	// second one.
	//
	// autoFlushInFlight above only ever guarded the SIZE trigger. Nothing stopped
	// the interval tick, an explicit Client.Flush and a size-triggered flush from
	// entering the loop together — two request streams against the endpoint the
	// backoff gate exists to protect, and a success in one clearing the gate a
	// failure in the other had just armed, which re-opens the one-request-per-event
	// behaviour outright (#2477). Guarded by mu.
	drainDone chan struct{}

	stopCh chan struct{}
	done   chan struct{}
}

// newEventProcessor creates a new event processor bounded at
// defaultMaxEventQueueSize.
func newEventProcessor(hc *httpClient, batchSize int, interval time.Duration) *eventProcessor {
	return newEventProcessorWithBound(hc, batchSize, interval, defaultMaxEventQueueSize)
}

// newEventProcessorWithBound is newEventProcessor with an explicit buffer bound.
// Exists so tests can exercise overflow without enqueueing ten thousand events;
// a non-positive bound falls back to the default.
func newEventProcessorWithBound(hc *httpClient, batchSize int, interval time.Duration, maxQueueSize int) *eventProcessor {
	if maxQueueSize <= 0 {
		maxQueueSize = defaultMaxEventQueueSize
	}
	return &eventProcessor{
		hc:           hc,
		batchSize:    batchSize,
		interval:     interval,
		maxQueueSize: maxQueueSize,
		buf:          make([]sdkEvent, 0, batchSize),
	}
}

// start begins the background flush goroutine.
func (ep *eventProcessor) start() {
	stopCh := make(chan struct{})
	done := make(chan struct{})

	ep.mu.Lock()
	ep.stopCh = stopCh
	ep.done = done
	ep.mu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(ep.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				// Deliberately the unguarded flush: the interval tick is the
				// retry vehicle for a batch the size trigger has backed off.
				ep.flush()
			case <-stopCh:
				return
			}
		}
	}()
}

// enqueue adds an event to the buffer, shedding the oldest events if that puts
// it over the bound. If the buffer reaches batchSize a flush is triggered,
// subject to the backoff gate and the in-flight latch. Safe for concurrent use.
func (ep *eventProcessor) enqueue(event sdkEvent) {
	if ep.hc == nil {
		return
	}

	ep.mu.Lock()
	if ep.closed {
		ep.mu.Unlock()
		return
	}
	ep.buf = append(ep.buf, event)
	dropped := ep.trimOldestLocked()
	shouldFlush := len(ep.buf) >= ep.batchSize
	ep.mu.Unlock()

	if dropped > 0 {
		log.Printf("[featureflip] event buffer is full; dropped %d of the oldest analytics event(s)", dropped)
	}

	if shouldFlush {
		ep.autoFlush()
	}
}

// autoFlush is the batch-size-triggered flush. It runs at most one flush at a
// time and stays quiet while a retryable failure has the size trigger in
// backoff; an explicit Flush and the interval tick both bypass it.
func (ep *eventProcessor) autoFlush() {
	ep.mu.Lock()
	if ep.closed || ep.autoFlushInFlight || time.Now().Before(ep.nextAutoFlushAt) {
		ep.mu.Unlock()
		return
	}
	ep.autoFlushInFlight = true
	ep.mu.Unlock()

	defer func() {
		ep.mu.Lock()
		ep.autoFlushInFlight = false
		ep.mu.Unlock()
	}()

	ep.flush()
}

// flush sends buffered events to the server, one request per batch, until the
// buffer is empty. Each batch is sliced off the front under lock and posted
// without holding it.
//
// The slice happens BEFORE the send, so the batch has to be held here to be put
// back if the send fails. Without that a single transient failure discarded it
// outright — and the public edge answers this endpoint with a 503 at a low but
// constant rate, so analytics were being lost steadily (#2456).
//
// One request per batch rather than one for the whole buffer: re-queuing is
// what lets the buffer grow to its bound during an outage, and posting ten
// thousand events at once risks a body the server rejects outright — a 413 is
// non-retryable, so the whole backlog would be dropped by the very path meant
// to preserve it.
//
// At most one drain loop runs at a time. A caller that arrives while one is
// already going waits for it and returns — it does NOT start its own and it does
// NOT return early, because a caller that asked for a flush is asking for its
// events to be sent, and handing back before the send settled would be a promise
// the SDK had not kept. This matches the js/node SDKs, whose flush() has always
// returned the in-flight promise.
func (ep *eventProcessor) flush() {
	if ep.hc == nil {
		return
	}

	ep.mu.Lock()
	if done := ep.drainDone; done != nil {
		ep.mu.Unlock()
		<-done
		return
	}
	done := make(chan struct{})
	ep.drainDone = done
	ep.mu.Unlock()

	defer func() {
		ep.mu.Lock()
		ep.drainDone = nil
		ep.mu.Unlock()
		close(done)
	}()

	ep.drain()
}

// drain is the loop itself, callable when coalescing must be bypassed.
func (ep *eventProcessor) drain() {
	if ep.hc == nil {
		return
	}

	for {
		events := ep.drainBatch()
		if len(events) == 0 {
			return
		}

		err := ep.hc.postEvents(events)

		switch {
		case err == nil:
			// Clear the backoff so the size trigger resumes immediately on
			// recovery rather than waiting out a gate armed by an earlier
			// failure.
			ep.mu.Lock()
			ep.nextAutoFlushAt = time.Time{}
			ep.mu.Unlock()

		case !isRetryableSendFailure(err):
			log.Printf("[featureflip] dropping %d analytics event(s) the events endpoint rejected permanently: %v", len(events), err)
			// Dropped rather than re-queued, so the buffer shrinks and the loop
			// still terminates — carry on to the next batch rather than letting
			// one poison batch block the backlog queued behind it.

		default:
			dropped := ep.requeue(events)
			log.Printf("[featureflip] failed to flush %d analytics event(s); re-queued for the next flush (%d dropped to stay within the buffer bound): %v",
				len(events), dropped, err)
			// Stop here. The batch is back at the head of the buffer this loop
			// is draining, so continuing would re-send it at once and spin for
			// as long as the endpoint stays down.
			return
		}

		// During shutdown the loop keeps going only while sends SUCCEED, so a
		// graceful close still delivers everything it can. The moment one
		// fails, that was the single final attempt promised by stop().
		if err != nil && ep.isClosed() {
			return
		}
	}
}

// drainBatch takes up to batchSize of the OLDEST buffered events, leaving the
// rest queued. Returns nil when the buffer is empty.
func (ep *eventProcessor) drainBatch() []sdkEvent {
	ep.mu.Lock()
	defer ep.mu.Unlock()

	if len(ep.buf) == 0 {
		return nil
	}

	take := ep.batchSize
	if take < 1 {
		// Defence-in-depth against a non-positive batch size: taking nothing
		// would leave a non-empty buffer forever and spin the loop above.
		take = 1
	}
	if take > len(ep.buf) {
		take = len(ep.buf)
	}

	events := make([]sdkEvent, take)
	copy(events, ep.buf[:take])

	// What is left is copied into a fresh slice rather than resliced, for the
	// same reason trimOldestLocked does it: a reslice keeps the taken events
	// reachable through the old backing array. The capacity hint preserves the
	// batchSize-sized buffer the processor started with.
	remaining := len(ep.buf) - take
	capacity := ep.batchSize
	if capacity < remaining {
		capacity = remaining
	}
	rest := make([]sdkEvent, remaining, capacity)
	copy(rest, ep.buf[take:])
	ep.buf = rest

	return events
}

// isClosed reports whether stop has begun.
func (ep *eventProcessor) isClosed() bool {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	return ep.closed
}

// requeue returns a batch that failed to send to the FRONT of the buffer, so
// the next flush retries it ahead of newer events and rough chronological order
// survives. It also arms the backoff gate — a batch coming back is precisely
// the signal the size trigger must stand down on.
//
// Deliberately NOT an inline retry: re-sending here would spin against a
// failing endpoint for as long as the outage lasted, holding up whichever
// goroutine called flush (an ordinary Track on the caller's hot path, or
// shutdown). Handing the batch back and letting the next flush pick it up keeps
// every attempt bounded to one round-trip.
//
// Returns how many events were shed to stay within the bound.
func (ep *eventProcessor) requeue(events []sdkEvent) int {
	if len(events) == 0 {
		return 0
	}

	ep.mu.Lock()
	defer ep.mu.Unlock()

	// Closed means shutdown is under way and nothing will flush again; report
	// the whole batch as dropped rather than growing a buffer no one will drain.
	if ep.closed {
		return len(events)
	}

	combined := make([]sdkEvent, 0, len(events)+len(ep.buf))
	combined = append(combined, events...)
	combined = append(combined, ep.buf...)
	ep.buf = combined

	ep.nextAutoFlushAt = time.Now().Add(ep.interval)

	return ep.trimOldestLocked()
}

// trimOldestLocked sheds oldest-first until the buffer fits the bound and
// returns how many events it dropped. Caller holds mu.
func (ep *eventProcessor) trimOldestLocked() int {
	overflow := len(ep.buf) - ep.maxQueueSize
	if overflow <= 0 {
		return 0
	}

	// Copied into a fresh slice rather than resliced: a reslice keeps the
	// dropped events reachable through the old backing array, which is the
	// opposite of what a memory bound is for.
	kept := make([]sdkEvent, len(ep.buf)-overflow)
	copy(kept, ep.buf[overflow:])
	ep.buf = kept

	return overflow
}

// stop shuts down the background flush goroutine and makes ONE final flush
// attempt. Whatever that attempt cannot deliver is discarded: looping until the
// buffer emptied would hang shutdown for as long as the endpoint stayed down,
// and nothing will flush after this. Idempotent.
func (ep *eventProcessor) stop() {
	ep.mu.Lock()
	if ep.closed {
		ep.mu.Unlock()
		return
	}
	ep.closed = true
	stopCh, done := ep.stopCh, ep.done
	ep.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
		<-done
	}

	// drain, not flush: shutdown must never be the call that gets coalesced away.
	// If a periodic drain happens to be in flight, flush would wait for it and
	// return, and anything enqueued after that loop's last look at the buffer
	// would be discarded unsent. Running two drains concurrently is safe here
	// precisely because closed is already set, so neither can re-queue and there
	// is no backoff left to disarm.
	//
	// closed is already set, so a retryable failure here reports the batch as
	// dropped instead of re-queueing it into a buffer nobody will drain.
	ep.drain()

	ep.mu.Lock()
	ep.buf = nil
	ep.mu.Unlock()
}
