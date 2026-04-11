package featureflip

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// flagServerOpts configures the test HTTP server.
type flagServerOpts struct {
	onEvents func([]sdkEvent)
}

// flagServer creates a test HTTP server that serves flag/segment data,
// accepts event POSTs, and keeps a stream endpoint open briefly.
func flagServer(flags []flagDTO, segments []segmentDTO, opts ...flagServerOpts) *httptest.Server {
	var opt flagServerOpts
	if len(opts) > 0 {
		opt = opts[0]
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sdk/flags":
			resp := getFlagsResponse{
				Environment: "test",
				Version:     1,
				Flags:       flags,
				Segments:    segments,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		case r.Method == http.MethodPost && r.URL.Path == "/v1/sdk/events":
			if opt.onEvents != nil {
				var req recordEventsRequest
				json.NewDecoder(r.Body).Decode(&req)
				opt.onEvents(req.Events)
			}
			w.WriteHeader(http.StatusAccepted)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/sdk/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			// Keep open briefly then close.
			time.Sleep(50 * time.Millisecond)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestNewClient_Initializes(t *testing.T) {
	flags := []flagDTO{
		{
			Key:     "test-flag",
			Version: 1,
			Type:    "Boolean",
			Enabled: true,
			Variations: []variationDTO{
				{Key: "true", Value: json.RawMessage(`true`)},
				{Key: "false", Value: json.RawMessage(`false`)},
			},
			Fallthrough:  serveConfig{Type: "Fixed", Variation: "true"},
			OffVariation: "false",
		},
	}
	server := flagServer(flags, nil)
	defer server.Close()

	client, err := Get("test-sdk-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	defer client.Close()

	if !client.Initialized() {
		t.Error("client should be initialized")
	}
}

func TestNewClient_EmptyKey_FallsBackToEnv(t *testing.T) {
	flags := []flagDTO{
		{
			Key:     "flag-1",
			Version: 1,
			Type:    "Boolean",
			Enabled: true,
			Variations: []variationDTO{
				{Key: "true", Value: json.RawMessage(`true`)},
			},
			Fallthrough:  serveConfig{Type: "Fixed", Variation: "true"},
			OffVariation: "false",
		},
	}
	server := flagServer(flags, nil)
	defer server.Close()

	t.Setenv("FEATUREFLIP_SDK_KEY", "env-sdk-key")

	client, err := Get("",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	defer client.Close()

	if !client.Initialized() {
		t.Error("client should be initialized when using env key")
	}
}

func TestNewClient_NoKey_ReturnsError(t *testing.T) {
	t.Setenv("FEATUREFLIP_SDK_KEY", "")

	_, err := Get("")
	if err == nil {
		t.Fatal("NewClient should return error when no SDK key is provided")
	}
}

func TestClient_BoolVariation(t *testing.T) {
	flags := []flagDTO{
		{
			Key:     "bool-flag",
			Version: 1,
			Type:    "Boolean",
			Enabled: true,
			Variations: []variationDTO{
				{Key: "true", Value: json.RawMessage(`true`)},
				{Key: "false", Value: json.RawMessage(`false`)},
			},
			Fallthrough:  serveConfig{Type: "Fixed", Variation: "true"},
			OffVariation: "false",
		},
	}
	server := flagServer(flags, nil)
	defer server.Close()

	client, err := Get("test-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}
	defer client.Close()

	ctx := EvaluationContext{UserID: "user-1"}
	result := client.BoolVariation("bool-flag", ctx, false)
	if result != true {
		t.Errorf("BoolVariation = %v, want true", result)
	}
}

func TestClient_BoolVariation_FlagNotFound(t *testing.T) {
	server := flagServer(nil, nil)
	defer server.Close()

	client, err := Get("test-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}
	defer client.Close()

	ctx := EvaluationContext{UserID: "user-1"}
	result := client.BoolVariation("nonexistent", ctx, false)
	if result != false {
		t.Errorf("BoolVariation = %v, want false (default)", result)
	}
}

func TestClient_StringVariation(t *testing.T) {
	flags := []flagDTO{
		{
			Key:     "string-flag",
			Version: 1,
			Type:    "String",
			Enabled: true,
			Variations: []variationDTO{
				{Key: "v1", Value: json.RawMessage(`"hello"`)},
				{Key: "v2", Value: json.RawMessage(`"world"`)},
			},
			Fallthrough:  serveConfig{Type: "Fixed", Variation: "v1"},
			OffVariation: "v2",
		},
	}
	server := flagServer(flags, nil)
	defer server.Close()

	client, err := Get("test-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}
	defer client.Close()

	ctx := EvaluationContext{UserID: "user-1"}
	result := client.StringVariation("string-flag", ctx, "default")
	if result != "hello" {
		t.Errorf("StringVariation = %q, want %q", result, "hello")
	}
}

func TestClient_Float64Variation(t *testing.T) {
	flags := []flagDTO{
		{
			Key:     "number-flag",
			Version: 1,
			Type:    "Number",
			Enabled: true,
			Variations: []variationDTO{
				{Key: "v1", Value: json.RawMessage(`42.5`)},
				{Key: "v2", Value: json.RawMessage(`0`)},
			},
			Fallthrough:  serveConfig{Type: "Fixed", Variation: "v1"},
			OffVariation: "v2",
		},
	}
	server := flagServer(flags, nil)
	defer server.Close()

	client, err := Get("test-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}
	defer client.Close()

	ctx := EvaluationContext{UserID: "user-1"}
	result := client.Float64Variation("number-flag", ctx, 0.0)
	if result != 42.5 {
		t.Errorf("Float64Variation = %v, want 42.5", result)
	}
}

func TestClient_VariationDetail(t *testing.T) {
	flags := []flagDTO{
		{
			Key:     "disabled-flag",
			Version: 1,
			Type:    "Boolean",
			Enabled: false,
			Variations: []variationDTO{
				{Key: "true", Value: json.RawMessage(`true`)},
				{Key: "false", Value: json.RawMessage(`false`)},
			},
			Fallthrough:  serveConfig{Type: "Fixed", Variation: "true"},
			OffVariation: "false",
		},
	}
	server := flagServer(flags, nil)
	defer server.Close()

	client, err := Get("test-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}
	defer client.Close()

	ctx := EvaluationContext{UserID: "user-1"}
	detail := client.VariationDetail("disabled-flag", ctx, true)
	if detail.Reason != ReasonFlagDisabled {
		t.Errorf("Reason = %q, want %q", detail.Reason, ReasonFlagDisabled)
	}
	if detail.Value != false {
		t.Errorf("Value = %v, want false", detail.Value)
	}
	if detail.Variation != "false" {
		t.Errorf("Variation = %q, want %q", detail.Variation, "false")
	}
}

func TestClient_VariationDetail_FlagNotFound(t *testing.T) {
	server := flagServer(nil, nil)
	defer server.Close()

	client, err := Get("test-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}
	defer client.Close()

	ctx := EvaluationContext{UserID: "user-1"}
	detail := client.VariationDetail("nonexistent", ctx, "fallback")
	if detail.Reason != ReasonFlagNotFound {
		t.Errorf("Reason = %q, want %q", detail.Reason, ReasonFlagNotFound)
	}
	if detail.Value != "fallback" {
		t.Errorf("Value = %v, want %q", detail.Value, "fallback")
	}
}

func TestClient_InitTimeout(t *testing.T) {
	// Server that sleeps longer than the init timeout.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := Get("test-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(200*time.Millisecond),
		WithConnectTimeout(100*time.Millisecond),
		WithReadTimeout(100*time.Millisecond),
	)
	if err == nil {
		t.Fatal("NewClient should return error on init timeout")
	}
}

func TestClient_Close_Idempotent(t *testing.T) {
	server := flagServer(nil, nil)
	defer server.Close()

	client, err := Get("test-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	// Close multiple times should not panic.
	if err := client.Close(); err != nil {
		t.Errorf("first Close returned error: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Errorf("second Close returned error: %v", err)
	}
}

func TestClient_Track_NoError(t *testing.T) {
	server := flagServer(nil, nil)
	defer server.Close()

	client, err := Get("test-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}
	defer client.Close()

	// Should not panic.
	ctx := EvaluationContext{UserID: "user-1"}
	client.Track("purchase", ctx, map[string]any{"amount": 99.99})
	client.Identify(ctx)
	client.Flush()
}

func TestClient_EventTypes_ArePascalCase(t *testing.T) {
	var mu sync.Mutex
	var captured []sdkEvent

	flags := []flagDTO{
		{
			Key:     "bool-flag",
			Version: 1,
			Type:    "Boolean",
			Enabled: true,
			Variations: []variationDTO{
				{Key: "true", Value: json.RawMessage(`true`)},
				{Key: "false", Value: json.RawMessage(`false`)},
			},
			Fallthrough:  serveConfig{Type: "Fixed", Variation: "true"},
			OffVariation: "false",
		},
	}

	server := flagServer(flags, nil, flagServerOpts{
		onEvents: func(events []sdkEvent) {
			mu.Lock()
			captured = append(captured, events...)
			mu.Unlock()
		},
	})
	defer server.Close()

	client, err := Get("test-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	ctx := EvaluationContext{UserID: "user-1"}

	// Trigger all three event types.
	client.BoolVariation("bool-flag", ctx, false)
	client.Track("purchase", ctx, map[string]any{"amount": 9.99})
	client.Identify(ctx)
	client.Flush()
	client.Close()

	mu.Lock()
	events := captured
	mu.Unlock()

	// The backend SdkEventType enum expects PascalCase values.
	// Only assert the three types we triggered are present with correct casing.
	wantTypes := map[string]bool{
		"Evaluation": false,
		"Custom":     false,
		"Identify":   false,
	}

	for _, e := range events {
		if _, ok := wantTypes[e.Type]; ok {
			wantTypes[e.Type] = true
		}
	}

	for typ, found := range wantTypes {
		if !found {
			t.Errorf("missing expected event type %q", typ)
		}
	}
}

func TestClient_Identify_IncludesFlagKey(t *testing.T) {
	var mu sync.Mutex
	var captured []sdkEvent

	server := flagServer(nil, nil, flagServerOpts{
		onEvents: func(events []sdkEvent) {
			mu.Lock()
			captured = append(captured, events...)
			mu.Unlock()
		},
	})
	defer server.Close()

	client, err := Get("test-key",
		WithBaseURL(server.URL),
		WithStreaming(false),
		WithInitTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	client.Identify(EvaluationContext{UserID: "user-1"})
	client.Flush()
	client.Close()

	mu.Lock()
	events := captured
	mu.Unlock()

	for _, e := range events {
		if e.Type == "Identify" {
			if e.FlagKey != "$identify" {
				t.Errorf("Identify event FlagKey = %q, want %q", e.FlagKey, "$identify")
			}
			return
		}
	}
	t.Error("no Identify event found")
}
