package featureflip

import (
	"encoding/json"
	"os"
	"testing"
)

// Runner for the shared `malformedConfigVectors` class (#2315).
//
// The rule: a config payload that violates the wire contract is discarded WHOLESALE,
// never partially applied. Go is the SDK where "wholesale" is most load-bearing —
// encoding/json PARTIALLY POPULATES on a type error, keeping every field it decoded
// and zeroing the rest, so ignoring the error replaces the store with flags whose
// Type is "" (#2288).
//
// TestStreaming_SyncRejectsMalformedFrameWholesale covers that guard with a
// hand-written frame. This runner covers the same ground from the SHARED fixture, so
// a divergence between SDKs fails a build rather than shipping — which is the whole
// point of the class (one server bug, #2279, produced five different behaviours).
type malformedVector struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	Expect      string `json:"expect"`
	// dropEntity only — the entities the payload must LOSE, and the ones it must keep.
	DropFlags    []string        `json:"dropFlags"`
	DropSegments []string        `json:"dropSegments"`
	KeepFlags    []string        `json:"keepFlags"`
	KeepSegments []string        `json:"keepSegments"`
	Payload      json.RawMessage `json:"payload"`
}

type malformedBlock struct {
	Seed    json.RawMessage   `json:"seed"`
	Vectors []malformedVector `json:"vectors"`
}

func loadMalformed(t *testing.T) malformedBlock {
	t.Helper()
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatalf("read vectors.json: %v", err)
	}
	var file struct {
		MalformedConfigVectors malformedBlock `json:"malformedConfigVectors"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse vectors.json: %v", err)
	}
	return file.MalformedConfigVectors
}

// seededStore applies the shared seed through the real sync path and fails loudly if
// it did not take. A runner whose seed silently failed would "pass" every reject
// vector for entirely the wrong reason.
func seededStore(t *testing.T, seed json.RawMessage) (*store, *streamSource) {
	t.Helper()
	s := newStore()
	ss := newStreamSource(nil, s, nil)
	ss.handleEvent("sync", string(seed))
	if _, ok := s.getFlag("mc-seed"); !ok {
		t.Fatal("seed snapshot did not apply — the runner would prove nothing")
	}
	return s, ss
}

func TestGoldenMalformedConfigVectors(t *testing.T) {
	block := loadMalformed(t)
	if len(block.Vectors) == 0 {
		t.Fatal("no malformedConfigVectors in fixture")
	}

	executed := 0
	for _, v := range block.Vectors {
		executed++
		t.Run(v.ID, func(t *testing.T) {
			s, ss := seededStore(t, block.Seed)

			ss.handleEvent("sync", string(v.Payload))

			switch v.Expect {
			case "reject":
				if _, ok := s.getFlag("mc-seed"); !ok {
					t.Errorf("%s: the malformed payload was applied — the seeded config is gone", v.Description)
				}
				if _, ok := s.getFlag("mc-bad-type"); ok {
					t.Error("a flag from the rejected payload reached the store (partial apply)")
				}
			case "accept":
				_, flagOK := s.getFlag("mc-accepted-flag")
				_, segOK := s.getSegment("mc-accepted")
				if !flagOK && !segOK {
					t.Errorf("%s: a forward-compatible payload was rejected", v.Description)
				}
			case "dropEntity":
				// Neither accept nor reject: the payload APPLIES, minus the entities
				// carrying an enum this build cannot evaluate. Both halves are asserted
				// — "dropped" alone is satisfied by rejecting the whole payload, and
				// "kept" alone by tolerating the bad value.
				for _, key := range v.DropFlags {
					if _, ok := s.getFlag(key); ok {
						t.Errorf("%s: flag %q should have been dropped", v.Description, key)
					}
				}
				for _, key := range v.DropSegments {
					if _, ok := s.getSegment(key); ok {
						t.Errorf("%s: segment %q should have been dropped", v.Description, key)
					}
				}
				for _, key := range v.KeepFlags {
					if _, ok := s.getFlag(key); !ok {
						t.Errorf("%s: flag %q should have been kept", v.Description, key)
					}
				}
				for _, key := range v.KeepSegments {
					if _, ok := s.getSegment(key); !ok {
						t.Errorf("%s: segment %q should have been kept", v.Description, key)
					}
				}
			default:
				t.Fatalf("unmapped expect %q", v.Expect)
			}
		})
	}

	if executed < 8 {
		t.Errorf("only %d malformed vectors executed, want >= 8 — did a skip rule appear?", executed)
	}
}
