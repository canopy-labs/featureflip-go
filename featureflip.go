// Package featureflip provides a Go SDK for [Featureflip] feature flag evaluation.
//
// Flag rules are fetched once and evaluated in-process, so a variation call
// costs no network round trip. Changes arrive over a server-sent-events stream,
// with polling as a fallback.
//
// Obtain a client via the package-level [Get] function. Multiple Get calls with
// the same SDK key return handles sharing one underlying shared core
// (refcounted); the shared core shuts down when the last handle is closed.
//
// The [Go SDK guide] covers targeting rules, segments, and event tracking with
// worked examples; the reference below documents the surface itself.
//
// [Featureflip]: https://featureflip.io
// [Go SDK guide]: https://featureflip.io/docs/sdks/go/
package featureflip

import (
	"errors"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// factory state: process-wide map of shared cores keyed by SDK key.
var (
	liveCoresMu sync.Mutex
	liveCores   = make(map[string]*sharedCore)
)

// Client is a handle to a shared Featureflip client. Multiple handles can
// share one underlying [sharedCore]. All evaluation, tracking, and lifecycle
// methods delegate to the core. Call [Client.Close] when done; when the last
// handle for a given SDK key is closed, the core shuts down.
type Client struct {
	core     *sharedCore
	disposed int32 // 0 = alive, 1 = disposed (per-handle)

	// subsMu guards unsubs, the flag-update subscriptions registered through
	// THIS handle. They are dropped when this handle closes, even though the
	// shared core may outlive it — a listener registered through a closed
	// handle firing on a sibling's stream would be a leak the caller has no
	// way to stop.
	subsMu sync.Mutex
	unsubs []func()
}

// Get returns a client for the given SDK key. The first call with a given key
// constructs and initializes a shared core; subsequent calls with the same key
// return a new handle pointing at the cached core. When the last handle for a
// key is closed, the core shuts down and is removed from the cache.
//
// If sdkKey is empty, the FEATUREFLIP_SDK_KEY environment variable is used.
//
// The opts are honored only on the first call for a given SDK key. Subsequent
// callers that pass meaningfully different options will see a warning logged;
// the cached core's options are preserved.
func Get(sdkKey string, opts ...Option) (*Client, error) {
	if sdkKey == "" {
		sdkKey = os.Getenv("FEATUREFLIP_SDK_KEY")
	}
	if sdkKey == "" {
		return nil, errors.New("featureflip: SDK key is required (pass directly or set FEATUREFLIP_SDK_KEY)")
	}

	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	// Retry loop: handles the race where a cached core is found but has
	// already begun shutting down (refcount hit 0 between lookup and
	// tryAcquire). Each iteration makes progress.
	for {
		liveCoresMu.Lock()
		existing, ok := liveCores[sdkKey]
		liveCoresMu.Unlock()

		if ok {
			if existing.tryAcquire() {
				if !configsEqual(existing.cfg, cfg) {
					log.Printf("[featureflip] Get called with different options for SDK key already in use. The cached instance's options are preserved; the passed options are ignored.")
				}
				return &Client{core: existing}, nil
			}
			// Stale entry — core shut down between lookup and acquire.
			liveCoresMu.Lock()
			if liveCores[sdkKey] == existing {
				delete(liveCores, sdkKey)
			}
			liveCoresMu.Unlock()
			continue
		}

		newCore := newSharedCore(sdkKey, cfg)

		liveCoresMu.Lock()
		// Double-check: another goroutine may have inserted between our
		// lookup and the lock acquisition.
		if race, ok := liveCores[sdkKey]; ok {
			liveCoresMu.Unlock()
			// Release our speculative core — the other goroutine won.
			newCore.release()
			if race.tryAcquire() {
				return &Client{core: race}, nil
			}
			// That one is stale too — retry.
			continue
		}
		liveCores[sdkKey] = newCore
		newCore.setOwning(&liveCoresMu, liveCores, sdkKey)
		liveCoresMu.Unlock()

		// Initialize the core (blocking). If init fails, remove from map
		// and release.
		if err := newCore.initializeOnce(); err != nil {
			liveCoresMu.Lock()
			if liveCores[sdkKey] == newCore {
				delete(liveCores, sdkKey)
			}
			liveCoresMu.Unlock()
			newCore.release()
			return nil, err
		}

		return &Client{core: newCore}, nil
	}
}

// isClosed reports whether this handle has been closed.
//
// Close() releases the core — stopping streaming/polling and shutting down the
// event processor — but the in-memory store stays readable, so an unguarded
// handle would keep serving a frozen snapshot that can never update again while
// still reporting itself initialized (#2289). Every public accessor consults
// this so a closed handle degrades to the caller's default, matching the
// contract the Python and PHP SDKs already implement.
func (c *Client) isClosed() bool {
	return atomic.LoadInt32(&c.disposed) != 0
}

// narrow asserts detail.Value to the type the calling accessor requires.
//
// On a mismatch the caller's default is substituted AND the reason is rewritten
// to [ReasonError], so a type-mismatched read is detectable instead of looking
// like a healthy serve (#2281). Substituting the value without the reason — the
// prior behaviour — meant a caller reading a string flag through
// [Client.BoolVariation] silently got their default back under ReasonFallthrough.
//
// Paths that never served a value are unaffected: flag-not-found already carries
// the caller's default, so its assertion succeeds and ReasonFlagNotFound stands.
func narrow[T any](detail *EvaluationDetail, defaultValue T) T {
	value, ok := detail.Value.(T)
	if !ok {
		value = defaultValue
		detail.Reason = ReasonError
	}
	detail.Value = value
	return value
}

// BoolVariation evaluates a boolean feature flag. Returns defaultValue if the
// flag is not found, an error occurs, or the served value is not a bool.
//
// Registered evaluation inspectors fire exactly once, after the type coercion
// below, so the event's Value is exactly the value returned here.
func (c *Client) BoolVariation(key string, ctx EvaluationContext, defaultValue bool) bool {
	if c.isClosed() {
		return defaultValue
	}
	detail := c.core.evaluateFlag(key, ctx, defaultValue)
	// detail is a value copy; rewriting it here cannot affect the store or the
	// caller — it only makes the inspector event report what we return.
	value := narrow(&detail, defaultValue)
	c.core.notifyInspectors(key, ctx, detail)
	return value
}

// StringVariation evaluates a string feature flag. Returns defaultValue if the
// flag is not found, an error occurs, or the served value is not a string.
//
// Registered evaluation inspectors fire exactly once, after the type coercion
// below, so the event's Value is exactly the value returned here.
func (c *Client) StringVariation(key string, ctx EvaluationContext, defaultValue string) string {
	if c.isClosed() {
		return defaultValue
	}
	detail := c.core.evaluateFlag(key, ctx, defaultValue)
	// detail is a value copy; rewriting it here cannot affect the store or the
	// caller — it only makes the inspector event report what we return.
	value := narrow(&detail, defaultValue)
	c.core.notifyInspectors(key, ctx, detail)
	return value
}

// Float64Variation evaluates a numeric feature flag. Returns defaultValue if
// the flag is not found, an error occurs, or the served value is not a number.
//
// Registered evaluation inspectors fire exactly once, after the type coercion
// below, so the event's Value is exactly the value returned here.
func (c *Client) Float64Variation(key string, ctx EvaluationContext, defaultValue float64) float64 {
	if c.isClosed() {
		return defaultValue
	}
	detail := c.core.evaluateFlag(key, ctx, defaultValue)
	// detail is a value copy; rewriting it here cannot affect the store or the
	// caller — it only makes the inspector event report what we return.
	value := narrow(&detail, defaultValue)
	c.core.notifyInspectors(key, ctx, detail)
	return value
}

// JSONVariation evaluates a JSON feature flag. Returns defaultValue if the
// flag is not found or an error occurs.
//
// Registered evaluation inspectors fire exactly once, with the value returned
// here.
func (c *Client) JSONVariation(key string, ctx EvaluationContext, defaultValue any) any {
	if c.isClosed() {
		return defaultValue
	}
	detail := c.core.evaluateFlag(key, ctx, defaultValue)
	value := detail.Value
	if detail.Reason == ReasonFlagNotFound {
		value = defaultValue
	}
	detail.Value = value
	c.core.notifyInspectors(key, ctx, detail)
	return value
}

// VariationDetail evaluates a feature flag and returns detailed evaluation
// information including the reason for the result.
//
// No typed coercion applies here — the whole detail is returned — so registered
// evaluation inspectors fire exactly once with the detail's own value.
func (c *Client) VariationDetail(key string, ctx EvaluationContext, defaultValue any) EvaluationDetail {
	if c.isClosed() {
		return EvaluationDetail{Value: defaultValue, Reason: ReasonError}
	}
	detail := c.core.evaluateFlag(key, ctx, defaultValue)
	c.core.notifyInspectors(key, ctx, detail)
	return detail
}

// Track records a custom event for analytics.
func (c *Client) Track(eventKey string, ctx EvaluationContext, metadata map[string]any) {
	if c.isClosed() {
		return
	}
	c.core.ep.enqueue(sdkEvent{
		Type:      "Custom",
		FlagKey:   eventKey,
		UserID:    ctx.UserID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Metadata:  metadata,
	})
}

// Identify records an identify event for user association.
func (c *Client) Identify(ctx EvaluationContext) {
	if c.isClosed() {
		return
	}
	// The caller's attributes ride along as metadata, matching the other server
	// SDKs. There is no alias to strip: UserID is a distinct struct field, so
	// Attributes never carries the identity.
	c.core.ep.enqueue(sdkEvent{
		Type:      "Identify",
		FlagKey:   "$identify",
		UserID:    ctx.UserID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Metadata:  ctx.Attributes,
	})
}

// Flush sends all buffered events to the server immediately.
func (c *Client) Flush() {
	if c.isClosed() {
		return
	}
	c.core.ep.flush()
}

// Initialized returns true if the client successfully completed initialization.
func (c *Client) Initialized() bool {
	if c.isClosed() {
		return false
	}
	return c.core.initialized
}

// Close decrements the refcount on the shared core. When the last handle for
// a given SDK key is closed, the core shuts down (stops streaming/polling,
// flushes events, removes itself from the factory cache). Double-close on the
// same handle is a no-op.
func (c *Client) Close() error {
	if atomic.CompareAndSwapInt32(&c.disposed, 0, 1) {
		// Drop this handle's subscriptions before releasing the core: the core
		// may survive (another handle holds it), and its data sources keep
		// running, so a listener left registered would go on firing for a
		// client the caller has already closed.
		c.subsMu.Lock()
		unsubs := c.unsubs
		c.unsubs = nil
		c.subsMu.Unlock()
		for _, unsub := range unsubs {
			unsub()
		}

		c.core.release()
	}
	return nil
}

// OnUpdate subscribes to flag-configuration changes.
//
// The listener is called with the flag keys whose configuration changed,
// batched into one call per update. It fires on the SDK's streaming or polling
// goroutine, so it must not block: a slow listener delays flag delivery and, on
// the SSE path, can stall the connection.
//
// The initial flag load does NOT fire — a cold start is not a change. Only
// later updates do. Keys are reported when a flag is added, removed or
// modified, and additionally for flags dragged along by the change: those
// referencing an edited segment, and those depending on a changed flag through
// a prerequisite (their evaluated value moves even though their own
// configuration did not).
//
// A panic in a listener is logged and swallowed; it does not affect flag
// delivery or the other listeners.
//
// The returned func unsubscribes and is idempotent. Subscriptions are also
// dropped when this handle is closed, so a caller that closes its client need
// not unsubscribe first.
//
// Example:
//
//	unsubscribe := client.OnUpdate(func(keys []string) {
//		log.Printf("flags changed: %v", keys)
//	})
//	defer unsubscribe()
func (c *Client) OnUpdate(listener FlagUpdateListener) func() {
	noop := func() {}
	if listener == nil {
		return noop
	}
	// Nothing will ever fire for a closed handle, so hand back a no-op rather
	// than registering a listener on a core this handle no longer participates
	// in.
	if c.isClosed() {
		return noop
	}

	unsub := c.core.addUpdateListener(listener)

	var once sync.Once
	idempotent := func() { once.Do(unsub) }

	c.subsMu.Lock()
	c.unsubs = append(c.unsubs, idempotent)
	c.subsMu.Unlock()

	return idempotent
}

// evaluateFlag is the core evaluation method on sharedCore — the single choke
// point every BoolVariation/StringVariation/Float64Variation/JSONVariation/
// VariationDetail call funnels through.
//
// It deliberately does NOT notify evaluation inspectors: the typed accessors
// coerce this detail's value (substituting their default on a type mismatch),
// so only they know the value the caller actually receives. Each public
// variation method calls [sharedCore.notifyInspectors] itself, exactly once,
// after coercion — and none of them delegates to another, so no call can
// double-fire. The evaluator itself is total (it never panics for a well-formed
// store), so its error case — a prerequisite chain deeper than
// maxPrerequisiteDepth, or an error bubbling up from a prerequisite — arrives
// as a normal return carrying [ReasonError] and is notified like any other.
func (sc *sharedCore) evaluateFlag(key string, ctx EvaluationContext, defaultValue any) EvaluationDetail {
	flag, ok := sc.store.getFlag(key)
	if !ok {
		return EvaluationDetail{
			Value:  defaultValue,
			Reason: ReasonFlagNotFound,
		}
	}

	detail := evaluate(flag, ctx, sc.store.allSegments(), sc.store.allFlags())

	// Malformed config: the evaluator selected a variation key the flag does not
	// define (e.g. a fallthrough/rule naming a since-deleted variation). Report
	// Error, mirroring the engine's ServeVariation + the C#/Java SDKs (#1989). A
	// variation that genuinely exists with a null JSON value is NOT this case —
	// hence the key-existence check rather than a nil-value check.
	if detail.Variation != "" && !variationExists(flag.Variations, detail.Variation) {
		detail.Reason = ReasonError
	}

	sc.ep.enqueue(sdkEvent{
		Type:      "Evaluation",
		FlagKey:   key,
		UserID:    ctx.UserID,
		Variation: detail.Variation,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	return detail
}

// --- Factory diagnostics (test-only) ---

// DebugLiveCoreCount returns the current number of shared cores in the
// factory cache. Diagnostic only — not part of the stable API.
func DebugLiveCoreCount() int {
	liveCoresMu.Lock()
	defer liveCoresMu.Unlock()
	return len(liveCores)
}

// DebugRefCount returns the refcount for the given SDK key, or 0 if no core
// is cached. Diagnostic only — not part of the stable API.
func DebugRefCount(sdkKey string) int {
	liveCoresMu.Lock()
	core, ok := liveCores[sdkKey]
	liveCoresMu.Unlock()
	if !ok {
		return 0
	}
	return core.debugRefCount()
}

// ResetForTesting clears the factory cache and shuts down all cached cores.
// For test isolation only.
func ResetForTesting() {
	liveCoresMu.Lock()
	snapshot := make([]*sharedCore, 0, len(liveCores))
	for _, core := range liveCores {
		snapshot = append(snapshot, core)
	}
	liveCores = make(map[string]*sharedCore)
	liveCoresMu.Unlock()

	for _, core := range snapshot {
		for core.debugRefCount() > 0 && !core.isShutDown() {
			core.release()
		}
	}
}
