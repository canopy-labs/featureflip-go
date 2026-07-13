package featureflip

import (
	"encoding/json"
	"os"
	"testing"
)

// goldenFile mirrors the top-level shape of testdata/vectors.json.
type goldenFile struct {
	BucketVectors    []goldenBucketVec    `json:"bucketVectors"`
	RolloutVectors   []goldenRolloutVec   `json:"rolloutVectors"`
	ConditionVectors []goldenConditionVec `json:"conditionVectors"`
	FlagVectors      []json.RawMessage    `json:"flagVectors"`
}

type goldenBucketVec struct {
	ID             string `json:"id"`
	Salt           string `json:"salt"`
	Value          string `json:"value"`
	ExpectedBucket int    `json:"expectedBucket"`
}

type goldenRolloutVec struct {
	ID                string              `json:"id"`
	Salt              string              `json:"salt"`
	Value             string              `json:"value"`
	Variations        []weightedVariation `json:"variations"`
	ExpectedVariation string              `json:"expectedVariation"`
}

type goldenConditionVec struct {
	ID        string `json:"id"`
	Attribute struct {
		Type  string `json:"type"`
		Value any    `json:"value"`
	} `json:"attribute"`
	Operator      string   `json:"operator"`
	Values        []string `json:"values"`
	Negate        bool     `json:"negate"`
	ExpectedMatch bool     `json:"expectedMatch"`
}

// goldenFlagVec is the per-entry shape inside flagVectors.
type goldenFlagVec struct {
	ID      string `json:"id"`
	FlagKey string `json:"flagKey"`
	// flags and segments are arrays on the wire; we index them into maps.
	Flags    []flagDTO    `json:"flags"`
	Segments []segmentDTO `json:"segments"`
	Context  struct {
		UserID     string         `json:"userId"`
		Attributes map[string]any `json:"attributes"`
	} `json:"context"`
	Expected struct {
		Variation string `json:"variation"`
		Value     any    `json:"value"`
		Reason    struct {
			Kind            string `json:"kind"`
			RuleID          string `json:"ruleId"`
			PrerequisiteKey string `json:"prerequisiteKey"`
		} `json:"reason"`
	} `json:"expected"`
}

func loadGolden(t *testing.T) goldenFile {
	t.Helper()
	data, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var g goldenFile
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return g
}

// TestGoldenBuckets verifies that bucket(salt, value) matches the engine-generated
// expected bucket for each bucket vector.
func TestGoldenBuckets(t *testing.T) {
	for _, v := range loadGolden(t).BucketVectors {
		got := bucket(v.Salt, v.Value)
		if got != v.ExpectedBucket {
			t.Errorf("[%s] bucket(%q, %q) = %d, want %d", v.ID, v.Salt, v.Value, got, v.ExpectedBucket)
		}
	}
}

// TestGoldenRollouts verifies percentage-rollout variation assignment by
// building a minimal flag DTO and evaluating via evaluate().
func TestGoldenRollouts(t *testing.T) {
	for _, v := range loadGolden(t).RolloutVectors {
		// Build a variation list with string values equal to each key.
		vars := make([]variationDTO, len(v.Variations))
		for i, w := range v.Variations {
			vars[i] = variationDTO{Key: w.Key, Value: mustJSON(w.Key)}
		}
		flag := flagDTO{
			Key:        "rollout",
			Version:    1,
			Type:       "String",
			Enabled:    true,
			Variations: vars,
			Fallthrough: serveConfig{
				Type:       "Rollout",
				Salt:       v.Salt,
				BucketBy:   "userId",
				Variations: v.Variations,
			},
			OffVariation: v.Variations[0].Key,
		}
		ctx := EvaluationContext{UserID: v.Value}
		r := evaluate(flag, ctx, nil, nil)
		if r.Variation != v.ExpectedVariation {
			t.Errorf("[%s] variation = %q, want %q", v.ID, r.Variation, v.ExpectedVariation)
		}
	}
}

// TestGoldenConditions verifies individual condition evaluation (operator + negate)
// by building a single-condition flag and checking whether "match" variation is served.
func TestGoldenConditions(t *testing.T) {
	for _, v := range loadGolden(t).ConditionVectors {
		flag := flagDTO{
			Key:     "cond",
			Version: 1,
			Type:    "String",
			Enabled: true,
			Variations: []variationDTO{
				{Key: "match", Value: mustJSON("match")},
				{Key: "nomatch", Value: mustJSON("nomatch")},
			},
			Rules: []ruleDTO{
				{
					ID:       "r",
					Priority: 0,
					Serve:    serveConfig{Type: "Fixed", Variation: "match"},
					ConditionGroups: []conditionGroup{
						{
							Operator: "And",
							Conditions: []condition{
								{
									Attribute: "attr",
									Operator:  v.Operator,
									Values:    v.Values,
									Negate:    v.Negate,
								},
							},
						},
					},
				},
			},
			Fallthrough:  serveConfig{Type: "Fixed", Variation: "nomatch"},
			OffVariation: "nomatch",
		}
		ctx := EvaluationContext{
			Attributes: map[string]any{"attr": v.Attribute.Value},
		}
		got := evaluate(flag, ctx, nil, nil).Variation == "match"
		if got != v.ExpectedMatch {
			t.Errorf("[%s] match = %v, want %v (operator=%s, value=%v, values=%v, negate=%v)",
				v.ID, got, v.ExpectedMatch, v.Operator, v.Attribute.Value, v.Values, v.Negate)
		}
	}
}

// TestGoldenFlags verifies end-to-end flag evaluation against the engine-generated
// flag vectors, checking variation, value, and normalized reason.
func TestGoldenFlags(t *testing.T) {
	g := loadGolden(t)
	for _, raw := range g.FlagVectors {
		var v goldenFlagVec
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("parse flagVector: %v", err)
		}

		// Index flags and segments into maps keyed by their key fields.
		flagMap := make(map[string]flagDTO, len(v.Flags))
		for _, f := range v.Flags {
			flagMap[f.Key] = f
		}
		segMap := make(map[string]segmentDTO, len(v.Segments))
		for _, s := range v.Segments {
			segMap[s.Key] = s
		}

		targetFlag, ok := flagMap[v.FlagKey]
		if !ok {
			t.Errorf("[%s] flagKey %q not found in flags", v.ID, v.FlagKey)
			continue
		}

		ctx := EvaluationContext{
			UserID:     v.Context.UserID,
			Attributes: v.Context.Attributes,
		}

		r := evaluate(targetFlag, ctx, segMap, flagMap)

		// Assert variation.
		if r.Variation != v.Expected.Variation {
			t.Errorf("[%s] variation = %q, want %q", v.ID, r.Variation, v.Expected.Variation)
		}

		// Assert value via round-trip JSON comparison so float/bool/string
		// comparisons are canonical.
		gotValJSON, err := json.Marshal(r.Value)
		if err != nil {
			t.Errorf("[%s] marshal got value: %v", v.ID, err)
			continue
		}
		wantValJSON, err := json.Marshal(v.Expected.Value)
		if err != nil {
			t.Errorf("[%s] marshal want value: %v", v.ID, err)
			continue
		}
		if string(gotValJSON) != string(wantValJSON) {
			t.Errorf("[%s] value = %s, want %s", v.ID, gotValJSON, wantValJSON)
		}

		// Assert normalized reason: kind + optional ruleId/prerequisiteKey.
		gotKind := string(r.Reason)
		if gotKind != v.Expected.Reason.Kind {
			t.Errorf("[%s] reason.kind = %q, want %q", v.ID, gotKind, v.Expected.Reason.Kind)
		}
		if v.Expected.Reason.RuleID != "" && r.RuleID != v.Expected.Reason.RuleID {
			t.Errorf("[%s] reason.ruleId = %q, want %q", v.ID, r.RuleID, v.Expected.Reason.RuleID)
		}
		if v.Expected.Reason.PrerequisiteKey != "" && r.PrerequisiteKey != v.Expected.Reason.PrerequisiteKey {
			t.Errorf("[%s] reason.prerequisiteKey = %q, want %q", v.ID, r.PrerequisiteKey, v.Expected.Reason.PrerequisiteKey)
		}
	}
}
