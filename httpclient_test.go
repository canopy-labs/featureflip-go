package featureflip

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPClient_GetFlags(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotUA string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")

		resp := getFlagsResponse{
			Environment: "test-env",
			Version:     1,
			Flags: []flagDTO{
				{Key: "flag-1", Version: 1, Enabled: true, Type: "Boolean"},
			},
			Segments: []segmentDTO{
				{Key: "seg-1", Version: 1},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key-123", cfg)

	result, err := hc.getFlags()
	if err != nil {
		t.Fatalf("getFlags returned error: %v", err)
	}

	// Verify request
	if gotPath != "/v1/sdk/flags" {
		t.Errorf("path = %q, want /v1/sdk/flags", gotPath)
	}
	if gotAuth != "sdk-key-123" {
		t.Errorf("Authorization = %q, want sdk-key-123", gotAuth)
	}
	if gotUA != "featureflip-go/"+sdkVersion {
		t.Errorf("User-Agent = %q, want featureflip-go/%s", gotUA, sdkVersion)
	}

	// Verify response parsing
	if result.Environment != "test-env" {
		t.Errorf("Environment = %q, want test-env", result.Environment)
	}
	if result.Version != 1 {
		t.Errorf("Version = %d, want 1", result.Version)
	}
	if len(result.Flags) != 1 {
		t.Fatalf("Flags count = %d, want 1", len(result.Flags))
	}
	if result.Flags[0].Key != "flag-1" {
		t.Errorf("Flag key = %q, want flag-1", result.Flags[0].Key)
	}
	if len(result.Segments) != 1 {
		t.Fatalf("Segments count = %d, want 1", len(result.Segments))
	}
	if result.Segments[0].Key != "seg-1" {
		t.Errorf("Segment key = %q, want seg-1", result.Segments[0].Key)
	}
}

func TestHTTPClient_GetFlag(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path

		flag := flagDTO{Key: "my-flag", Version: 3, Enabled: true, Type: "Boolean"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(flag)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key-456", cfg)

	result, err := hc.getFlag("my-flag")
	if err != nil {
		t.Fatalf("getFlag returned error: %v", err)
	}

	if gotPath != "/v1/sdk/flags/my-flag" {
		t.Errorf("path = %q, want /v1/sdk/flags/my-flag", gotPath)
	}
	if result.Key != "my-flag" {
		t.Errorf("Key = %q, want my-flag", result.Key)
	}
	if result.Version != 3 {
		t.Errorf("Version = %d, want 3", result.Version)
	}
}

func TestHTTPClient_GetFlag_EscapesKey(t *testing.T) {
	var gotRequestURI string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequestURI = r.RequestURI

		flag := flagDTO{Key: "flag/with spaces", Version: 1, Enabled: true}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(flag)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	_, err := hc.getFlag("flag/with spaces")
	if err != nil {
		t.Fatalf("getFlag returned error: %v", err)
	}

	// url.PathEscape encodes "/" as %2F and " " as %20
	// Use RequestURI since r.URL.Path is automatically decoded by Go's HTTP server.
	expected := "/v1/sdk/flags/flag%2Fwith%20spaces"
	if gotRequestURI != expected {
		t.Errorf("RequestURI = %q, want %q", gotRequestURI, expected)
	}
}

func TestHTTPClient_PostEvents(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotContentType string
	var gotBody recordEventsRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")

		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)

		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key-789", cfg)

	events := []sdkEvent{
		{Type: "evaluation", FlagKey: "flag-1", Variation: "true", Timestamp: "2024-01-01T00:00:00Z"},
		{Type: "evaluation", FlagKey: "flag-2", Variation: "v1", Timestamp: "2024-01-01T00:00:01Z"},
	}

	err := hc.postEvents(events)
	if err != nil {
		t.Fatalf("postEvents returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/sdk/events" {
		t.Errorf("path = %q, want /v1/sdk/events", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if len(gotBody.Events) != 2 {
		t.Fatalf("events count = %d, want 2", len(gotBody.Events))
	}
	if gotBody.Events[0].FlagKey != "flag-1" {
		t.Errorf("event[0].FlagKey = %q, want flag-1", gotBody.Events[0].FlagKey)
	}
	if gotBody.Events[1].FlagKey != "flag-2" {
		t.Errorf("event[1].FlagKey = %q, want flag-2", gotBody.Events[1].FlagKey)
	}
}

func TestHTTPClient_GetFlags_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	_, err := hc.getFlags()
	if err == nil {
		t.Fatal("getFlags should return error on 500")
	}

	expected := "unexpected status 500"
	if got := err.Error(); !contains(got, expected) {
		t.Errorf("error = %q, should contain %q", got, expected)
	}
}

func TestHTTPClient_GetFlag_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	_, err := hc.getFlag("nonexistent")
	if err == nil {
		t.Fatal("getFlag should return error on 404")
	}

	expected := "unexpected status 404"
	if got := err.Error(); !contains(got, expected) {
		t.Errorf("error = %q, should contain %q", got, expected)
	}
}

func TestHTTPClient_PostEvents_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	err := hc.postEvents([]sdkEvent{{Type: "evaluation", FlagKey: "f", Timestamp: "t"}})
	if err == nil {
		t.Fatal("postEvents should return error on 500")
	}
}

func TestHTTPClient_NewStreamRequest(t *testing.T) {
	cfg := defaultConfig()
	cfg.baseURL = "https://eval.example.com"
	hc := newHTTPClient("sdk-key-stream", cfg)

	req, err := hc.newStreamRequest()
	if err != nil {
		t.Fatalf("newStreamRequest returned error: %v", err)
	}

	if req.URL.String() != "https://eval.example.com/v1/sdk/stream" {
		t.Errorf("URL = %q, want https://eval.example.com/v1/sdk/stream", req.URL.String())
	}
	if req.Header.Get("Authorization") != "sdk-key-stream" {
		t.Errorf("Authorization = %q, want sdk-key-stream", req.Header.Get("Authorization"))
	}
	if req.Header.Get("Accept") != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", req.Header.Get("Accept"))
	}
	if req.Header.Get("User-Agent") != "featureflip-go/"+sdkVersion {
		t.Errorf("User-Agent = %q, want featureflip-go/%s", req.Header.Get("User-Agent"), sdkVersion)
	}
}

func TestHTTPClient_StreamHTTPClient(t *testing.T) {
	cfg := defaultConfig()
	hc := newHTTPClient("sdk-key", cfg)

	streamClient := hc.streamHTTPClient(5 * 1000 * 1000 * 1000) // 5s in nanoseconds
	if streamClient == nil {
		t.Fatal("streamHTTPClient returned nil")
	}
	if streamClient.Timeout != 0 {
		t.Errorf("stream client Timeout = %v, want 0 (no timeout)", streamClient.Timeout)
	}
}

// contains checks if s contains substr (simple helper to avoid strings import).
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
