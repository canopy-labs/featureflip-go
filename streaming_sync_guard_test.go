package featureflip

import (
	"encoding/json"
	"testing"
)

// Go's encoding/json PARTIALLY POPULATES a struct when it hits a type error: it
// keeps every field it managed to decode and leaves the mismatched ones at their
// zero value, then returns the error. So the `if err != nil { return }` guard in
// handleEvent's "sync" branch is load-bearing, not defensive boilerplate — drop it
// and a malformed frame replaces the whole store with flags whose Type is "",
// matching neither "Fixed" nor "Rollout" and silently breaking every evaluation.
//
// That is not hypothetical. The evaluation API used to serialize enums as integers
// over SSE while serializing them as strings over REST (#2279), which is exactly
// this shape: `"type": 0` into a string field. The Go SDK survived it by dropping
// the frame; the Ruby SDK, which has no such guard, silently mis-targeted every
// segment (#2285).
//
// The server side is fixed, so these frames should no longer arrive. These tests
// exist because the guard must survive anyone "simplifying" it later — the failure
// it prevents is silent, and nothing else in the suite covers it (#2288).
func TestStreaming_SyncRejectsMalformedFrameWholesale(t *testing.T) {
	// A store already holding good config, as it would be after a REST fetch.
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

	ss := newStreamSource(nil, s, nil)

	// The #2279 payload shape: enums as integers where the DTO declares strings.
	// Note `enabled` and `key` are perfectly decodable — that is the trap. A
	// handler that ignored the error would keep those and zero out only `type`.
	malformed := `{"environment":"test","version":2,"flags":[` +
		`{"key":"known-flag","version":2,"type":0,"enabled":true,` +
		`"fallthrough":{"type":0,"variation":"on"},"offVariation":"off"}],"segments":[]}`

	ss.handleEvent("sync", malformed)

	got, ok := s.getFlag("known-flag")
	if !ok {
		t.Fatal("flag disappeared from the store: the malformed frame was applied")
	}
	if got.Type != "Boolean" {
		t.Errorf("store was replaced from a malformed frame: Type = %q, want %q "+
			"(partial decode leaked into the store)", got.Type, "Boolean")
	}
	if got.Fallthrough.Type != "Fixed" {
		t.Errorf("store was replaced from a malformed frame: Fallthrough.Type = %q, want %q",
			got.Fallthrough.Type, "Fixed")
	}
	if got.Version != 1 {
		t.Errorf("store was replaced from a malformed frame: Version = %d, want 1", got.Version)
	}
}

// Demonstrates the partial-population behaviour the guard defends against, so the
// reason the guard exists is pinned by a test rather than only by a comment. If a
// future Go release stopped partially populating, this test would fail and the
// comment above could be revisited.
func TestStreaming_SyncMalformedFrameWouldPartiallyDecode(t *testing.T) {
	var resp getFlagsResponse
	malformed := `{"flags":[{"key":"k","version":2,"type":0,"enabled":true}],"segments":[]}`

	err := json.Unmarshal([]byte(malformed), &resp)
	if err == nil {
		t.Fatal("expected a type error unmarshalling an integer into a string field")
	}
	if len(resp.Flags) != 1 {
		t.Fatalf("expected the partial decode to still yield 1 flag, got %d", len(resp.Flags))
	}
	if resp.Flags[0].Key != "k" || !resp.Flags[0].Enabled {
		t.Error("expected decodable fields to survive the partial decode")
	}
	if resp.Flags[0].Type != "" {
		t.Errorf("expected the mismatched field to be left zero, got %q", resp.Flags[0].Type)
	}
}

// A well-formed frame must still replace the store — otherwise the guard could be
// "fixed" by rejecting everything.
func TestStreaming_SyncAppliesWellFormedFrame(t *testing.T) {
	s := newStore()
	s.setAll([]flagDTO{{Key: "old-flag", Version: 1, Type: "Boolean"}}, nil)

	ss := newStreamSource(nil, s, nil)

	wellFormed := `{"environment":"test","version":2,"flags":[` +
		`{"key":"new-flag","version":2,"type":"String","enabled":true,` +
		`"fallthrough":{"type":"Fixed","variation":"a"},"offVariation":"a"}],"segments":[]}`

	ss.handleEvent("sync", wellFormed)

	if _, ok := s.getFlag("old-flag"); ok {
		t.Error("sync must REPLACE the store, not merge: old-flag survived")
	}
	got, ok := s.getFlag("new-flag")
	if !ok {
		t.Fatal("well-formed sync frame was not applied")
	}
	if got.Type != "String" {
		t.Errorf("Type = %q, want %q", got.Type, "String")
	}
}
