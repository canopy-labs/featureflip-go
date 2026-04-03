// Package featureflip provides a Go SDK for Featureflip feature flag evaluation.
package featureflip

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// Client is the main entry point for the Featureflip Go SDK.
// It manages flag evaluation, event tracking, and real-time updates.
type Client struct {
	cfg         config
	store       *store
	hc          *httpClient
	ep          *eventProcessor
	initialized bool
	closeOnce   sync.Once
	stopStream  func()
	stopPoll    func()
}

// NewClient creates a new Featureflip client. It performs an initial flag fetch
// (blocking up to initTimeout) and starts background streaming or polling.
//
// If sdkKey is empty, the FEATUREFLIP_SDK_KEY environment variable is used.
func NewClient(sdkKey string, opts ...Option) (*Client, error) {
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

	hc := newHTTPClient(sdkKey, cfg)
	s := newStore()

	// Initial fetch with timeout.
	type fetchResult struct {
		resp *getFlagsResponse
		err  error
	}
	ch := make(chan fetchResult, 1)
	go func() {
		resp, err := hc.getFlags()
		ch <- fetchResult{resp: resp, err: err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), cfg.initTimeout)
	defer cancel()

	select {
	case result := <-ch:
		if result.err != nil {
			return nil, fmt.Errorf("featureflip: initial fetch failed: %w", result.err)
		}
		s.setAll(result.resp.Flags, result.resp.Segments)
	case <-ctx.Done():
		return nil, fmt.Errorf("featureflip: initial fetch timed out after %s", cfg.initTimeout)
	}

	// Start event processor.
	ep := newEventProcessor(hc, cfg.flushBatchSize, cfg.flushInterval)
	ep.start()

	c := &Client{
		cfg:         cfg,
		store:       s,
		hc:          hc,
		ep:          ep,
		initialized: true,
	}

	// Start streaming or polling for real-time updates.
	if cfg.streaming {
		ss := newStreamSource(hc, s, nil)
		ss.connectTimeout = cfg.connectTimeout
		go ss.run()
		c.stopStream = ss.stop
	} else {
		ps := newPollSource(hc, s, cfg.pollInterval)
		go ps.run()
		c.stopPoll = ps.stop
	}

	return c, nil
}

// BoolVariation evaluates a boolean feature flag. Returns defaultValue if the
// flag is not found or an error occurs.
func (c *Client) BoolVariation(key string, ctx EvaluationContext, defaultValue bool) bool {
	detail := c.evaluateFlag(key, ctx, defaultValue)
	if v, ok := detail.Value.(bool); ok {
		return v
	}
	return defaultValue
}

// StringVariation evaluates a string feature flag. Returns defaultValue if the
// flag is not found or an error occurs.
func (c *Client) StringVariation(key string, ctx EvaluationContext, defaultValue string) string {
	detail := c.evaluateFlag(key, ctx, defaultValue)
	if v, ok := detail.Value.(string); ok {
		return v
	}
	return defaultValue
}

// Float64Variation evaluates a numeric feature flag. Returns defaultValue if the
// flag is not found or an error occurs.
func (c *Client) Float64Variation(key string, ctx EvaluationContext, defaultValue float64) float64 {
	detail := c.evaluateFlag(key, ctx, defaultValue)
	if v, ok := detail.Value.(float64); ok {
		return v
	}
	return defaultValue
}

// JSONVariation evaluates a JSON feature flag. Returns defaultValue if the
// flag is not found or an error occurs.
func (c *Client) JSONVariation(key string, ctx EvaluationContext, defaultValue any) any {
	detail := c.evaluateFlag(key, ctx, defaultValue)
	if detail.Reason == ReasonFlagNotFound {
		return defaultValue
	}
	return detail.Value
}

// VariationDetail evaluates a feature flag and returns detailed evaluation
// information including the reason for the result.
func (c *Client) VariationDetail(key string, ctx EvaluationContext, defaultValue any) EvaluationDetail {
	return c.evaluateFlag(key, ctx, defaultValue)
}

// evaluateFlag is the core evaluation method. It looks up the flag, evaluates it,
// and enqueues an evaluation event.
func (c *Client) evaluateFlag(key string, ctx EvaluationContext, defaultValue any) EvaluationDetail {
	flag, ok := c.store.getFlag(key)
	if !ok {
		return EvaluationDetail{
			Value:  defaultValue,
			Reason: ReasonFlagNotFound,
		}
	}

	detail := evaluate(flag, ctx, c.store.allSegments())

	c.ep.enqueue(sdkEvent{
		Type:      "Evaluation",
		FlagKey:   key,
		UserID:    ctx.UserID,
		Variation: detail.Variation,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	return detail
}

// Track records a custom event for analytics.
func (c *Client) Track(eventKey string, ctx EvaluationContext, metadata map[string]any) {
	c.ep.enqueue(sdkEvent{
		Type:      "Custom",
		FlagKey:   eventKey,
		UserID:    ctx.UserID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Metadata:  metadata,
	})
}

// Identify records an identify event for user association.
func (c *Client) Identify(ctx EvaluationContext) {
	c.ep.enqueue(sdkEvent{
		Type:      "Identify",
		FlagKey:   "$identify",
		UserID:    ctx.UserID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

// Flush sends all buffered events to the server immediately.
func (c *Client) Flush() {
	c.ep.flush()
}

// Initialized returns true if the client successfully completed initialization.
func (c *Client) Initialized() bool {
	return c.initialized
}

// Close shuts down the client, stopping background goroutines and flushing
// any remaining events. It is safe to call multiple times.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		if c.stopStream != nil {
			c.stopStream()
		}
		if c.stopPoll != nil {
			c.stopPoll()
		}
		c.ep.stop()
	})
	return nil
}
