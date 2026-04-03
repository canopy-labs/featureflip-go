package featureflip

import "time"

type config struct {
	baseURL        string
	streaming      bool
	pollInterval   time.Duration
	flushInterval  time.Duration
	flushBatchSize int
	initTimeout    time.Duration
	connectTimeout time.Duration
	readTimeout    time.Duration
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
