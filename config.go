package featureflip

import (
	"errors"
	"time"
)

type config struct {
	baseURL        string
	streaming      bool
	pollInterval   time.Duration
	flushInterval  time.Duration
	flushBatchSize int
	initTimeout    time.Duration
	connectTimeout time.Duration
	readTimeout    time.Duration
	inspectors     []EvaluationInspector
	wrapperName    string
	wrapperVersion string

	// snapshotStore is nil unless [WithSnapshotStore] is passed. maxSnapshotAge is
	// zero until [WithMaxSnapshotAge] is passed, which lets configsEqual tell an
	// omitted option from an explicit one.
	snapshotStore  SnapshotStore
	maxSnapshotAge time.Duration

	// optionErr is the first invalid option value seen. [Get] returns it.
	optionErr error
}

// effectiveMaxSnapshotAge is the configured maximum snapshot age, or the default.
func (c config) effectiveMaxSnapshotAge() time.Duration {
	if c.maxSnapshotAge == 0 {
		return defaultMaxSnapshotAge
	}
	return c.maxSnapshotAge
}

func defaultConfig() config {
	return config{
		baseURL:        "https://eval.featureflip.io",
		streaming:      true,
		pollInterval:   30 * time.Second,
		flushInterval:  30 * time.Second,
		flushBatchSize: 100,
		initTimeout:    10 * time.Second,
		connectTimeout: 5 * time.Second,
		readTimeout:    10 * time.Second,
	}
}

// Option configures the Featureflip client.
type Option func(*config)

// WithBaseURL sets the base URL of the evaluation API.
func WithBaseURL(url string) Option { return func(c *config) { c.baseURL = url } }

// WithStreaming enables or disables SSE streaming for real-time flag updates.
func WithStreaming(enabled bool) Option { return func(c *config) { c.streaming = enabled } }

// WithPollInterval sets how often the client polls for flag updates when streaming is disabled.
func WithPollInterval(d time.Duration) Option { return func(c *config) { c.pollInterval = d } }

// WithFlushInterval sets how often buffered events are flushed to the server.
func WithFlushInterval(d time.Duration) Option { return func(c *config) { c.flushInterval = d } }

// WithFlushBatchSize sets the maximum number of events to send in a single flush.
func WithFlushBatchSize(n int) Option { return func(c *config) { c.flushBatchSize = n } }

// WithInitTimeout sets the maximum time to wait for initial flag data on client startup.
func WithInitTimeout(d time.Duration) Option { return func(c *config) { c.initTimeout = d } }

// WithConnectTimeout sets the timeout for establishing HTTP connections.
func WithConnectTimeout(d time.Duration) Option { return func(c *config) { c.connectTimeout = d } }

// WithReadTimeout sets the timeout for reading HTTP responses.
func WithReadTimeout(d time.Duration) Option { return func(c *config) { c.readTimeout = d } }

// WithInspectors registers [EvaluationInspector] callbacks that observe every
// flag evaluation in-process, synchronously. May be called more than once;
// inspectors accumulate. Nil entries are dropped.
//
// Like every other option, inspectors are honored only on the first [Get] for a
// given SDK key — the cached shared core's inspectors are preserved and a later
// Get's inspectors are ignored.
func WithInspectors(inspectors ...EvaluationInspector) Option {
	return func(c *config) { c.inspectors = append(c.inspectors, inspectors...) }
}

// WithWrapper identifies a library that wraps this SDK, such as an OpenFeature
// provider. Its token is sent after the SDK's own in the User-Agent
// ("featureflip-go/<version> my-wrapper/1.0.0") so usage through the wrapper can
// be counted. Both parts must be valid HTTP tokens; an invalid wrapper is ignored
// with a logged warning. version may be empty. Leave unset when calling the SDK
// directly.
//
// Not part of the "different options" check: it identifies the caller rather
// than changing behavior. Like every other option, honored only on the first
// [Get] for a given SDK key.
func WithWrapper(name, version string) Option {
	return func(c *config) {
		c.wrapperName = name
		c.wrapperVersion = version
	}
}

// WithSnapshotStore enables starting from a last-known configuration. After every
// full update the client applies (the initial fetch, a poll, a stream resync) it
// writes its full flag state to store in the background, skipping the write when
// the state is unchanged and the last write began less than five minutes ago.
// Single-flag stream updates and deletes write only when the client started live.
// Writes never block evaluation. If the initial fetch fails or times out, the client reads the store
// and, when it holds a usable snapshot younger than [WithMaxSnapshotAge], starts
// from it. The live data source still starts and takes over when the API returns.
//
// Off by default: a snapshot contains targeting rules, including customer
// attribute values, so nothing is written unless you opt in. See
// [FileSnapshotStore] for the built-in file store and [SnapshotStore] for the
// interface to implement for any other backend.
//
// The store is read only after a failed or timed-out initial fetch, and the read
// is bounded by the init timeout too, so the worst-case [Get] takes twice
// [WithInitTimeout]. With a store configured a failed start with no usable
// snapshot still returns an initialized client that serves caller defaults.
// [Client.StartupInfo] reports which case happened.
//
// Like every other option, the store is honored only on the first [Get] for a
// given SDK key. A later Get that passes a different store instance logs the
// "different options" warning and the cached core's store is kept. A store is not
// part of the different-options comparison when a later Get omits it.
func WithSnapshotStore(store SnapshotStore) Option {
	return func(c *config) { c.snapshotStore = store }
}

// WithMaxSnapshotAge sets the oldest snapshot the client will start from. The
// default is 24 hours. The value has whole-second granularity (a fraction of a
// second is truncated) and must be at least one second: [Get] returns an error
// otherwise. There is no "never expire" value. An older snapshot is ignored, the
// client serves caller defaults, and [Client.StartupInfo] reports
// snapshot-expired.
//
// Part of the different-options comparison when passed again on a later [Get].
func WithMaxSnapshotAge(d time.Duration) Option {
	return func(c *config) {
		if d < time.Second {
			if c.optionErr == nil {
				c.optionErr = errors.New("featureflip: WithMaxSnapshotAge must be at least one second")
			}
			return
		}
		c.maxSnapshotAge = d
	}
}
