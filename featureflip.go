// Package featureflip provides a Go SDK for Featureflip feature flag evaluation.
//
// Obtain a client via the package-level [Get] function. Multiple Get calls with
// the same SDK key return handles sharing one underlying shared core
// (refcounted); the shared core shuts down when the last handle is closed.
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

// BoolVariation evaluates a boolean feature flag. Returns defaultValue if the
// flag is not found or an error occurs.
func (c *Client) BoolVariation(key string, ctx EvaluationContext, defaultValue bool) bool {
	detail := c.core.evaluateFlag(key, ctx, defaultValue)
	if v, ok := detail.Value.(bool); ok {
		return v
	}
	return defaultValue
}

// StringVariation evaluates a string feature flag. Returns defaultValue if the
// flag is not found or an error occurs.
func (c *Client) StringVariation(key string, ctx EvaluationContext, defaultValue string) string {
	detail := c.core.evaluateFlag(key, ctx, defaultValue)
	if v, ok := detail.Value.(string); ok {
		return v
	}
	return defaultValue
}

// Float64Variation evaluates a numeric feature flag. Returns defaultValue if the
// flag is not found or an error occurs.
func (c *Client) Float64Variation(key string, ctx EvaluationContext, defaultValue float64) float64 {
	detail := c.core.evaluateFlag(key, ctx, defaultValue)
	if v, ok := detail.Value.(float64); ok {
		return v
	}
	return defaultValue
}

// JSONVariation evaluates a JSON feature flag. Returns defaultValue if the
// flag is not found or an error occurs.
func (c *Client) JSONVariation(key string, ctx EvaluationContext, defaultValue any) any {
	detail := c.core.evaluateFlag(key, ctx, defaultValue)
	if detail.Reason == ReasonFlagNotFound {
		return defaultValue
	}
	return detail.Value
}

// VariationDetail evaluates a feature flag and returns detailed evaluation
// information including the reason for the result.
func (c *Client) VariationDetail(key string, ctx EvaluationContext, defaultValue any) EvaluationDetail {
	return c.core.evaluateFlag(key, ctx, defaultValue)
}

// Track records a custom event for analytics.
func (c *Client) Track(eventKey string, ctx EvaluationContext, metadata map[string]any) {
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
	c.core.ep.enqueue(sdkEvent{
		Type:      "Identify",
		FlagKey:   "$identify",
		UserID:    ctx.UserID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

// Flush sends all buffered events to the server immediately.
func (c *Client) Flush() {
	c.core.ep.flush()
}

// Initialized returns true if the client successfully completed initialization.
func (c *Client) Initialized() bool {
	return c.core.initialized
}

// Close decrements the refcount on the shared core. When the last handle for
// a given SDK key is closed, the core shuts down (stops streaming/polling,
// flushes events, removes itself from the factory cache). Double-close on the
// same handle is a no-op.
func (c *Client) Close() error {
	if atomic.CompareAndSwapInt32(&c.disposed, 0, 1) {
		c.core.release()
	}
	return nil
}

// evaluateFlag is the core evaluation method on sharedCore.
func (sc *sharedCore) evaluateFlag(key string, ctx EvaluationContext, defaultValue any) EvaluationDetail {
	flag, ok := sc.store.getFlag(key)
	if !ok {
		return EvaluationDetail{
			Value:  defaultValue,
			Reason: ReasonFlagNotFound,
		}
	}

	detail := evaluate(flag, ctx, sc.store.allSegments())

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
