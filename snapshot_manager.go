package featureflip

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// snapshotManager owns the snapshot store integration of one shared core: the
// startup info, the startup fallback read, and the coalesced background writes.
//
// All methods are safe on a nil receiver (a [ForTesting] core has none) and when
// no store is configured, in which case only the startup info is tracked.
//
// Concurrency: mu guards the bookkeeping only. It is never held across a store
// call or a serialization, so a slow or hung store cannot block evaluation, the
// data sources, or Close. At most one write goroutine runs at a time. A request
// that arrives while it runs sets pending, and the goroutine loops once more,
// re-reading the state when that trailing write runs.
type snapshotManager struct {
	store         SnapshotStore // nil when no store is configured
	state         *store
	keyHash       string
	maxAgeSeconds int64
	refresh       time.Duration
	now           func() time.Time

	mu        sync.Mutex
	info      StartupInfo
	closed    bool
	writing   bool
	pending   bool
	haveWrite bool
	lastData  string    // serialized data of the last write that started
	lastStart time.Time // when the last write started
}

func newSnapshotManager(cfg config, sdkKey string, state *store) *snapshotManager {
	return &snapshotManager{
		store:         cfg.snapshotStore,
		state:         state,
		keyHash:       snapshotKeyHash(cfg.baseURL, sdkKey),
		maxAgeSeconds: int64(cfg.effectiveMaxSnapshotAge() / time.Second),
		refresh:       snapshotRefreshInterval,
		now:           time.Now,
		info:          StartupInfo{Source: StartupNone},
	}
}

// startup returns the current startup info.
func (m *snapshotManager) startup() StartupInfo {
	if m == nil {
		return StartupInfo{Source: StartupLive}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info
}

// close stops any further write from starting. It neither waits for nor cancels
// a write already in flight.
func (m *snapshotManager) close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	m.pending = false
	m.mu.Unlock()
}

// markLive moves the startup info to live, clearing the reason and age.
func (m *snapshotManager) markLive() {
	m.mu.Lock()
	m.info = StartupInfo{Source: StartupLive}
	m.mu.Unlock()
}

// fullApplied is called after every full apply of live data (initial fetch, poll,
// SSE sync, segment refetch). The first one after a snapshot or none start moves
// the startup info to live and always writes. Later ones skip the write when the
// state equals the last write's and that write began inside the refresh window.
func (m *snapshotManager) fullApplied() {
	if m == nil {
		return
	}
	m.markLive()
	if m.store == nil {
		return
	}
	data, err := m.serializeState()
	if err != nil {
		log.Printf("[featureflip] snapshot not written: could not serialize flag state: %v", err)
		return
	}
	m.mu.Lock()
	now := m.now()
	unchanged := m.haveWrite && string(data) == m.lastData && now.Sub(m.lastStart) < m.refresh
	if !unchanged && !m.closed {
		// Record the write we are about to request from the bytes already in hand,
		// so an identical full apply that follows immediately (the first poll after
		// the initial fetch) is skipped instead of writing twice. writeOnce
		// overwrites this with what it actually serializes when it runs.
		m.haveWrite = true
		m.lastData = string(data)
		m.lastStart = now
	}
	m.mu.Unlock()
	if !unchanged {
		m.schedule()
	}
}

// deltaApplied is called after a single-flag SSE update or delete changed the
// state. It writes only when the client started live: right after a snapshot or
// none start the first delta would re-stamp stale data with a fresh fetchedAt.
func (m *snapshotManager) deltaApplied() {
	if m == nil || m.store == nil {
		return
	}
	m.mu.Lock()
	live := m.info.Source == StartupLive
	m.mu.Unlock()
	if live {
		m.schedule()
	}
}

// ping is the stream heartbeat. While live it rewrites the unchanged state at
// most once per refresh interval, measured from the start of the last write.
func (m *snapshotManager) ping() {
	if m == nil || m.store == nil {
		return
	}
	m.mu.Lock()
	due := m.info.Source == StartupLive && m.now().Sub(m.lastStart) >= m.refresh
	m.mu.Unlock()
	if due {
		m.schedule()
	}
}

func (m *snapshotManager) serializeState() ([]byte, error) {
	flags, segments := m.state.sortedSnapshot()
	return serializeSnapshotData(flags, segments)
}

// schedule requests a write: start the writer goroutine, or mark a trailing write
// when one is already running.
func (m *snapshotManager) schedule() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	if m.writing {
		m.pending = true
		m.mu.Unlock()
		return
	}
	m.writing = true
	m.mu.Unlock()
	go m.writeLoop()
}

func (m *snapshotManager) writeLoop() {
	for {
		m.mu.Lock()
		if m.closed {
			m.writing = false
			m.pending = false
			m.mu.Unlock()
			return
		}
		m.pending = false
		start := m.now()
		m.mu.Unlock()

		m.writeOnce(start)

		m.mu.Lock()
		if !m.pending || m.closed {
			m.writing = false
			m.pending = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}

// writeOnce reads the state as it is NOW and hands it to the store. A store error
// or panic is logged and the write dropped.
func (m *snapshotManager) writeOnce(start time.Time) {
	data, err := m.serializeState()
	if err != nil {
		log.Printf("[featureflip] snapshot not written: could not serialize flag state: %v", err)
		return
	}
	text, err := encodeSnapshotEnvelope(m.keyHash, start.UnixMilli(), data)
	if err != nil {
		log.Printf("[featureflip] snapshot not written: could not encode envelope: %v", err)
		return
	}
	m.mu.Lock()
	m.haveWrite = true
	m.lastData = string(data)
	m.lastStart = start
	m.mu.Unlock()

	// A failed write must not make the unchanged-state comparison skip the next
	// full apply for the whole refresh window: forget it, so the next one retries.
	forget := func() {
		m.mu.Lock()
		if m.lastData == string(data) {
			m.haveWrite = false
		}
		m.mu.Unlock()
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[featureflip] snapshot store Put panicked, write dropped: %v", r)
			forget()
		}
	}()
	if err := m.store.Put(text); err != nil {
		log.Printf("[featureflip] snapshot store Put failed, write dropped: %v", err)
		forget()
	}
}

// --- startup read ---

type snapshotRead struct {
	text  string
	found bool
	err   error
}

// readStore calls Get on its own goroutine and waits at most timeout. The result
// channel is buffered, so a late result is dropped without leaking the goroutine.
func (m *snapshotManager) readStore(timeout time.Duration) (snapshotRead, bool) {
	ch := make(chan snapshotRead, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- snapshotRead{err: fmt.Errorf("Get panicked: %v", r)}
			}
		}()
		text, found, err := m.store.Get()
		ch <- snapshotRead{text: text, found: found, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r, true
	case <-timer.C:
		return snapshotRead{}, false
	}
}

// loadFallback runs after a failed or timed-out initial fetch. It reads the store
// (bounded by timeout), validates the text and, when usable, applies it to the
// flag store as the baseline. It never writes and never fires update listeners.
// cause names why the fetch failed, so a revoked key is distinguishable from an
// unreachable host in the log.
func (m *snapshotManager) loadFallback(cause string, timeout time.Duration) {
	if m == nil || m.store == nil {
		return
	}
	log.Printf("[featureflip] initial fetch failed (%s), trying the snapshot store", cause)

	r, ok := m.readStore(timeout)
	if !ok {
		log.Printf("[featureflip] snapshot store Get did not return within %s, serving defaults", timeout)
		return
	}
	if r.err != nil {
		log.Printf("[featureflip] snapshot store Get failed, treating as a miss: %v", r.err)
		return
	}
	if !r.found {
		return // a plain miss: nothing was ever stored
	}

	now := m.now()
	decoded, err := decodeSnapshotEnvelope(r.text, m.keyHash, now)
	if err != nil {
		m.setInfo(StartupInfo{Source: StartupNone, Reason: SnapshotReasonInvalid})
		log.Printf("[featureflip] ignoring stored snapshot (%s): %v", SnapshotReasonInvalid, err)
		return
	}
	ageMs := now.UnixMilli() - decoded.fetchedAtMs
	if ageMs < 0 {
		ageMs = 0
	}
	ageSeconds := ageMs / 1000
	if ageMs > m.maxAgeSeconds*1000 {
		m.setInfo(StartupInfo{Source: StartupNone, Reason: SnapshotReasonExpired, AgeSeconds: ageSeconds})
		log.Printf("[featureflip] ignoring stored snapshot (%s): it is %ds old and the maximum is %ds",
			SnapshotReasonExpired, ageSeconds, m.maxAgeSeconds)
		return
	}

	// Entities this SDK build cannot evaluate are dropped, as on every live path.
	// The return value (the changed keys) is deliberately discarded: loading a
	// snapshot is the baseline and fires no listeners.
	m.state.setAll(dropUnevaluable(decoded.resp.Flags, decoded.resp.Segments))
	m.setInfo(StartupInfo{Source: StartupSnapshot, Reason: SnapshotReasonSnapshot, AgeSeconds: ageSeconds})
	log.Printf("[featureflip] started from a stored snapshot that is %ds old; the live connection keeps retrying", ageSeconds)
}

func (m *snapshotManager) setInfo(info StartupInfo) {
	m.mu.Lock()
	m.info = info
	m.mu.Unlock()
}
