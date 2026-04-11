package featureflip

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGet_FreshKey(t *testing.T) {
	flags := []flagDTO{boolFlag("test-flag", true, "true", "false")}
	server := flagServer(flags, nil)
	defer server.Close()
	t.Cleanup(ResetForTesting)

	client, err := Get("fresh-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	defer client.Close()

	if !client.Initialized() {
		t.Error("client should be initialized")
	}
	if got := client.BoolVariation("test-flag", EvaluationContext{UserID: "u1"}, false); !got {
		t.Error("expected true")
	}
	if rc := DebugRefCount("fresh-key"); rc != 1 {
		t.Errorf("refcount = %d, want 1", rc)
	}
}

func TestGet_SameKey_SharesCore(t *testing.T) {
	flags := []flagDTO{boolFlag("test-flag", true, "true", "false")}
	server := flagServer(flags, nil)
	defer server.Close()
	t.Cleanup(ResetForTesting)

	h1, err := Get("dedup-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("Get h1 error: %v", err)
	}
	h2, err := Get("dedup-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("Get h2 error: %v", err)
	}

	if h1 == h2 {
		t.Error("handles should be distinct objects")
	}
	if rc := DebugRefCount("dedup-key"); rc != 2 {
		t.Errorf("refcount = %d, want 2", rc)
	}

	h1.Close()
	h2.Close()
}

func TestGet_DifferentKeys_IndependentCores(t *testing.T) {
	flags := []flagDTO{boolFlag("test-flag", true, "true", "false")}
	server := flagServer(flags, nil)
	defer server.Close()
	t.Cleanup(ResetForTesting)

	hA, err := Get("key-a",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	hB, err := Get("key-b",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}

	if DebugRefCount("key-a") != 1 || DebugRefCount("key-b") != 1 {
		t.Errorf("each key should have refcount 1")
	}
	if DebugLiveCoreCount() < 2 {
		t.Errorf("expected at least 2 live cores, got %d", DebugLiveCoreCount())
	}

	hA.Close()
	hB.Close()
}

func TestGet_AfterCloseOnly_ConstructsFresh(t *testing.T) {
	flags := []flagDTO{boolFlag("test-flag", true, "true", "false")}
	server := flagServer(flags, nil)
	defer server.Close()
	t.Cleanup(ResetForTesting)

	h1, err := Get("recreate-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	h1.Close()
	if rc := DebugRefCount("recreate-key"); rc != 0 {
		t.Errorf("refcount after close = %d, want 0", rc)
	}

	h2, err := Get("recreate-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	if !h2.Initialized() {
		t.Error("second client should be initialized")
	}
}

func TestGet_CloseOneOfTwo_OtherStillWorks(t *testing.T) {
	flags := []flagDTO{boolFlag("test-flag", true, "true", "false")}
	server := flagServer(flags, nil)
	defer server.Close()
	t.Cleanup(ResetForTesting)

	h1, _ := Get("shared-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	h2, _ := Get("shared-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)

	if rc := DebugRefCount("shared-key"); rc != 2 {
		t.Fatalf("refcount = %d, want 2", rc)
	}
	h1.Close()
	if rc := DebugRefCount("shared-key"); rc != 1 {
		t.Fatalf("refcount after h1.Close = %d, want 1", rc)
	}
	// h2 still functional
	if got := h2.BoolVariation("test-flag", EvaluationContext{UserID: "u1"}, false); !got {
		t.Error("h2 should still evaluate flags")
	}
	h2.Close()
	if rc := DebugRefCount("shared-key"); rc != 0 {
		t.Errorf("refcount = %d, want 0", rc)
	}
}

func TestGet_DoubleClose_Idempotent(t *testing.T) {
	flags := []flagDTO{boolFlag("test-flag", true, "true", "false")}
	server := flagServer(flags, nil)
	defer server.Close()
	t.Cleanup(ResetForTesting)

	h1, _ := Get("double-close-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	h2, _ := Get("double-close-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)

	h1.Close()
	h1.Close()
	h1.Close()

	if rc := DebugRefCount("double-close-key"); rc != 1 {
		t.Errorf("refcount = %d, want 1 (h1 closed 3x should decrement once)", rc)
	}
	h2.Close()
}

func TestGet_32ConcurrentCalls_ShareOneCore(t *testing.T) {
	flags := []flagDTO{boolFlag("test-flag", true, "true", "false")}
	server := flagServer(flags, nil)
	defer server.Close()
	t.Cleanup(ResetForTesting)

	const n = 32
	var (
		wg      sync.WaitGroup
		handles [n]*Client
		errors  [n]error
		mu      sync.Mutex
		fails   int32
	)

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			c, err := Get("concurrent-key",
				WithBaseURL(server.URL),
				WithStreaming(false),
				WithInitTimeout(5*time.Second),
			)
			mu.Lock()
			handles[idx] = c
			errors[idx] = err
			mu.Unlock()
			if err != nil {
				atomic.AddInt32(&fails, 1)
			}
		}(i)
	}
	wg.Wait()

	if f := atomic.LoadInt32(&fails); f > 0 {
		for i, err := range errors {
			if err != nil {
				t.Errorf("Get[%d] error: %v", i, err)
			}
		}
		t.FailNow()
	}

	if rc := DebugRefCount("concurrent-key"); rc != n {
		t.Errorf("refcount = %d, want %d", rc, n)
	}

	for _, h := range handles {
		if h != nil {
			h.Close()
		}
	}
	if rc := DebugRefCount("concurrent-key"); rc != 0 {
		t.Errorf("refcount after close all = %d, want 0", rc)
	}
}

func TestGet_EmptyKey_ReturnsError(t *testing.T) {
	t.Setenv("FEATUREFLIP_SDK_KEY", "")
	_, err := Get("")
	if err == nil {
		t.Fatal("Get with empty key should return error")
	}
}

func TestForTesting_NotInCache(t *testing.T) {
	t.Cleanup(ResetForTesting)
	before := DebugLiveCoreCount()
	client := ForTesting(map[string]any{"my-flag": true})
	defer client.Close()
	if DebugLiveCoreCount() != before {
		t.Error("ForTesting should not register in the factory cache")
	}
	if !client.BoolVariation("my-flag", EvaluationContext{}, false) {
		t.Error("ForTesting flag should evaluate")
	}
}

