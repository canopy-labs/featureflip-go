package featureflip

import (
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := defaultConfig()

	if cfg.baseURL != "https://eval.featureflip.io" {
		t.Errorf("baseURL = %q, want %q", cfg.baseURL, "https://eval.featureflip.io")
	}
	if !cfg.streaming {
		t.Error("streaming = false, want true")
	}
	if cfg.pollInterval != 30*time.Second {
		t.Errorf("pollInterval = %v, want %v", cfg.pollInterval, 30*time.Second)
	}
	if cfg.flushInterval != 30*time.Second {
		t.Errorf("flushInterval = %v, want %v", cfg.flushInterval, 30*time.Second)
	}
	if cfg.flushBatchSize != 100 {
		t.Errorf("flushBatchSize = %d, want %d", cfg.flushBatchSize, 100)
	}
	if cfg.initTimeout != 10*time.Second {
		t.Errorf("initTimeout = %v, want %v", cfg.initTimeout, 10*time.Second)
	}
	if cfg.connectTimeout != 5*time.Second {
		t.Errorf("connectTimeout = %v, want %v", cfg.connectTimeout, 5*time.Second)
	}
	if cfg.readTimeout != 10*time.Second {
		t.Errorf("readTimeout = %v, want %v", cfg.readTimeout, 10*time.Second)
	}
}

func TestOptions(t *testing.T) {
	cfg := defaultConfig()

	opts := []Option{
		WithBaseURL("http://localhost:8080"),
		WithStreaming(false),
		WithPollInterval(5 * time.Second),
		WithFlushInterval(10 * time.Second),
		WithFlushBatchSize(50),
		WithInitTimeout(3 * time.Second),
		WithConnectTimeout(2 * time.Second),
		WithReadTimeout(7 * time.Second),
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	if cfg.baseURL != "http://localhost:8080" {
		t.Errorf("baseURL = %q, want %q", cfg.baseURL, "http://localhost:8080")
	}
	if cfg.streaming {
		t.Error("streaming = true, want false")
	}
	if cfg.pollInterval != 5*time.Second {
		t.Errorf("pollInterval = %v, want %v", cfg.pollInterval, 5*time.Second)
	}
	if cfg.flushInterval != 10*time.Second {
		t.Errorf("flushInterval = %v, want %v", cfg.flushInterval, 10*time.Second)
	}
	if cfg.flushBatchSize != 50 {
		t.Errorf("flushBatchSize = %d, want %d", cfg.flushBatchSize, 50)
	}
	if cfg.initTimeout != 3*time.Second {
		t.Errorf("initTimeout = %v, want %v", cfg.initTimeout, 3*time.Second)
	}
	if cfg.connectTimeout != 2*time.Second {
		t.Errorf("connectTimeout = %v, want %v", cfg.connectTimeout, 2*time.Second)
	}
	if cfg.readTimeout != 7*time.Second {
		t.Errorf("readTimeout = %v, want %v", cfg.readTimeout, 7*time.Second)
	}
}

func TestPartialOptions(t *testing.T) {
	cfg := defaultConfig()

	// Apply only one option, verify others remain at defaults
	WithBaseURL("http://custom:9090")(&cfg)

	if cfg.baseURL != "http://custom:9090" {
		t.Errorf("baseURL = %q, want %q", cfg.baseURL, "http://custom:9090")
	}
	// All other fields should remain at defaults
	if !cfg.streaming {
		t.Error("streaming should remain true")
	}
	if cfg.pollInterval != 30*time.Second {
		t.Errorf("pollInterval should remain %v, got %v", 30*time.Second, cfg.pollInterval)
	}
	if cfg.flushBatchSize != 100 {
		t.Errorf("flushBatchSize should remain %d, got %d", 100, cfg.flushBatchSize)
	}
}
