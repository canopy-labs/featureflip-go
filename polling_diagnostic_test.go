package featureflip

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The polling path must distinguish the two failures its single `err` covers.
//
// A transport failure self-heals: the next tick retries, and logging every
// blip would flood the logs of a briefly-offline client. A decode failure does
// not self-heal — the server keeps sending the same payload, so the client
// serves stale config (or caller defaults) indefinitely while saying nothing.
//
// That is the exact failure mode the streaming path's comment says silence
// caused: the enums-as-integers server bug (#2279) went unnoticed for so long
// because nothing announced the dropped frames. Streaming logs; polling did
// not, leaving one SDK's two transports disagreeing — a divergence class
// packages/CLAUDE.md calls out on its own (#2396).
//
// The store must be left intact in both cases; that guard is #2288's and is
// asserted here too so a "simplification" cannot quietly drop it.

// pollOnce wires a poll source against the given handler and runs exactly one
// poll, returning whatever reached the standard logger.
func pollOnce(t *testing.T, s *store, handler http.HandlerFunc) string {
	t.Helper()

	server := httptest.NewServer(handler)
	defer server.Close()

	var logBuf bytes.Buffer
	originalOutput := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(originalOutput)
		log.SetFlags(originalFlags)
	}()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	ps := newPollSource(newHTTPClient("sdk-key", cfg), s, time.Hour, nil)
	ps.poll()

	return logBuf.String()
}

// storeWithKnownFlag returns a store already holding good config, as it would
// be after a successful earlier poll.
func storeWithKnownFlag() *store {
	s := newStore()
	s.setAll([]flagDTO{{
		Key:     "known-flag",
		Version: 1,
		Type:    "Boolean",
		Enabled: true,
		Fallthrough: serveConfig{
			Type:      "Fixed",
			Variation: "on",
		},
		OffVariation: "off",
	}}, nil)
	return s
}

func TestPolling_LogsAMalformedPayload(t *testing.T) {
	s := storeWithKnownFlag()

	// The #2279 payload shape: enums as integers where the DTO declares strings.
	// `key` and `enabled` decode perfectly — that is the trap.
	malformed := `{"environment":"test","version":2,"flags":[` +
		`{"key":"known-flag","version":2,"type":0,"enabled":true,` +
		`"fallthrough":{"type":0,"variation":"on"},"offVariation":"off"}],"segments":[]}`

	logged := pollOnce(t, s, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(malformed))
	})

	if !strings.Contains(logged, "[featureflip]") {
		t.Errorf("a malformed payload was discarded silently; log = %q", logged)
	}

	// #2288's guard: the discard is wholesale, never a partial application.
	got, ok := s.getFlag("known-flag")
	if !ok {
		t.Fatal("flag disappeared from the store: the malformed payload was applied")
	}
	if got.Type != "Boolean" {
		t.Errorf("partial decode leaked into the store: Type = %q, want %q", got.Type, "Boolean")
	}
}

func TestPolling_LogsSyntacticallyInvalidJSON(t *testing.T) {
	s := storeWithKnownFlag()

	logged := pollOnce(t, s, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"flags": [ this is not json`))
	})

	if !strings.Contains(logged, "[featureflip]") {
		t.Errorf("invalid JSON was discarded silently; log = %q", logged)
	}
	if _, ok := s.getFlag("known-flag"); !ok {
		t.Error("store was cleared by an invalid payload")
	}
}

func TestPolling_StaysSilentOnATransportFailure(t *testing.T) {
	s := storeWithKnownFlag()

	// A 500 is the self-healing case: the next tick retries, so logging it
	// would flood the logs of a client whose backend is briefly unhealthy.
	logged := pollOnce(t, s, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	if logged != "" {
		t.Errorf("a transport failure should stay silent, logged %q", logged)
	}
	if _, ok := s.getFlag("known-flag"); !ok {
		t.Error("store was cleared by a transport failure")
	}
}

func TestPolling_StaysSilentWhenTheServerIsUnreachable(t *testing.T) {
	s := storeWithKnownFlag()

	var logBuf bytes.Buffer
	originalOutput := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(originalOutput)
		log.SetFlags(originalFlags)
	}()

	cfg := defaultConfig()
	// Nothing is listening here; this is the offline-client case.
	cfg.baseURL = "http://127.0.0.1:1"
	ps := newPollSource(newHTTPClient("sdk-key", cfg), s, time.Hour, nil)
	ps.poll()

	if logged := logBuf.String(); logged != "" {
		t.Errorf("an unreachable server should stay silent, logged %q", logged)
	}
}

func TestPolling_StaysSilentOnASuccessfulPoll(t *testing.T) {
	s := newStore()

	logged := pollOnce(t, s, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"environment":"test","version":1,"flags":[],"segments":[]}`))
	})

	if logged != "" {
		t.Errorf("a successful poll should log nothing, logged %q", logged)
	}
}

// The same defect as poll() lived on two more paths that fetch config through
// the HTTP client. `segment.updated` calls the identical getFlags(), and
// `flag.created`/`flag.updated` call getFlag() — both discarded a decode
// failure without a word, so the SDK could still drop config silently even
// after the polling path learned to speak up (#2396).

// streamOnce dispatches one SSE event against a stream source wired to the
// given handler, returning whatever reached the standard logger.
func streamOnce(t *testing.T, s *store, eventType, data string, handler http.HandlerFunc) string {
	t.Helper()

	server := httptest.NewServer(handler)
	defer server.Close()

	var logBuf bytes.Buffer
	originalOutput := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(originalOutput)
		log.SetFlags(originalFlags)
	}()

	cfg := defaultConfig()
	cfg.baseURL = server.URL
	ss := newStreamSource(newHTTPClient("sdk-key", cfg), s, nil)
	ss.handleEvent(eventType, data)

	return logBuf.String()
}

func TestStreaming_SegmentUpdatedLogsAMalformedRefetch(t *testing.T) {
	s := storeWithKnownFlag()

	malformed := `{"environment":"test","version":2,"flags":[` +
		`{"key":"known-flag","version":2,"type":0,"enabled":true}],"segments":[]}`

	logged := streamOnce(t, s, "segment.updated", "{}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(malformed))
	})

	if !strings.Contains(logged, "[featureflip]") {
		t.Errorf("segment.updated discarded a malformed refetch silently; log = %q", logged)
	}
	got, ok := s.getFlag("known-flag")
	if !ok {
		t.Fatal("flag disappeared: the malformed refetch was applied")
	}
	if got.Type != "Boolean" {
		t.Errorf("partial decode leaked into the store: Type = %q", got.Type)
	}
}

func TestStreaming_SegmentUpdatedStaysSilentOnATransportFailure(t *testing.T) {
	s := storeWithKnownFlag()

	logged := streamOnce(t, s, "segment.updated", "{}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	if logged != "" {
		t.Errorf("a transport failure should stay silent, logged %q", logged)
	}
}

func TestStreaming_FlagUpdatedLogsAMalformedSingleFlagRefetch(t *testing.T) {
	s := storeWithKnownFlag()

	logged := streamOnce(t, s, "flag.updated", `{"key":"known-flag"}`, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"key":"known-flag","version":2,"type":0,"enabled":true}`))
	})

	if !strings.Contains(logged, "[featureflip]") {
		t.Errorf("flag.updated discarded a malformed refetch silently; log = %q", logged)
	}
	got, ok := s.getFlag("known-flag")
	if !ok {
		t.Fatal("flag disappeared: the malformed refetch was applied")
	}
	if got.Type != "Boolean" {
		t.Errorf("partial decode leaked into the store: Type = %q", got.Type)
	}
}

func TestStreaming_LogsAMalformedEventEnvelope(t *testing.T) {
	s := storeWithKnownFlag()

	// The envelope is tiny (`{"key":"..."}`), but a violation here is still a
	// contract violation, and `sync` already announces its own. Silence on one
	// frame type and noise on another is the asymmetry this issue is about.
	logged := streamOnce(t, s, "flag.updated", `{"key":`, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the envelope never parsed; no refetch should have been attempted")
	})

	if !strings.Contains(logged, "[featureflip]") {
		t.Errorf("a malformed event envelope was discarded silently; log = %q", logged)
	}
}

func TestStreaming_FlagDeletedLogsAMalformedEventEnvelope(t *testing.T) {
	s := storeWithKnownFlag()

	logged := streamOnce(t, s, "flag.deleted", `{"key":`, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the envelope never parsed; no request should have been made")
	})

	if !strings.Contains(logged, "[featureflip]") {
		t.Errorf("a malformed delete envelope was discarded silently; log = %q", logged)
	}
	if _, ok := s.getFlag("known-flag"); !ok {
		t.Error("a malformed delete envelope removed a flag")
	}
}
