package featureflip

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// prereqFlag builds a bool flag whose fallthrough serves the "on" variation
// (or "off" if disabled). Mirrors the helper in the Java SDK's prereq test.
func prereqFlag(key string, enabled bool, prereqs []prerequisite) flagDTO {
	f := boolFlag(key, enabled, "on", "off")
	f.Prerequisites = prereqs
	return f
}

func mkPrereq(flagKey, expected string) prerequisite {
	return prerequisite{
		PrerequisiteFlagKey:  flagKey,
		ExpectedVariationKey: expected,
	}
}

func flagMap(flags ...flagDTO) map[string]flagDTO {
	m := make(map[string]flagDTO, len(flags))
	for _, f := range flags {
		m[f.Key] = f
	}
	return m
}

func TestPrereq_NoPrerequisites_BehavesUnchanged(t *testing.T) {
	flag := prereqFlag("child", true, nil)
	ctx := EvaluationContext{UserID: "u1"}

	result := evaluate(flag, ctx, nil, flagMap(flag))

	if result.Reason != ReasonFallthrough {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonFallthrough)
	}
	if result.Variation != "on" {
		t.Errorf("Variation = %q, want %q", result.Variation, "on")
	}
	if result.PrerequisiteKey != "" {
		t.Errorf("PrerequisiteKey = %q, want empty", result.PrerequisiteKey)
	}
}

func TestPrereq_Satisfied_EvaluatesChildNormally(t *testing.T) {
	parent := prereqFlag("parent", true, nil)
	child := prereqFlag("child", true, []prerequisite{mkPrereq("parent", "on")})
	ctx := EvaluationContext{UserID: "u1"}

	result := evaluate(child, ctx, nil, flagMap(parent, child))

	if result.Reason != ReasonFallthrough {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonFallthrough)
	}
	if result.Variation != "on" {
		t.Errorf("Variation = %q, want %q", result.Variation, "on")
	}
	if result.PrerequisiteKey != "" {
		t.Errorf("PrerequisiteKey = %q, want empty", result.PrerequisiteKey)
	}
}

func TestPrereq_Unsatisfied_ReturnsOffWithPrerequisiteKey(t *testing.T) {
	// parent serves "on"; child expects "off" -> mismatch
	parent := prereqFlag("parent", true, nil)
	child := prereqFlag("child", true, []prerequisite{mkPrereq("parent", "off")})
	ctx := EvaluationContext{UserID: "u1"}

	result := evaluate(child, ctx, nil, flagMap(parent, child))

	if result.Reason != ReasonPrerequisiteFailed {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonPrerequisiteFailed)
	}
	if result.Variation != "off" {
		t.Errorf("Variation = %q, want %q", result.Variation, "off")
	}
	if result.PrerequisiteKey != "parent" {
		t.Errorf("PrerequisiteKey = %q, want %q", result.PrerequisiteKey, "parent")
	}
}

func TestPrereq_DisabledParent_ServesOffSoMismatchFails(t *testing.T) {
	parent := prereqFlag("parent", false, nil) // disabled -> "off"
	child := prereqFlag("child", true, []prerequisite{mkPrereq("parent", "on")})
	ctx := EvaluationContext{UserID: "u1"}

	result := evaluate(child, ctx, nil, flagMap(parent, child))

	if result.Reason != ReasonPrerequisiteFailed {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonPrerequisiteFailed)
	}
	if result.Variation != "off" {
		t.Errorf("Variation = %q, want %q", result.Variation, "off")
	}
	if result.PrerequisiteKey != "parent" {
		t.Errorf("PrerequisiteKey = %q, want %q", result.PrerequisiteKey, "parent")
	}
}

func TestPrereq_Multiple_FirstFailingKeyIsReported(t *testing.T) {
	p1 := prereqFlag("p1", true, nil) // serves "on"
	p2 := prereqFlag("p2", true, nil) // serves "on"
	// Child requires both to serve "off" — both fail; only p1 is reported.
	child := prereqFlag("child", true, []prerequisite{
		mkPrereq("p1", "off"),
		mkPrereq("p2", "off"),
	})
	ctx := EvaluationContext{UserID: "u1"}

	result := evaluate(child, ctx, nil, flagMap(p1, p2, child))

	if result.Reason != ReasonPrerequisiteFailed {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonPrerequisiteFailed)
	}
	if result.PrerequisiteKey != "p1" {
		t.Errorf("PrerequisiteKey = %q, want %q", result.PrerequisiteKey, "p1")
	}
}

func TestPrereq_Chained_PropagatesFailureUpward(t *testing.T) {
	grandparent := prereqFlag("grandparent", true, nil) // "on"
	parent := prereqFlag("parent", true, []prerequisite{mkPrereq("grandparent", "off")})
	child := prereqFlag("child", true, []prerequisite{mkPrereq("parent", "on")})
	ctx := EvaluationContext{UserID: "u1"}

	result := evaluate(child, ctx, nil, flagMap(grandparent, parent, child))

	if result.Reason != ReasonPrerequisiteFailed {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonPrerequisiteFailed)
	}
	// Child's prereq is "parent", and parent fails -> reported key is "parent".
	if result.PrerequisiteKey != "parent" {
		t.Errorf("PrerequisiteKey = %q, want %q", result.PrerequisiteKey, "parent")
	}
}

func TestPrereq_MissingFlag_FailsSafely(t *testing.T) {
	child := prereqFlag("child", true, []prerequisite{mkPrereq("missing", "on")})
	ctx := EvaluationContext{UserID: "u1"}

	result := evaluate(child, ctx, nil, flagMap(child))

	if result.Reason != ReasonPrerequisiteFailed {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonPrerequisiteFailed)
	}
	if result.Variation != "off" {
		t.Errorf("Variation = %q, want %q", result.Variation, "off")
	}
	if result.PrerequisiteKey != "missing" {
		t.Errorf("PrerequisiteKey = %q, want %q", result.PrerequisiteKey, "missing")
	}
}

func TestPrereq_NilAllFlags_TreatedAsMissing(t *testing.T) {
	child := prereqFlag("child", true, []prerequisite{mkPrereq("parent", "on")})
	ctx := EvaluationContext{UserID: "u1"}

	result := evaluate(child, ctx, nil, nil)

	if result.Reason != ReasonPrerequisiteFailed {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonPrerequisiteFailed)
	}
	if result.PrerequisiteKey != "parent" {
		t.Errorf("PrerequisiteKey = %q, want %q", result.PrerequisiteKey, "parent")
	}
}

func TestPrereq_DepthExceeded_ReturnsErrorReason(t *testing.T) {
	// Chain of 12 flags: f0 -> f1 -> ... -> f11. maxPrerequisiteDepth is 10,
	// so f0 should hit the error path.
	flags := make(map[string]flagDTO, 12)
	for i := 0; i < 12; i++ {
		key := fmt.Sprintf("f%d", i)
		var prereqs []prerequisite
		if i < 11 {
			prereqs = []prerequisite{mkPrereq(fmt.Sprintf("f%d", i+1), "on")}
		}
		flags[key] = prereqFlag(key, true, prereqs)
	}

	result := evaluate(flags["f0"], EvaluationContext{UserID: "u1"}, nil, flags)

	if result.Reason != ReasonError {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonError)
	}
}

func TestPrereq_DisabledChildShortCircuits_IgnoresPrereqs(t *testing.T) {
	// A disabled flag should return ReasonFlagDisabled without consulting
	// prerequisites — matches JS / Java behaviour.
	child := prereqFlag("child", false, []prerequisite{mkPrereq("missing", "on")})
	ctx := EvaluationContext{UserID: "u1"}

	result := evaluate(child, ctx, nil, flagMap(child))

	if result.Reason != ReasonFlagDisabled {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonFlagDisabled)
	}
	if result.PrerequisiteKey != "" {
		t.Errorf("PrerequisiteKey = %q, want empty", result.PrerequisiteKey)
	}
}

func TestPrereq_SharedMemo_ReusesParentResult(t *testing.T) {
	parent := prereqFlag("parent", true, nil)
	childA := prereqFlag("a", true, []prerequisite{mkPrereq("parent", "on")})
	childB := prereqFlag("b", true, []prerequisite{mkPrereq("parent", "on")})
	allFlags := flagMap(parent, childA, childB)
	ctx := EvaluationContext{UserID: "u1"}
	memo := make(map[string]EvaluationDetail)

	a := evaluateWithSharedMemo(childA, ctx, nil, allFlags, memo)
	parentAfterFirst, ok := memo["parent"]
	if !ok {
		t.Fatal("memo missing parent after first evaluation")
	}

	b := evaluateWithSharedMemo(childB, ctx, nil, allFlags, memo)

	// Memo entry must be the same struct value — overwriting would replace it.
	parentAfterSecond := memo["parent"]
	if parentAfterFirst != parentAfterSecond {
		t.Errorf("parent memo entry was rewritten: before=%+v after=%+v",
			parentAfterFirst, parentAfterSecond)
	}
	if a.Variation != "on" || b.Variation != "on" {
		t.Errorf("expected both children to serve 'on'; got a=%q b=%q",
			a.Variation, b.Variation)
	}
}

func TestPrereq_SharedMemo_ConcurrentBatch_RaceClean(t *testing.T) {
	// Each goroutine gets its own memo (per-call), but they share the same
	// allFlags map. This is the "GetAllFlags" pattern: distinct top-level
	// evaluations run concurrently. Race detector should not flag the
	// read-only map access.
	parent := prereqFlag("parent", true, nil)
	flags := []flagDTO{parent}
	for i := 0; i < 20; i++ {
		flags = append(flags, prereqFlag(fmt.Sprintf("c%d", i), true,
			[]prerequisite{mkPrereq("parent", "on")}))
	}
	allFlags := flagMap(flags...)

	var wg sync.WaitGroup
	for _, f := range flags {
		wg.Add(1)
		go func(target flagDTO) {
			defer wg.Done()
			ctx := EvaluationContext{UserID: "u1"}
			detail := evaluate(target, ctx, nil, allFlags)
			if detail.Reason == ReasonError {
				t.Errorf("unexpected ReasonError for %q", target.Key)
			}
		}(f)
	}
	wg.Wait()
}

func TestPrereq_DTORoundTrip_JSONShape(t *testing.T) {
	// Verifies the wire-format JSON tags so a server payload with
	// "prerequisites": [...] deserialises into the flagDTO.
	raw := `{
		"key": "child",
		"version": 1,
		"type": "Boolean",
		"enabled": true,
		"variations": [
			{"key": "on", "value": true},
			{"key": "off", "value": false}
		],
		"rules": [],
		"fallthrough": {"type": "Fixed", "variation": "on"},
		"offVariation": "off",
		"prerequisites": [
			{"prerequisiteFlagKey": "parent", "expectedVariationKey": "on"}
		]
	}`

	var f flagDTO
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(f.Prerequisites) != 1 {
		t.Fatalf("len(Prerequisites) = %d, want 1", len(f.Prerequisites))
	}
	if f.Prerequisites[0].PrerequisiteFlagKey != "parent" {
		t.Errorf("PrerequisiteFlagKey = %q, want %q",
			f.Prerequisites[0].PrerequisiteFlagKey, "parent")
	}
	if f.Prerequisites[0].ExpectedVariationKey != "on" {
		t.Errorf("ExpectedVariationKey = %q, want %q",
			f.Prerequisites[0].ExpectedVariationKey, "on")
	}
}
