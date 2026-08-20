package featureflip

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// The coreContractVectors class asserts the shared CORE's contract — typed-accessor
// strictness, malformed-variation handling — one layer above the evaluator the
// other golden classes cover.
//
// It exists because that layer had no executable cross-SDK spec at all: #1989 and
// #2281 each shipped as a 6-of-7-SDK divergence that no CI could see, and both were
// found by a manual sweep. Unlike the other classes these vectors are hand-authored
// rather than engine-generated, because the engine has no opinion here and in fact
// disagrees — it returns null where an SDK must return the caller's default.
//
// `expect.reason` is a CANONICAL token mapped to Go's vocabulary below, since reason
// spelling legitimately differs per SDK.

type contractVec struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	Kind        string    `json:"kind"`
	Flags       []flagDTO `json:"flags"`
	FlagKey     string    `json:"flagKey"`
	Context     struct {
		UserID     string         `json:"userId"`
		Attributes map[string]any `json:"attributes"`
	} `json:"context"`
	Read struct {
		As      string `json:"as"`
		Default any    `json:"default"`
	} `json:"read"`
	Expect struct {
		Value  any    `json:"value"`
		Reason string `json:"reason"`
	} `json:"expect"`
}

type contractFile struct {
	CoreContractVectors []contractVec `json:"coreContractVectors"`
}

// canonicalReason maps a vector's canonical token to Go's EvaluationReason.
func canonicalReason(t *testing.T, token string) EvaluationReason {
	t.Helper()
	switch token {
	case "Error":
		return ReasonError
	case "Fallthrough":
		return ReasonFallthrough
	case "FlagNotFound":
		return ReasonFlagNotFound
	default:
		t.Fatalf("unmapped canonical reason %q — add it to canonicalReason", token)
		return ""
	}
}

func TestGoldenCoreContract(t *testing.T) {
	data, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f contractFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(f.CoreContractVectors) == 0 {
		t.Fatal("no coreContractVectors in fixture")
	}

	executed := 0
	for _, v := range f.CoreContractVectors {
		// Go exposes no int accessor — Float64Variation covers every JSON number.
		// Skipping is explicit so a capability gap can't masquerade as a pass.
		if v.Read.As == "int" {
			continue
		}

		t.Run(v.ID, func(t *testing.T) {
			var events []EvaluationEvent
			cfg := defaultConfig()
			WithInspectors(func(e EvaluationEvent) { events = append(events, e) })(&cfg)

			core := newSharedCore("contract-"+v.ID, cfg)
			core.store.setAll(v.Flags, nil)
			core.initialized = true
			core.ep = newEventProcessor(nil, 100, time.Hour)
			client := &Client{core: core}
			defer client.Close()

			ctx := EvaluationContext{UserID: v.Context.UserID, Attributes: v.Context.Attributes}

			var got any
			switch v.Read.As {
			case "bool":
				got = client.BoolVariation(v.FlagKey, ctx, v.Read.Default.(bool))
			case "string":
				got = client.StringVariation(v.FlagKey, ctx, v.Read.Default.(string))
			case "number", "double":
				got = client.Float64Variation(v.FlagKey, ctx, v.Read.Default.(float64))
			default:
				t.Fatalf("unmapped read.as %q — add it to the switch", v.Read.As)
			}

			// JSON numbers decode as float64, so compare numerics as float64.
			want := v.Expect.Value
			if wf, ok := want.(float64); ok {
				gf, ok := got.(float64)
				if !ok || gf != wf {
					t.Errorf("%s: value = %#v, want %v", v.Description, got, wf)
				}
			} else if got != want {
				t.Errorf("%s: value = %#v, want %#v", v.Description, got, want)
			}

			// The typed accessors return only a value, so the reason is observed
			// through the inspector — the same surface a real caller would use.
			if len(events) != 1 {
				t.Fatalf("inspector fired %d times, want exactly 1", len(events))
			}
			if want := canonicalReason(t, v.Expect.Reason); events[0].Reason != want {
				t.Errorf("%s: reason = %q, want %q", v.Description, events[0].Reason, want)
			}
		})
		executed++
	}

	// A runner that silently skips everything is worse than no runner at all.
	if executed < 12 {
		t.Errorf("only %d contract vectors executed, want >= 12 — did a skip rule widen?", executed)
	}
}
