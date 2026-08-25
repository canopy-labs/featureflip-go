package featureflip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// singleFlagServer stands in for the eval-api's GET /v1/sdk/flags/{key}, returning the
// given raw JSON. Raw rather than a marshalled flagDTO because the whole point is a
// serve type the DTO's own Go type could hold but this build cannot dispatch on.
func singleFlagServer(t *testing.T, body string) *httpClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	cfg := defaultConfig()
	cfg.baseURL = srv.URL
	return newHTTPClient("test-key", cfg)
}

// Entity-drop behaviour that the shared malformedConfigVectors runner cannot reach.
//
// That runner drives the "sync" branch, so it exercises the SNAPSHOT parse boundary
// only. The single-flag DELTA path (`flag.created` / `flag.updated` -> getFlag ->
// setFlag) is a separate boundary with its own guard, and it is the realistic trigger:
// it is what the server sends the moment someone edits a flag to use a serve type this
// build has not heard of.
//
// A delta drop also means something different from a snapshot drop, which is the real
// assertion below: the store's PREVIOUS copy must survive. Upserting the new one would
// install a flag this build mis-evaluates, and removing the key would strand callers on
// their default when a perfectly good older version is already in hand.

func TestDropUnevaluable_KeepsEvaluableEntities(t *testing.T) {
	// Positive control. Without it every assertion in this file is satisfied just as
	// well by a build that drops EVERYTHING, which would be a worse bug than the one
	// being fixed.
	flags := []flagDTO{{
		Key:         "ok",
		Fallthrough: serveConfig{Type: "Rollout"},
		Rules: []ruleDTO{{
			ID:              "r1",
			Serve:           serveConfig{Type: "Fixed"},
			ConditionGroups: []conditionGroup{{Operator: "Or"}},
		}},
	}}
	segments := []segmentDTO{{Key: "seg", ConditionLogic: "Or"}}

	keptFlags, keptSegments := dropUnevaluable(flags, segments)

	if len(keptFlags) != 1 || len(keptSegments) != 1 {
		t.Fatalf("evaluable entities were dropped: %d flags, %d segments kept",
			len(keptFlags), len(keptSegments))
	}
}

func TestDropUnevaluable_IgnoresAbsentEnums(t *testing.T) {
	// An EMPTY value is the field being absent, which is the missing-required-field
	// axis, not this one — the SDKs deliberately disagree there (ruby/python/php default
	// an absent conditionLogic to "And", js rejects the payload), so dropping on "" would
	// add a fourth behaviour rather than converge one. It would also silently start
	// discarding entities from payloads that work today, which is why the check is scoped
	// to a value that is PRESENT and unrecognised.
	flags := []flagDTO{{Key: "no-serve-type"}}
	segments := []segmentDTO{{Key: "no-logic"}}

	keptFlags, keptSegments := dropUnevaluable(flags, segments)

	if len(keptFlags) != 1 {
		t.Error("a flag with an ABSENT serve type was dropped; absent is a different axis")
	}
	if len(keptSegments) != 1 {
		t.Error("a segment with an ABSENT conditionLogic was dropped; absent is a different axis")
	}
}

func TestStreaming_FlagDeltaWithUnknownServeTypeKeepsPreviousFlag(t *testing.T) {
	s := storeWithKnownFlag()
	hc := singleFlagServer(t, `{"key":"known-flag","version":2,"type":"Boolean",`+
		`"enabled":true,"fallthrough":{"type":"Canary","variation":"on"},"offVariation":"off"}`)
	ss := newStreamSource(hc, s, nil)

	ss.handleEvent("flag.updated", `{"key":"known-flag"}`)

	got, ok := s.getFlag("known-flag")
	if !ok {
		t.Fatal("the flag was REMOVED from the store; a dropped delta must leave the previous copy serving")
	}
	if got.Version != 1 || got.Fallthrough.Type != "Fixed" {
		t.Errorf("the unevaluable delta was applied: Version = %d, Fallthrough.Type = %q; want 1, %q",
			got.Version, got.Fallthrough.Type, "Fixed")
	}
}

func TestStreaming_FlagDeltaWithKnownServeTypeIsApplied(t *testing.T) {
	// The matching positive control for the delta path specifically: proves the guard
	// blocks the unevaluable delta above rather than every delta.
	s := storeWithKnownFlag()
	hc := singleFlagServer(t, `{"key":"known-flag","version":2,"type":"Boolean",`+
		`"enabled":true,"fallthrough":{"type":"Rollout","variation":"on"},"offVariation":"off"}`)
	ss := newStreamSource(hc, s, nil)

	ss.handleEvent("flag.updated", `{"key":"known-flag"}`)

	got, _ := s.getFlag("known-flag")
	if got.Version != 2 || got.Fallthrough.Type != "Rollout" {
		t.Errorf("an evaluable delta was not applied: Version = %d, Fallthrough.Type = %q; want 2, %q",
			got.Version, got.Fallthrough.Type, "Rollout")
	}
}
