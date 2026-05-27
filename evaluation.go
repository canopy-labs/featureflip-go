package featureflip

import (
	"encoding/json"
	"fmt"
	"sort"
)

// maxPrerequisiteDepth is a safety net against pathological prerequisite chains.
// Cycles are blocked at write time on the server, so reaching this limit
// indicates a corrupt config — the evaluator returns [ReasonError].
const maxPrerequisiteDepth = 10

// evaluate performs local flag evaluation using the provided flag definition,
// evaluation context, segment definitions, and the full flag map (used to
// resolve prerequisites). Pass nil for allFlags when the flag is known to
// have no prerequisites; any declared prerequisite will then fail safely as
// if missing.
//
// Algorithm:
//  1. If the flag is disabled, return offVariation with ReasonFlagDisabled.
//  2. Resolve prerequisites recursively. On mismatch or missing prereq,
//     return offVariation with ReasonPrerequisiteFailed and PrerequisiteKey
//     set. Error reasons from prereq evaluation bubble up as ReasonError.
//  3. Sort rules by priority (ascending) and evaluate each. The first matching
//     rule determines the result (ReasonRuleMatch).
//  4. If no rules match, use the flag's fallthrough serve config (ReasonFallthrough).
func evaluate(flag flagDTO, ctx EvaluationContext, segments map[string]segmentDTO, allFlags map[string]flagDTO) EvaluationDetail {
	memo := make(map[string]EvaluationDetail)
	return evaluateInternal(flag, ctx, segments, allFlags, 0, memo)
}

// evaluateWithSharedMemo is like [evaluate] but threads an externally-owned
// memoisation map through the call. Use this when evaluating multiple flags
// in one sweep (e.g. an "evaluate all" pass) so that a shared prerequisite
// is only evaluated once.
func evaluateWithSharedMemo(flag flagDTO, ctx EvaluationContext, segments map[string]segmentDTO, allFlags map[string]flagDTO, memo map[string]EvaluationDetail) EvaluationDetail {
	return evaluateInternal(flag, ctx, segments, allFlags, 0, memo)
}

func evaluateInternal(flag flagDTO, ctx EvaluationContext, segments map[string]segmentDTO, allFlags map[string]flagDTO, depth int, memo map[string]EvaluationDetail) EvaluationDetail {
	if depth > maxPrerequisiteDepth {
		val := resolveVariationValue(flag.Variations, flag.OffVariation)
		return EvaluationDetail{
			Value:     val,
			Variation: flag.OffVariation,
			Reason:    ReasonError,
		}
	}

	if !flag.Enabled {
		val := resolveVariationValue(flag.Variations, flag.OffVariation)
		return EvaluationDetail{
			Value:     val,
			Variation: flag.OffVariation,
			Reason:    ReasonFlagDisabled,
		}
	}

	// Resolve prerequisites in order. A failing prerequisite short-circuits to
	// the off variation with ReasonPrerequisiteFailed; ReasonError propagates
	// upward unchanged.
	for _, prereq := range flag.Prerequisites {
		prereqResult, cached := memo[prereq.PrerequisiteFlagKey]

		if !cached {
			prereqFlag, ok := allFlags[prereq.PrerequisiteFlagKey]
			if !ok {
				// Missing flag: fail safely. Memo the current flag's result
				// under flag.Key — the prereq itself has no result to memo.
				val := resolveVariationValue(flag.Variations, flag.OffVariation)
				result := EvaluationDetail{
					Value:           val,
					Variation:       flag.OffVariation,
					Reason:          ReasonPrerequisiteFailed,
					PrerequisiteKey: prereq.PrerequisiteFlagKey,
				}
				memo[flag.Key] = result
				return result
			}

			prereqResult = evaluateInternal(prereqFlag, ctx, segments, allFlags, depth+1, memo)
			memo[prereq.PrerequisiteFlagKey] = prereqResult
		}

		if prereqResult.Reason == ReasonError {
			val := resolveVariationValue(flag.Variations, flag.OffVariation)
			result := EvaluationDetail{
				Value:     val,
				Variation: flag.OffVariation,
				Reason:    ReasonError,
			}
			memo[flag.Key] = result
			return result
		}

		if prereqResult.Variation != prereq.ExpectedVariationKey {
			val := resolveVariationValue(flag.Variations, flag.OffVariation)
			result := EvaluationDetail{
				Value:           val,
				Variation:       flag.OffVariation,
				Reason:          ReasonPrerequisiteFailed,
				PrerequisiteKey: prereq.PrerequisiteFlagKey,
			}
			memo[flag.Key] = result
			return result
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
			result := EvaluationDetail{
				Value:     val,
				Variation: variationKey,
				Reason:    ReasonRuleMatch,
				RuleID:    rule.ID,
			}
			memo[flag.Key] = result
			return result
		}
	}

	// No rules matched — fallthrough.
	variationKey := resolveServe(flag.Fallthrough, ctx, flag.Key)
	val := resolveVariationValue(flag.Variations, variationKey)
	result := EvaluationDetail{
		Value:     val,
		Variation: variationKey,
		Reason:    ReasonFallthrough,
	}
	memo[flag.Key] = result
	return result
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
