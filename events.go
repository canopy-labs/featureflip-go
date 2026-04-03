package featureflip

import (
	"sync"
	"time"
)

// eventProcessor batches SDK events and flushes them to the evaluation API
// either when the batch is full or on a periodic interval.
type eventProcessor struct {
	hc        *httpClient
	batchSize int
	interval  time.Duration
	mu        sync.Mutex
	buf       []sdkEvent
	stopCh    chan struct{}
	done      chan struct{}
}

// newEventProcessor creates a new event processor.
func newEventProcessor(hc *httpClient, batchSize int, interval time.Duration) *eventProcessor {
	return &eventProcessor{
		hc:        hc,
		batchSize: batchSize,
		interval:  interval,
		buf:       make([]sdkEvent, 0, batchSize),
	}
}

// start begins the background flush goroutine.
func (ep *eventProcessor) start() {
	ep.stopCh = make(chan struct{})
	ep.done = make(chan struct{})

	go func() {
		defer close(ep.done)
		ticker := time.NewTicker(ep.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				ep.flush()
			case <-ep.stopCh:
				return
			}
		}
	}()
}

// enqueue adds an event to the buffer. If the buffer reaches batchSize,
// it is flushed immediately. Safe for concurrent use.
func (ep *eventProcessor) enqueue(event sdkEvent) {
	if ep.hc == nil {
		return
	}

	ep.mu.Lock()
	ep.buf = append(ep.buf, event)
	shouldFlush := len(ep.buf) >= ep.batchSize
	ep.mu.Unlock()

	if shouldFlush {
		ep.flush()
	}
}

// flush sends all buffered events to the server. It swaps the internal
// buffer under lock and then posts events without holding the lock.
// Errors from the HTTP call are silently ignored (fire-and-forget).
func (ep *eventProcessor) flush() {
	if ep.hc == nil {
		return
	}

	ep.mu.Lock()
	if len(ep.buf) == 0 {
		ep.mu.Unlock()
		return
	}
	events := ep.buf
	ep.buf = make([]sdkEvent, 0, ep.batchSize)
	ep.mu.Unlock()

	// Fire-and-forget: ignore errors.
	_ = ep.hc.postEvents(events)
}

// stop shuts down the background flush goroutine and performs a final flush.
func (ep *eventProcessor) stop() {
	if ep.stopCh == nil {
		return
	}

	close(ep.stopCh)
	<-ep.done
	ep.flush()
}
