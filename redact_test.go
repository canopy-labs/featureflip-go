package featureflip

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	sentinelServerKey = "sdk_server_SentinelKeyMustNeverAppearInLogs-0123456789"
	sentinelClientKey = "sdk_client_SentinelKeyMustNeverAppearInLogs-0123456789"
)

// captureSDKLog routes the standard logger into a buffer for the test.
type sdkLogCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *sdkLogCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *sdkLogCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func captureSDKLog(t *testing.T) *sdkLogCapture {
	t.Helper()
	c := &sdkLogCapture{}
	origOut, origFlags := log.Writer(), log.Flags()
	log.SetOutput(c)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(origOut)
		log.SetFlags(origFlags)
	})
	return c
}

func TestLogf_RedactsKeyFromAnErrorArgument(t *testing.T) {
	logs := captureSDKLog(t)
	registerRedactedKey(sentinelServerKey)

	// The shape this exists for: an HTTP stack whose error quotes the
	// Authorization header it was asked to send.
	err := fmt.Errorf("Get \"https://x/v1/sdk/flags\": request header Authorization: %s", sentinelServerKey)
	logf("[featureflip] initial fetch failed (%v), serving defaults", err)

	out := logs.String()
	if strings.Contains(out, sentinelServerKey) {
		t.Fatalf("key leaked: %q", out)
	}
	if !strings.Contains(out, "Authorization: [redacted]") {
		t.Fatalf("expected the key replaced by [redacted], got %q", out)
	}
}

func TestLogf_RedactsKeyInEveryFormatVerb(t *testing.T) {
	logs := captureSDKLog(t)
	registerRedactedKey(sentinelServerKey)

	logf("quoted %q, string %s, struct %+v, err %v", sentinelServerKey, sentinelServerKey,
		struct{ K string }{sentinelServerKey}, errors.New(sentinelServerKey))

	if out := logs.String(); strings.Contains(out, sentinelServerKey) {
		t.Fatalf("key leaked: %q", out)
	}
}

func TestLogf_ShortKeyIsLeftAlone(t *testing.T) {
	logs := captureSDKLog(t)
	short := "k"
	registerRedactedKey(short)

	logf("skipped a malformed flag %s", short)

	if out := logs.String(); !strings.Contains(out, "skipped a malformed flag k") {
		t.Fatalf("a short key must not be redacted, got %q", out)
	}
}

func TestLogf_KeyStaysRedactedAfterItsCoreShutsDown(t *testing.T) {
	logs := captureSDKLog(t)
	core := newSharedCore(sentinelServerKey, defaultConfig())
	core.release()

	// Stream and poll goroutines are not joined by shutdown, so one can log late.
	logf("late line %s", sentinelServerKey)
	if strings.Contains(logs.String(), sentinelServerKey) {
		t.Fatal("key must stay redacted after shutdown")
	}
}

// keyQuotingTransport fails every request with an error that quotes the
// Authorization header, as a third-party HTTP stack might.
type keyQuotingTransport struct{ key string }

func (tr keyQuotingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("transport failure sending header Authorization: %s to %s", tr.key, req.URL.Host)
}

// statusErrorTransport performs the real round trip, then, for a non-200
// answer, fails with an error quoting the Authorization header so the
// 401/500 cases prove redaction rather than clean call sites.
type statusErrorTransport struct {
	inner http.RoundTripper
	key   string
}

func (tr statusErrorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := tr.inner.RoundTrip(req)
	if err == nil && resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("status %d for request with Authorization: %s", resp.StatusCode, tr.key)
	}
	return resp, err
}

// driveClient exercises the noisy paths and fails on any exception message
// that carries the key.
func driveClient(t *testing.T, key string, client *Client) {
	t.Helper()
	ctx := EvaluationContext{UserID: "u1"}

	client.BoolVariation("unknown-flag", ctx, false)
	client.StringVariation("unknown-flag", ctx, "d")
	client.Float64Variation("bool-flag", ctx, 1) // type mismatch
	client.StringVariation("bool-flag", ctx, "d")
	client.VariationDetail("unknown-flag", ctx, false)
	client.Track("clicked", ctx, map[string]any{"n": 1})
	client.Identify(ctx)
	client.Flush()
	client.StartupInfo()
	if err := client.Close(); err != nil && strings.Contains(err.Error(), key) {
		t.Fatalf("Close error carries the key: %v", err)
	}
}

const sentinelFlagsPayload = `{"environment":"e","version":1,"segments":[],"flags":[
 {"key":"bool-flag","version":1,"type":"Boolean","enabled":true,
  "variations":[{"key":"on","value":true},{"key":"off","value":false}],
  "fallthrough":{"type":"Fixed","variation":"on"},"offVariation":"off","rules":[]},
 {"key":"future-flag","version":1,"type":"Boolean","enabled":true,
  "variations":[{"key":"on","value":true}],
  "fallthrough":{"type":"SomeFutureServeType","variation":"on"},"offVariation":"on","rules":[]}]}`

func TestSentinelKey_NeverReachesLogsOrErrors(t *testing.T) {
	for _, key := range []string{sentinelServerKey, sentinelClientKey} {
		for _, status := range []int{200, 401, 500} {
			t.Run(fmt.Sprintf("%s/status-%d", key[:10], status), func(t *testing.T) {
				logs := captureSDKLog(t)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/events") {
						w.WriteHeader(500)
						return
					}
					if status != 200 {
						w.WriteHeader(status)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, sentinelFlagsPayload)
				}))
				defer srv.Close()

				opts := []Option{WithBaseURL(srv.URL), WithStreaming(false),
					WithPollInterval(20 * time.Millisecond), WithInitTimeout(2 * time.Second)}
				client, err := Get(key, opts...)
				if err != nil {
					if strings.Contains(err.Error(), key) {
						t.Fatalf("Get error carries the key: %v", err)
					}
					return
				}
				// Same key, different options: the "options ignored" warning.
				other, err := Get(key, WithBaseURL(srv.URL), WithStreaming(true))
				if err != nil {
					t.Fatalf("second Get: %v", err)
				}
				time.Sleep(80 * time.Millisecond) // let a few polls fail
				driveClient(t, key, other)
				driveClient(t, key, client)

				if out := logs.String(); strings.Contains(out, key) {
					t.Fatalf("key leaked into the log:\n%s", out)
				}
			})
		}
	}
}

func TestSentinelKey_TransportErrorQuotingTheKeyIsRedacted(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			logs := captureSDKLog(t)
			cfg := defaultConfig()
			cfg.streaming = streaming
			cfg.pollInterval = 20 * time.Millisecond
			cfg.flushInterval = time.Hour
			core := newSharedCore(sentinelServerKey, cfg)
			core.hc.client.Transport = keyQuotingTransport{key: sentinelServerKey}
			client := &Client{core: core}

			if err := core.initializeOnce(); err != nil && strings.Contains(err.Error(), sentinelServerKey) {
				t.Fatalf("init error carries the key: %v", err)
			}
			time.Sleep(100 * time.Millisecond)
			driveClient(t, sentinelServerKey, client)

			out := logs.String()
			if strings.Contains(out, sentinelServerKey) {
				t.Fatalf("key leaked into the log:\n%s", out)
			}
			if !strings.Contains(out, "[redacted]") {
				t.Fatalf("expected the transport error to be logged with the key redacted, got:\n%s", out)
			}
		})
	}
}

func TestSentinelKey_StatusFailuresQuotingTheKeyAreRedacted(t *testing.T) {
	for _, status := range []int{200, 401, 500} {
		t.Run(fmt.Sprintf("status-%d", status), func(t *testing.T) {
			logs := captureSDKLog(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status != 200 || strings.HasSuffix(r.URL.Path, "/events") {
					w.WriteHeader(status)
					return
				}
				// A flag keyed by the sentinel is dropped with its key logged.
				fmt.Fprint(w, strings.Replace(sentinelFlagsPayload, "future-flag", sentinelServerKey, 1))
			}))
			defer srv.Close()

			cfg := defaultConfig()
			cfg.baseURL = srv.URL
			cfg.streaming = false
			cfg.pollInterval = 20 * time.Millisecond
			cfg.flushInterval = time.Hour
			core := newSharedCore(sentinelServerKey, cfg)
			core.hc.client.Transport = statusErrorTransport{inner: core.hc.client.Transport, key: sentinelServerKey}
			client := &Client{core: core}

			if err := core.initializeOnce(); err != nil && strings.Contains(err.Error(), sentinelServerKey) {
				t.Fatalf("init error carries the key: %v", err)
			}
			time.Sleep(80 * time.Millisecond)
			driveClient(t, sentinelServerKey, client)

			out := logs.String()
			if strings.Contains(out, sentinelServerKey) {
				t.Fatalf("key leaked into the log:\n%s", out)
			}
			if !strings.Contains(out, "[redacted]") {
				t.Fatalf("expected a redacted key in the log, got:\n%s", out)
			}
		})
	}
}
