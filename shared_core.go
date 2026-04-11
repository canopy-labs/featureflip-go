package featureflip

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// sharedCore owns all expensive resources of a Client: the HTTP client, flag
// store, event processor, and background streaming/polling goroutines.
//
// Refcounted via atomic int32: multiple Client handles share one core, and the
// real shutdown runs only when the last handle is closed. The get-or-create
// logic in the package-level factory uses a sync.Mutex-guarded map keyed by
// SDK key so concurrent Get calls resolve to exactly one core per key.
type sharedCore struct {
	cfg         config
	sdkKey      string
	store       *store
	hc          *httpClient
	ep          *eventProcessor
	initialized bool
	initOnce    sync.Once
	initErr     error
	stopStream  func()
	stopPoll    func()

	refCount int32 // accessed via atomic operations
	shutDown int32 // 0 = alive, 1 = shut down (CAS guard)

	owningMu  *sync.Mutex
	owningMap map[string]*sharedCore
	owningKey string
}

// newSharedCore constructs a core but does NOT initialize it (no network
// calls). Initialization is deferred to initializeOnce, which is called by
// Client.Initialized or implicitly by the factory.
func newSharedCore(sdkKey string, cfg config) *sharedCore {
	hc := newHTTPClient(sdkKey, cfg)
	return &sharedCore{
		cfg:      cfg,
		sdkKey:   sdkKey,
		store:    newStore(),
		hc:       hc,
		ep:       newEventProcessor(hc, cfg.flushBatchSize, cfg.flushInterval),
		refCount: 1, // first handle
	}
}

// initializeOnce performs the blocking initial fetch, starts the event
// processor and the streaming/polling data source. Exactly-once: concurrent
// callers all observe the same result via sync.Once.
func (sc *sharedCore) initializeOnce() error {
	sc.initOnce.Do(func() {
		sc.initErr = sc.doInit()
	})
	return sc.initErr
}

func (sc *sharedCore) doInit() error {
	// Initial fetch with timeout.
	type fetchResult struct {
		resp *getFlagsResponse
		err  error
	}
	ch := make(chan fetchResult, 1)
	go func() {
		resp, err := sc.hc.getFlags()
		ch <- fetchResult{resp: resp, err: err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), sc.cfg.initTimeout)
	defer cancel()

	select {
	case result := <-ch:
		if result.err != nil {
			return fmt.Errorf("featureflip: initial fetch failed: %w", result.err)
		}
		sc.store.setAll(result.resp.Flags, result.resp.Segments)
	case <-ctx.Done():
		return fmt.Errorf("featureflip: initial fetch timed out after %s", sc.cfg.initTimeout)
	}

	// Start event processor.
	sc.ep.start()

	// Start streaming or polling.
	if sc.cfg.streaming {
		ss := newStreamSource(sc.hc, sc.store, nil)
		ss.connectTimeout = sc.cfg.connectTimeout
		go ss.run()
		sc.stopStream = ss.stop
	} else {
		ps := newPollSource(sc.hc, sc.store, sc.cfg.pollInterval)
		go ps.run()
		sc.stopPoll = ps.stop
	}

	sc.initialized = true
	return nil
}

// setOwning records the factory map and key so shutdown can remove itself.
func (sc *sharedCore) setOwning(mu *sync.Mutex, m map[string]*sharedCore, key string) {
	sc.owningMu = mu
	sc.owningMap = m
	sc.owningKey = key
}

// tryAcquire atomically increments the refcount if the core is still alive.
// Returns false if the core has shut down — caller must construct a new one.
func (sc *sharedCore) tryAcquire() bool {
	for {
		current := atomic.LoadInt32(&sc.refCount)
		if current <= 0 {
			return false
		}
		if atomic.CompareAndSwapInt32(&sc.refCount, current, current+1) {
			return true
		}
	}
}

// release decrements the refcount. When it reaches zero, runs the real
// shutdown exactly once. Over-release is a no-op.
func (sc *sharedCore) release() {
	for {
		current := atomic.LoadInt32(&sc.refCount)
		if current <= 0 {
			return
		}
		if atomic.CompareAndSwapInt32(&sc.refCount, current, current-1) {
			if current-1 == 0 {
				if atomic.CompareAndSwapInt32(&sc.shutDown, 0, 1) {
					sc.shutdown()
				}
			}
			return
		}
	}
}

func (sc *sharedCore) shutdown() {
	// Remove from factory map first.
	if sc.owningMu != nil && sc.owningMap != nil {
		sc.owningMu.Lock()
		if sc.owningMap[sc.owningKey] == sc {
			delete(sc.owningMap, sc.owningKey)
		}
		sc.owningMu.Unlock()
	}

	if sc.stopStream != nil {
		sc.stopStream()
	}
	if sc.stopPoll != nil {
		sc.stopPoll()
	}
	sc.ep.stop()
}

func (sc *sharedCore) debugRefCount() int {
	return int(atomic.LoadInt32(&sc.refCount))
}

func (sc *sharedCore) isShutDown() bool {
	return atomic.LoadInt32(&sc.shutDown) != 0
}

// configsEqual compares the fields that matter for the "options differ on
// repeat Get()" warning. sdkKey is excluded (it is the cache key itself).
func configsEqual(a, b config) bool {
	return a.baseURL == b.baseURL &&
		a.streaming == b.streaming &&
		a.pollInterval == b.pollInterval &&
		a.flushInterval == b.flushInterval &&
		a.flushBatchSize == b.flushBatchSize &&
		a.initTimeout == b.initTimeout &&
		a.connectTimeout == b.connectTimeout &&
		a.readTimeout == b.readTimeout
}
