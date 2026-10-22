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

// serveOff serves the flag's off variation with the given reason. An off
// variation the flag does not define is malformed config: report ReasonError
// with the key and no value, like the engine's ServeVariation (#4236).
func serveOff(flag flagDTO, reason EvaluationReason, prerequisiteKey string) EvaluationDetail {
	if !variationExists(flag.Variations, flag.OffVariation) {
		return EvaluationDetail{Variation: flag.OffVariation, Reason: ReasonError}
	}
	return EvaluationDetail{
		Value:           resolveVariationValue(flag.Variations, flag.OffVariation),
		Variation:       flag.OffVariation,
		Reason:          reason,
		PrerequisiteKey: prerequisiteKey,
	}
}

func evaluateInternal(flag flagDTO, ctx EvaluationContext, segments map[string]segmentDTO, allFlags map[string]flagDTO, depth int, memo map[string]EvaluationDetail) EvaluationDetail {
	if depth > maxPrerequisiteDepth {
		return serveOff(flag, ReasonError, "")
	}

	if !flag.Enabled {
		return serveOff(flag, ReasonFlagDisabled, "")
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
				result := serveOff(flag, ReasonPrerequisiteFailed, prereq.PrerequisiteFlagKey)
				memo[flag.Key] = result
				return result
			}

			prereqResult = evaluateInternal(prereqFlag, ctx, segments, allFlags, depth+1, memo)
			memo[prereq.PrerequisiteFlagKey] = prereqResult
		}

		if prereqResult.Reason == ReasonError {
			result := serveOff(flag, ReasonError, "")
			memo[flag.Key] = result
			return result
		}

		if prereqResult.Variation != prereq.ExpectedVariationKey {
			result := serveOff(flag, ReasonPrerequisiteFailed, prereq.PrerequisiteFlagKey)
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
			variationKey, ok := resolveServe(rule.Serve, ctx)
			if !ok {
				// The rollout can't bucket this context: the rule doesn't match (#4236).
				warnUnbucketable(flag.Key, rule.ID, bucketByOf(rule.Serve))
				continue
			}
			if !variationExists(flag.Variations, variationKey) {
				// The served key names no variation: malformed config. Report Error (no
				// rule id) with the served key, like the engine's ServeVariation (#4236).
				result := EvaluationDetail{Variation: variationKey, Reason: ReasonError}
				memo[flag.Key] = result
				return result
			}
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
	variationKey, ok := resolveServe(flag.Fallthrough, ctx)
	if !ok {
		// Serve the fallthrough's default variation rather than a rollout bucket (#4236).
		warnUnbucketable(flag.Key, "", bucketByOf(flag.Fallthrough))
		variationKey = flag.Fallthrough.Variation
	}
	if !variationExists(flag.Variations, variationKey) {
		result := EvaluationDetail{Variation: variationKey, Reason: ReasonError}
		memo[flag.Key] = result
		return result
	}
	val := resolveVariationValue(flag.Variations, variationKey)
	result := EvaluationDetail{
		Value:     val,
		Variation: variationKey,
		Reason:    ReasonFallthrough,
	}
	memo[flag.Key] = result
	return result
}

// bucketByOf returns the attribute a serve buckets on, defaulting to userId.
func bucketByOf(serve serveConfig) string {
	if serve.BucketBy == "" {
		return "userId"
	}
	return serve.BucketBy
}

// resolveServe determines which variation key to serve based on the serve config.
// For Fixed serve type, it returns the configured variation key directly.
// For Rollout serve type, it uses deterministic bucketing.
// Rollout bucketing uses serve.Salt directly (empty when absent), matching the
// engine and all SDKs (#1452).
//
// ok is false when a Rollout serve can't bucket the context: its bucketBy value
// is missing, nil, or the empty string. The caller then skips the rule or, for
// a fallthrough, serves the fallthrough's default variation (#4236).
func resolveServe(serve serveConfig, ctx EvaluationContext) (string, bool) {
	if serve.Type == "Fixed" {
		return serve.Variation, true
	}

	// A Rollout serve can arrive with no weighted variations — env-level PercentageRollout
	// has nowhere to store per-variation weights, so the mapper emits Type=Rollout with no
	// variations (#1469). Degrade to the default fixed variation instead of returning an empty
	// key. Mirrors the engine + C#/Java SDK evaluators. Never a can't-bucket case.
	if len(serve.Variations) == 0 {
		return serve.Variation, true
	}

	// Rollout
	value, _ := bucketValue(ctx, bucketByOf(serve))
	if value == "" {
		return "", false
	}

	b := bucket(serve.Salt, value)

	cumulative := 0
	for _, wv := range serve.Variations {
		cumulative += wv.Weight
		if b < cumulative {
			return wv.Key, true
		}
	}

	// Fallback: return last variation if bucket falls through (should not happen with correct
	// weights summing to 100). serve.Variations is guaranteed non-empty — the no-variations case
	// returned the default above.
	return serve.Variations[len(serve.Variations)-1].Key, true
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

// variationExists reports whether the flag defines a variation with the given
// key. Used to tell "the served key is not defined" (malformed config -> Error)
// apart from "the variation exists but its JSON value is null" — resolveVariationValue
// returns nil for both, so a nil value alone cannot distinguish them.
func variationExists(variations []variationDTO, key string) bool {
	for _, v := range variations {
		if v.Key == key {
			return true
		}
	}
	return false
}
