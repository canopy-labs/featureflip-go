package featureflip

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"sort"
	"sync"
	"testing"
	"time"
)

// The eventPayloadVectors class locks what Identify and Track actually put on the
// wire: {type, flagKey, userId?, variation?, timestamp, metadata?}.
//
// That shape had no executable spec at all, which is the direct cause of #2359 — a
// three-way payload divergence across six server SDKs (js/node/python forwarded the
// caller's attributes as `metadata`; php/go/ruby, this one included, discarded them)
// sat unnoticed indefinitely. Nothing compared an emitted event against an expected
// shape, and the receiving end reduces every event to a counter tuple, so no
// downstream assertion caught it either.
//
// Hand-authored rather than engine-generated, because the engine emits no events: it
// returns an EvaluationResult, and the payload is built a layer above that. See
// tools/golden-vectors/README.md for the full runner contract.

type eventVec struct {
	ID          string         `json:"id"`
	Description string         `json:"description"`
	Kind        string         `json:"kind"`
	Requires    []string       `json:"requires"`
	Context     map[string]any `json:"context"`
	EventKey    string         `json:"eventKey"`
	Metadata    map[string]any `json:"metadata"`
	Expect      map[string]any `json:"expect"`

	// Distinguishes an ABSENT metadata key from an explicitly empty bag: both
	// must produce the same bytes, and only the raw map can tell them apart.
	raw map[string]json.RawMessage
}

type eventFile struct {
	EventPayloadVectors []json.RawMessage `json:"eventPayloadVectors"`
}

// An ISO-8601 instant that designates UTC. Deliberately not an equality check: the
// precision and the zero-offset spelling differ legitimately per SDK (this one emits
// whole seconds and a "Z"), so a literal expectation would lock in a divergence
// rather than a contract.
var utcInstant = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|\+00:00)$`)

// The context capabilities this SDK has. A vector requiring anything outside this
// set is skipped explicitly, so a structural gap can't masquerade as a pass.
//
// Go has anonymousContext (UserID is a plain string, so "" is a legitimate absent
// identity alongside populated Attributes) but NOT mapContext: the identity lives
// in its own struct field, so which spelling a caller used is not a question this
// SDK can be asked.
var capabilities = map[string]bool{"anonymousContext": true}

// Captures the events endpoint's RAW request body. Decoding into []sdkEvent the way
// the other tests do would defeat the purpose: `omitempty` means absence is only
// observable in the bytes, and an absent optional is exactly what #2359 was about.
func eventCapturingServer(t *testing.T, bodies *[]byte, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sdk/flags":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(getFlagsResponse{Environment: "test", Version: 1})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/sdk/events":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			*bodies = append(*bodies, body...)
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// Splits the vector's flat context into this SDK's typed EvaluationContext: the
// identity into its own field, everything else into the attribute bag. That split is
// what a real caller writes, so the assertion still lands on the SDK rather than on
// the mapping — but it is also why identity-SPELLING vectors are skipped below.
func contextFromVector(raw map[string]any) EvaluationContext {
	ctx := EvaluationContext{Attributes: map[string]any{}}
	for k, v := range raw {
		switch k {
		case "user_id", "userId":
			if s, ok := v.(string); ok {
				ctx.UserID = s
			}
		default:
			ctx.Attributes[k] = v
		}
	}
	if len(ctx.Attributes) == 0 {
		ctx.Attributes = nil
	}
	return ctx
}

func TestGoldenEventPayload(t *testing.T) {
	data, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f eventFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(f.EventPayloadVectors) == 0 {
		t.Fatal("no eventPayloadVectors in fixture")
	}

	executed := 0
	for _, rawVec := range f.EventPayloadVectors {
		var v eventVec
		if err := json.Unmarshal(rawVec, &v); err != nil {
			t.Fatalf("parse vector: %v", err)
		}
		if err := json.Unmarshal(rawVec, &v.raw); err != nil {
			t.Fatalf("parse vector keys: %v", err)
		}

		if !supported(v.Requires) {
			continue
		}
		executed++

		t.Run(v.ID, func(t *testing.T) {
			var mu sync.Mutex
			var body []byte
			server := eventCapturingServer(t, &body, &mu)
			defer server.Close()

			client, err := Get("events-"+v.ID,
				WithBaseURL(server.URL),
				WithStreaming(false),
				WithInitTimeout(5*time.Second),
			)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}

			ctx := contextFromVector(v.Context)
			if v.Kind == "identify" {
				client.Identify(ctx)
			} else {
				// A vector with no `metadata` key omits the argument entirely,
				// which must put the same bytes on the wire as an empty bag.
				var meta map[string]any
				if _, ok := v.raw["metadata"]; ok {
					meta = v.Metadata
				}
				client.Track(v.EventKey, ctx, meta)
			}
			client.Flush()
			client.Close()

			mu.Lock()
			captured := append([]byte(nil), body...)
			mu.Unlock()

			var sent struct {
				Events []map[string]any `json:"events"`
			}
			if err := json.Unmarshal(captured, &sent); err != nil {
				t.Fatalf("decode captured body %q: %v", captured, err)
			}
			if len(sent.Events) != 1 {
				t.Fatalf("got %d events, want 1", len(sent.Events))
			}
			event := sent.Events[0]

			// The EXACT field set, not a subset: #2359 was a field being present
			// in three SDKs and absent in three, which a subset assertion cannot
			// see.
			gotKeys := make([]string, 0, len(event))
			for k := range event {
				gotKeys = append(gotKeys, k)
			}
			wantKeys := append([]string{"timestamp"}, expectFieldNames(v.Expect)...)
			sort.Strings(gotKeys)
			sort.Strings(wantKeys)
			if !reflect.DeepEqual(gotKeys, wantKeys) {
				t.Fatalf("field set = %v, want %v", gotKeys, wantKeys)
			}

			for field, want := range v.Expect {
				if !reflect.DeepEqual(event[field], want) {
					t.Errorf("%s = %#v, want %#v", field, event[field], want)
				}
			}

			ts, _ := event["timestamp"].(string)
			if !utcInstant.MatchString(ts) {
				t.Errorf("timestamp %q is not an ISO-8601 UTC instant", ts)
			}
		})
	}

	// A runner that silently skips everything is worse than no runner at all.
	if executed < 10 {
		t.Errorf("only %d event payload vectors executed, want >= 10 — did a skip rule widen?", executed)
	}
}

func supported(requires []string) bool {
	for _, c := range requires {
		if !capabilities[c] {
			return false
		}
	}
	return true
}

func expectFieldNames(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
