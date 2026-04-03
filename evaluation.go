package featureflip

import (
	"encoding/json"
	"fmt"
	"sort"
)

// evaluate performs local flag evaluation using the provided flag definition,
// evaluation context, and segment definitions.
//
// Algorithm:
//  1. If the flag is disabled, return offVariation with ReasonFlagDisabled.
//  2. Sort rules by priority (ascending) and evaluate each. The first matching
//     rule determines the result (ReasonRuleMatch).
//  3. If no rules match, use the flag's fallthrough serve config (ReasonFallthrough).
func evaluate(flag flagDTO, ctx EvaluationContext, segments map[string]segmentDTO) EvaluationDetail {
	if !flag.Enabled {
		val := resolveVariationValue(flag.Variations, flag.OffVariation)
		return EvaluationDetail{
			Value:     val,
			Variation: flag.OffVariation,
			Reason:    ReasonFlagDisabled,
		}
	}

	// Sort rules by priority ascending.
	rules := make([]ruleDTO, len(flag.Rules))
	copy(rules, flag.Rules)
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority < rules[j].Priority
	})

	for _, rule := range rules {
		matched := false

		if rule.SegmentKey != "" {
			seg, ok := segments[rule.SegmentKey]
			if ok {
				matched = evaluateConditions(seg.Conditions, seg.ConditionLogic, ctx)
			}
		} else {
			matched = evaluateConditionGroups(rule.ConditionGroups, ctx)
		}

		if matched {
			variationKey := resolveServe(rule.Serve, ctx, flag.Key)
			val := resolveVariationValue(flag.Variations, variationKey)
			return EvaluationDetail{
				Value:     val,
				Variation: variationKey,
				Reason:    ReasonRuleMatch,
				RuleID:    rule.ID,
			}
		}
	}

	// No rules matched — fallthrough.
	variationKey := resolveServe(flag.Fallthrough, ctx, flag.Key)
	val := resolveVariationValue(flag.Variations, variationKey)
	return EvaluationDetail{
		Value:     val,
		Variation: variationKey,
		Reason:    ReasonFallthrough,
	}
}

// resolveServe determines which variation key to serve based on the serve config.
// For Fixed serve type, it returns the configured variation key directly.
// For Rollout serve type, it uses deterministic bucketing.
// flagKey is used as a fallback salt when serve.Salt is empty, matching
// the behavior of the C# and Java SDKs.
func resolveServe(serve serveConfig, ctx EvaluationContext, flagKey string) string {
	if serve.Type == "Fixed" {
		return serve.Variation
	}

	// Rollout
	bucketBy := serve.BucketBy
	if bucketBy == "" {
		bucketBy = "userId"
	}

	value, _ := getAttributeValue(ctx, bucketBy)
	salt := serve.Salt
	if salt == "" {
		salt = flagKey
	}
	b := bucket(salt, value)

	cumulative := 0
	for _, wv := range serve.Variations {
		cumulative += wv.Weight
		if b < cumulative {
			return wv.Key
		}
	}

	// Fallback: return last variation if bucket falls through
	// (should not happen with correct weights summing to 100).
	if len(serve.Variations) > 0 {
		return serve.Variations[len(serve.Variations)-1].Key
	}
	return ""
}

// resolveVariationValue finds a variation by key and unmarshals its JSON value.
// Returns nil if the variation key is not found.
func resolveVariationValue(variations []variationDTO, key string) any {
	for _, v := range variations {
		if v.Key == key {
			var result any
			if err := json.Unmarshal(v.Value, &result); err != nil {
				return fmt.Sprintf("<unmarshal error: %v>", err)
			}
			return result
		}
	}
	return nil
}
