package featureflip

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The SDK token plus a wrapper's token in the User-Agent (#4149, contract in
// #4138). featureflip-go/<version> was already sent; what was missing is a way
// for a library that wraps the SDK (the OpenFeature provider) to name itself.
// Asserted on the wire for the flag fetch and event post, and on the stream
// request, since all three go through setHeaders.

const sdkToken = "featureflip-go/" + sdkVersion

// agentsFor drives one request down each path and returns each one's User-Agent.
func agentsFor(t *testing.T, opts ...Option) map[string]string {
	t.Helper()
	var mu sync.Mutex
	got := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.URL.Path] = r.Header.Get("User-Agent")
		mu.Unlock()
		if r.URL.Path == "/v1/sdk/flags" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"environment":"e","version":1,"flags":[],"segments":[]}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	cfg.baseURL = server.URL
	hc := newHTTPClient("sdk-key", cfg)

	if _, err := hc.getFlags(); err != nil {
		t.Fatalf("getFlags: %v", err)
	}
	if err := hc.postEvents([]sdkEvent{{Type: "Custom", FlagKey: "f"}}); err != nil {
		t.Fatalf("postEvents: %v", err)
	}
	req, err := hc.newStreamRequest()
	if err != nil {
		t.Fatalf("newStreamRequest: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	return map[string]string{
		"flags":  got["/v1/sdk/flags"],
		"events": got["/v1/sdk/events"],
		"stream": req.Header.Get("User-Agent"),
	}
}

func TestSdkIdentity_NoWrapperSendsTheBareSdkToken(t *testing.T) {
	for path, ua := range agentsFor(t) {
		if ua != sdkToken {
			t.Errorf("%s User-Agent = %q, want %q", path, ua, sdkToken)
		}
	}
}

func TestSdkIdentity_WrapperIsAppendedOnEveryPath(t *testing.T) {
	want := sdkToken + " featureflip-openfeature-go/1.2.3"
	for path, ua := range agentsFor(t, WithWrapper("featureflip-openfeature-go", "1.2.3")) {
		if ua != want {
			t.Errorf("%s User-Agent = %q, want %q", path, ua, want)
		}
	}
}

func TestSdkIdentity_WrapperWithoutAVersionIsABareName(t *testing.T) {
	if ua := agentsFor(t, WithWrapper("my-wrapper", ""))["flags"]; ua != sdkToken+" my-wrapper" {
		t.Errorf("User-Agent = %q, want %q", ua, sdkToken+" my-wrapper")
	}
}

func TestSdkIdentity_InvalidWrapperIsDroppedWithAWarning(t *testing.T) {
	// A space is not a legal product-token character, and net/http would send
	// it inside the header value regardless. Identity metadata must never stop
	// the client starting, so it is dropped and said so.
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	if ua := agentsFor(t, WithWrapper("bad name", "1.0"))["flags"]; ua != sdkToken {
		t.Errorf("User-Agent = %q, want %q", ua, sdkToken)
	}
	if !strings.Contains(buf.String(), "bad name") {
		t.Errorf("expected a warning naming the wrapper, got %q", buf.String())
	}
}

func TestSdkIdentity_WrapperDoesNotCountAsADifferentConfig(t *testing.T) {
	// It identifies the caller rather than changing behavior, so the provider
	// sharing an SDK key with the host application must not trip the warning.
	a := defaultConfig()
	b := defaultConfig()
	WithWrapper("w", "1")(&b)
	if !configsEqual(a, b) {
		t.Error("configsEqual = false, want true when only the wrapper differs")
	}
}
