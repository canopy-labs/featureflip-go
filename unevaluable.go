package featureflip

import (
	"fmt"
	"log"
)

// Entity-level drop for enum values this SDK build cannot evaluate (#2402).
//
// ServeType and ConditionLogic are the two enums that are BOTH carried on the wire as
// strings AND consulted by the evaluator, and each dispatches on a two-way branch with
// no third arm:
//
//	serve.Type == "Fixed" ... else ROLLOUT
//	logic      == "And"   ... else ANY (OR)
//
// So an unrecognised value does not fail — it takes the ELSE arm. A segment carrying
// conditionLogic "Xor" evaluates as OR, so a segment meant to require ALL of its
// conditions matches ANY of them: the rule fails OPEN and over-targets. Go is
// especially exposed because encoding/json accepts any string into these fields
// without complaint; the sync guard that catches integer enums (#2288) never fires.
//
// Neither obvious fix works. Tolerating the value — the way an unknown flag type is
// tolerated (#2401) — IS the silent mis-evaluation above; flag.Type is safe to tolerate
// only because nothing evaluates it. Rejecting the payload means one additive server
// change takes down every flag on a pinned client (the #2372/#2395 outage shape).
//
// So the containing entity goes instead: the caller gets their default for exactly the
// affected flag via the FlagNotFound path, and every other flag keeps serving. Dropping
// a segment leaves rules pointing at it dangling, which is safe because evaluateRule
// already treats an unresolvable SegmentKey as no-match — the cascade fails CLOSED, and
// the engine-generated f-segment-unresolvable golden vector pins it.
//
// Scoped deliberately to a NON-EMPTY unrecognised value. An empty string is the field
// being ABSENT, which is the missing-required-field axis, not this one — and the SDKs
// already, deliberately, disagree there: ruby, python and php default an absent
// conditionLogic to "And", while js rejects the whole payload for a missing
// fallthrough. Dropping the entity on "" would not converge that divergence, it would
// add a fourth behaviour, and it would change the handling of payloads that work today
// over a case the server never emits. Keeping the check to values that are present and
// unrecognised makes this change purely additive.

var (
	knownServeTypes     = map[string]struct{}{"Fixed": {}, "Rollout": {}}
	knownConditionLogic = map[string]struct{}{"And": {}, "Or": {}}
)

// unevaluableFlagReason explains why a flag cannot be evaluated, or returns "" if it
// can. A reason rather than a bool so the diagnostic can name the field and the value
// actually received — the one detail an operator debugging a vanished flag needs.
func unevaluableFlagReason(f flagDTO) string {
	if r := unevaluableServeReason(f.Fallthrough, "fallthrough"); r != "" {
		return r
	}

	for _, rule := range f.Rules {
		if r := unevaluableServeReason(rule.Serve, fmt.Sprintf("rule[%s].serve", rule.ID)); r != "" {
			return r
		}
		for _, g := range rule.ConditionGroups {
			if g.Operator == "" {
				continue
			}
			if _, ok := knownConditionLogic[g.Operator]; !ok {
				return fmt.Sprintf(
					"rule[%s].conditionGroup.operator %q is not a condition logic this SDK version understands",
					rule.ID, g.Operator)
			}
		}
	}

	return ""
}

// unevaluableSegmentReason explains why a segment cannot be evaluated, or returns "".
func unevaluableSegmentReason(s segmentDTO) string {
	if s.ConditionLogic == "" {
		return ""
	}
	if _, ok := knownConditionLogic[s.ConditionLogic]; !ok {
		return fmt.Sprintf(
			"conditionLogic %q is not a condition logic this SDK version understands",
			s.ConditionLogic)
	}
	return ""
}

func unevaluableServeReason(serve serveConfig, path string) string {
	if serve.Type == "" {
		return ""
	}
	if _, ok := knownServeTypes[serve.Type]; !ok {
		return fmt.Sprintf("%s.type %q is not a serve type this SDK version understands", path, serve.Type)
	}
	return ""
}

// dropUnevaluable splits a decoded snapshot into the entities this build can evaluate
// and the ones it cannot, logging one line per drop. Never fails the payload: an
// unknown enum NAME is a forward-compatibility event, not a contract violation, and the
// healthy remainder must still apply. That is a different axis from the wrong-TYPE
// check json.Unmarshal performs, which still discards the frame wholesale.
func dropUnevaluable(flags []flagDTO, segments []segmentDTO) ([]flagDTO, []segmentDTO) {
	keptFlags := make([]flagDTO, 0, len(flags))
	for _, f := range flags {
		if reason := unevaluableFlagReason(f); reason != "" {
			log.Printf("[featureflip] dropping flag %q: %s. This SDK version may be older "+
				"than the flag configuration; the rest of the configuration was applied.", f.Key, reason)
			continue
		}
		keptFlags = append(keptFlags, f)
	}

	keptSegments := make([]segmentDTO, 0, len(segments))
	for _, s := range segments {
		if reason := unevaluableSegmentReason(s); reason != "" {
			log.Printf("[featureflip] dropping segment %q: %s. This SDK version may be older "+
				"than the flag configuration; the rest of the configuration was applied.", s.Key, reason)
			continue
		}
		keptSegments = append(keptSegments, s)
	}

	return keptFlags, keptSegments
}
