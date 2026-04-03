package featureflip

import (
	"encoding/json"
	"fmt"
	"testing"
)

// mustJSON marshals v to json.RawMessage, panicking on error.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustJSON: %v", err))
	}
	return b
}

// boolFlag creates a simple boolean flag for testing.
func boolFlag(key string, enabled bool, onKey, offKey string) flagDTO {
	return flagDTO{
		Key:     key,
		Version: 1,
		Type:    "Boolean",
		Enabled: enabled,
		Variations: []variationDTO{
			{Key: onKey, Value: mustJSON(true)},
			{Key: offKey, Value: mustJSON(false)},
		},
		Fallthrough: serveConfig{
			Type:      "Fixed",
			Variation: onKey,
		},
		OffVariation: offKey,
	}
}

func TestEvaluate_FlagDisabled(t *testing.T) {
	flag := boolFlag("test-flag", false, "on", "off")
	ctx := EvaluationContext{UserID: "user-1"}

	result := evaluate(flag, ctx, nil)

	if result.Reason != ReasonFlagDisabled {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonFlagDisabled)
	}
	if result.Variation != "off" {
		t.Errorf("Variation = %q, want %q", result.Variation, "off")
	}
	if result.Value != false {
		t.Errorf("Value = %v, want false", result.Value)
	}
}

func TestEvaluate_Fallthrough(t *testing.T) {
	flag := boolFlag("test-flag", true, "on", "off")
	ctx := EvaluationContext{UserID: "user-1"}

	result := evaluate(flag, ctx, nil)

	if result.Reason != ReasonFallthrough {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonFallthrough)
	}
	if result.Variation != "on" {
		t.Errorf("Variation = %q, want %q", result.Variation, "on")
	}
	if result.Value != true {
		t.Errorf("Value = %v, want true", result.Value)
	}
}

func TestEvaluate_RuleMatch(t *testing.T) {
	flag := boolFlag("test-flag", true, "on", "off")
	flag.Rules = []ruleDTO{
		{
			ID:       "rule-1",
			Priority: 1,
			ConditionGroups: []conditionGroup{
				{
					Operator: "And",
					Conditions: []condition{
						{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
					},
				},
			},
			Serve: serveConfig{
				Type:      "Fixed",
				Variation: "on",
			},
		},
	}

	ctx := EvaluationContext{
		UserID:     "user-1",
		Attributes: map[string]any{"country": "US"},
	}

	result := evaluate(flag, ctx, nil)

	if result.Reason != ReasonRuleMatch {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonRuleMatch)
	}
	if result.RuleID != "rule-1" {
		t.Errorf("RuleID = %q, want %q", result.RuleID, "rule-1")
	}
	if result.Variation != "on" {
		t.Errorf("Variation = %q, want %q", result.Variation, "on")
	}
	if result.Value != true {
		t.Errorf("Value = %v, want true", result.Value)
	}
}

func TestEvaluate_RuleNoMatch_Fallthrough(t *testing.T) {
	flag := boolFlag("test-flag", true, "on", "off")
	flag.Rules = []ruleDTO{
		{
			ID:       "rule-1",
			Priority: 1,
			ConditionGroups: []conditionGroup{
				{
					Operator: "And",
					Conditions: []condition{
						{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
					},
				},
			},
			Serve: serveConfig{
				Type:      "Fixed",
				Variation: "on",
			},
		},
	}

	ctx := EvaluationContext{
		UserID:     "user-1",
		Attributes: map[string]any{"country": "FR"},
	}

	result := evaluate(flag, ctx, nil)

	if result.Reason != ReasonFallthrough {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonFallthrough)
	}
	if result.RuleID != "" {
		t.Errorf("RuleID = %q, want empty", result.RuleID)
	}
}

func TestEvaluate_SegmentRule(t *testing.T) {
	flag := boolFlag("test-flag", true, "on", "off")
	flag.Rules = []ruleDTO{
		{
			ID:         "rule-seg",
			Priority:   1,
			SegmentKey: "beta-users",
			Serve: serveConfig{
				Type:      "Fixed",
				Variation: "on",
			},
		},
	}

	segments := map[string]segmentDTO{
		"beta-users": {
			Key:     "beta-users",
			Version: 1,
			Conditions: []condition{
				{Attribute: "email", Operator: "EndsWith", Values: []string{"@example.com"}},
			},
			ConditionLogic: "And",
		},
	}

	// User in segment
	ctx := EvaluationContext{
		UserID:     "user-1",
		Attributes: map[string]any{"email": "alice@example.com"},
	}
	result := evaluate(flag, ctx, segments)
	if result.Reason != ReasonRuleMatch {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonRuleMatch)
	}
	if result.RuleID != "rule-seg" {
		t.Errorf("RuleID = %q, want %q", result.RuleID, "rule-seg")
	}

	// User not in segment
	ctx2 := EvaluationContext{
		UserID:     "user-2",
		Attributes: map[string]any{"email": "bob@other.com"},
	}
	result2 := evaluate(flag, ctx2, segments)
	if result2.Reason != ReasonFallthrough {
		t.Errorf("Reason = %q, want %q", result2.Reason, ReasonFallthrough)
	}
}

func TestEvaluate_SegmentRule_MissingSegment(t *testing.T) {
	flag := boolFlag("test-flag", true, "on", "off")
	flag.Rules = []ruleDTO{
		{
			ID:         "rule-seg",
			Priority:   1,
			SegmentKey: "nonexistent",
			Serve: serveConfig{
				Type:      "Fixed",
				Variation: "on",
			},
		},
	}

	ctx := EvaluationContext{UserID: "user-1"}
	result := evaluate(flag, ctx, map[string]segmentDTO{})

	// Missing segment means rule doesn't match
	if result.Reason != ReasonFallthrough {
		t.Errorf("Reason = %q, want %q", result.Reason, ReasonFallthrough)
	}
}

func TestEvaluate_Rollout(t *testing.T) {
	flag := boolFlag("rollout-flag", true, "on", "off")
	flag.Fallthrough = serveConfig{
		Type:     "Rollout",
		BucketBy: "userId",
		Salt:     "rollout-salt",
		Variations: []weightedVariation{
			{Key: "on", Weight: 50},
			{Key: "off", Weight: 50},
		},
	}

	onCount := 0
	offCount := 0
	n := 1000

	for i := 0; i < n; i++ {
		ctx := EvaluationContext{UserID: fmt.Sprintf("user-%d", i)}
		result := evaluate(flag, ctx, nil)
		switch result.Variation {
		case "on":
			onCount++
		case "off":
			offCount++
		default:
			t.Fatalf("unexpected variation %q", result.Variation)
		}
	}

	// With 50/50 split, expect roughly 500 each. Allow wide tolerance.
	if onCount < 350 || onCount > 650 {
		t.Errorf("on=%d out of %d, expected roughly 500 (350-650)", onCount, n)
	}
	if offCount < 350 || offCount > 650 {
		t.Errorf("off=%d out of %d, expected roughly 500 (350-650)", offCount, n)
	}
	t.Logf("rollout distribution: on=%d, off=%d", onCount, offCount)
}

func TestEvaluate_RulePriority(t *testing.T) {
	flag := boolFlag("priority-flag", true, "on", "off")
	flag.Variations = append(flag.Variations, variationDTO{
		Key:   "special",
		Value: mustJSON("special-value"),
	})

	// Add rules out of priority order to verify sorting.
	flag.Rules = []ruleDTO{
		{
			ID:       "rule-low-priority",
			Priority: 10,
			ConditionGroups: []conditionGroup{
				{
					Operator: "And",
					Conditions: []condition{
						{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
					},
				},
			},
			Serve: serveConfig{Type: "Fixed", Variation: "off"},
		},
		{
			ID:       "rule-high-priority",
			Priority: 1,
			ConditionGroups: []conditionGroup{
				{
					Operator: "And",
					Conditions: []condition{
						{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
					},
				},
			},
			Serve: serveConfig{Type: "Fixed", Variation: "special"},
		},
	}

	ctx := EvaluationContext{
		UserID:     "user-1",
		Attributes: map[string]any{"country": "US"},
	}

	result := evaluate(flag, ctx, nil)

	// Lower priority number wins.
	if result.RuleID != "rule-high-priority" {
		t.Errorf("RuleID = %q, want %q (lower priority number should win)", result.RuleID, "rule-high-priority")
	}
	if result.Variation != "special" {
		t.Errorf("Variation = %q, want %q", result.Variation, "special")
	}
}

func TestEvaluate_Rollout_DefaultBucketBy(t *testing.T) {
	flag := boolFlag("rollout-flag", true, "on", "off")
	flag.Fallthrough = serveConfig{
		Type: "Rollout",
		Salt: "test-salt",
		// BucketBy is empty, should default to "userId"
		Variations: []weightedVariation{
			{Key: "on", Weight: 100},
		},
	}

	ctx := EvaluationContext{UserID: "user-1"}
	result := evaluate(flag, ctx, nil)

	if result.Variation != "on" {
		t.Errorf("Variation = %q, want %q", result.Variation, "on")
	}
}

func TestResolveVariationValue_NotFound(t *testing.T) {
	variations := []variationDTO{
		{Key: "on", Value: mustJSON(true)},
	}
	result := resolveVariationValue(variations, "nonexistent")
	if result != nil {
		t.Errorf("expected nil for missing variation, got %v", result)
	}
}

func TestResolveVariationValue_StringType(t *testing.T) {
	variations := []variationDTO{
		{Key: "color", Value: mustJSON("red")},
	}
	result := resolveVariationValue(variations, "color")
	if result != "red" {
		t.Errorf("got %v, want %q", result, "red")
	}
}

func TestResolveVariationValue_NumberType(t *testing.T) {
	variations := []variationDTO{
		{Key: "limit", Value: mustJSON(42.5)},
	}
	result := resolveVariationValue(variations, "limit")
	if result != 42.5 {
		t.Errorf("got %v, want %v", result, 42.5)
	}
}

func TestResolveVariationValue_JSONObject(t *testing.T) {
	obj := map[string]any{"theme": "dark"}
	variations := []variationDTO{
		{Key: "config", Value: mustJSON(obj)},
	}
	result := resolveVariationValue(variations, "config")
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", result)
	}
	if m["theme"] != "dark" {
		t.Errorf("theme = %v, want %q", m["theme"], "dark")
	}
}

func TestEvaluate_MultipleRules_FirstMatchWins(t *testing.T) {
	flag := boolFlag("multi-rule", true, "on", "off")
	flag.Rules = []ruleDTO{
		{
			ID:       "rule-1",
			Priority: 1,
			ConditionGroups: []conditionGroup{
				{
					Operator: "And",
					Conditions: []condition{
						{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
					},
				},
			},
			Serve: serveConfig{Type: "Fixed", Variation: "on"},
		},
		{
			ID:       "rule-2",
			Priority: 2,
			ConditionGroups: []conditionGroup{
				{
					Operator: "And",
					Conditions: []condition{
						{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
					},
				},
			},
			Serve: serveConfig{Type: "Fixed", Variation: "off"},
		},
	}

	ctx := EvaluationContext{
		UserID:     "user-1",
		Attributes: map[string]any{"country": "US"},
	}

	result := evaluate(flag, ctx, nil)

	if result.RuleID != "rule-1" {
		t.Errorf("RuleID = %q, want %q (first match should win)", result.RuleID, "rule-1")
	}
}
