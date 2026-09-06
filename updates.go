package featureflip

import (
	"reflect"
	"sort"
)

// FlagUpdateListener is called with the flag keys whose configuration changed,
// batched into one call per update.
//
// It fires on the SDK's streaming or polling goroutine, so it must not block:
// a slow listener delays flag delivery and, on the SSE path, can stall the
// connection.
type FlagUpdateListener func(flagKeys []string)

// diffSnapshot reports the flag keys whose evaluated value may differ between
// two snapshots.
//
// The store is handed a FULL snapshot on every poll tick and on every SSE
// `sync` reconnect, so notifying on each one would report "everything changed"
// once per poll interval. This reduces a snapshot to the keys that could
// actually have moved.
//
// Mirrors `packages/js-sdk/src/core/store.ts` (`diffSnapshot`) and the Python
// SDK's `_updates.diff_snapshot`; the semantics are cross-SDK contract, not an
// implementation detail.
//
// Comparison is `reflect.DeepEqual` rather than `==`: the DTOs carry slices and
// `json.RawMessage`, so they are not comparable with `==` at all — that is a
// compile error, not a silent wrong answer, but DeepEqual is also the only
// form that actually compares the configuration rather than a header field.
// Version numbers are deliberately NOT used: a segment edit moves a flag's
// evaluated value without bumping that flag's version.
func diffSnapshot(
	previousFlags map[string]flagDTO,
	previousSegments map[string]segmentDTO,
	nextFlags []flagDTO,
	nextSegments []segmentDTO,
) []string {
	changed := make(map[string]struct{})

	nextFlagKeys := make(map[string]struct{}, len(nextFlags))
	for _, flag := range nextFlags {
		nextFlagKeys[flag.Key] = struct{}{}
		previous, ok := previousFlags[flag.Key]
		if !ok || !reflect.DeepEqual(previous, flag) {
			changed[flag.Key] = struct{}{}
		}
	}
	// Removed server-side: still a change for anyone holding the old value.
	for key := range previousFlags {
		if _, ok := nextFlagKeys[key]; !ok {
			changed[key] = struct{}{}
		}
	}

	changedSegments := make(map[string]struct{})
	nextSegmentKeys := make(map[string]struct{}, len(nextSegments))
	for _, segment := range nextSegments {
		nextSegmentKeys[segment.Key] = struct{}{}
		previous, ok := previousSegments[segment.Key]
		if !ok || !reflect.DeepEqual(previous, segment) {
			changedSegments[segment.Key] = struct{}{}
		}
	}
	for key := range previousSegments {
		if _, ok := nextSegmentKeys[key]; !ok {
			changedSegments[key] = struct{}{}
		}
	}

	if len(changedSegments) > 0 {
		// A segment edit alters evaluation outcomes without bumping any flag's
		// version, so the flags referencing it have to be pulled in by hand.
		//
		// Only the incoming flags need scanning: a flag that dropped its
		// reference to a changed segment must have had its own rules edited,
		// which changes its own config and is already reported above.
		for _, flag := range nextFlags {
			for _, rule := range flag.Rules {
				if rule.SegmentKey == "" {
					continue
				}
				if _, ok := changedSegments[rule.SegmentKey]; ok {
					changed[flag.Key] = struct{}{}
					break
				}
			}
		}
	}

	// Must run last: it walks out from the fully-resolved changed set, so a
	// flag that only the segment scan above pulled in still reaches its own
	// dependents.
	addPrerequisiteDependents(nextFlags, changed)

	return sortedKeys(changed)
}

// addPrerequisiteDependents expands changed in place with every transitive
// prerequisite dependent.
//
// A flag's version covers its own prerequisite rows but not the flags those
// rows point at, so toggling a prerequisite bumps only the prerequisite's
// version. The evaluator resolves prerequisites recursively, so the dependent's
// value still flips — to its off variation, with [ReasonPrerequisiteFailed] —
// leaving the dependent silently absent from the change notification.
//
// Walks reverse edges out from the changed set rather than scanning every
// flag's chain, so cost is proportional to the affected subgraph rather than to
// the whole snapshot. Over-reporting is harmless (a listener re-reads a flag
// that evaluates the same); under-reporting is the bug this prevents.
//
// flags is normally the INCOMING snapshot, so a removed flag still resolves its
// dependents: they are still present and still carry the now-dangling
// prerequisite row.
func addPrerequisiteDependents(flags []flagDTO, changed map[string]struct{}) {
	if len(changed) == 0 {
		return
	}

	dependents := make(map[string][]string)
	for _, flag := range flags {
		for _, prerequisite := range flag.Prerequisites {
			key := prerequisite.PrerequisiteFlagKey
			if key == "" {
				continue
			}
			dependents[key] = append(dependents[key], flag.Key)
		}
	}
	if len(dependents) == 0 {
		return
	}

	// changed doubles as the visited set, so a prerequisite cycle (rejected by
	// the server, but reachable via a malformed payload) terminates instead of
	// looping forever. No depth cap is needed for the same reason — unlike the
	// evaluator, this walk visits each flag at most once.
	queue := make([]string, 0, len(changed))
	for key := range changed {
		queue = append(queue, key)
	}
	for len(queue) > 0 {
		key := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, dependent := range dependents[key] {
			if _, seen := changed[dependent]; !seen {
				changed[dependent] = struct{}{}
				queue = append(queue, dependent)
			}
		}
	}
}

// sortedKeys renders a key set as a sorted slice, so a listener sees a stable
// order rather than Go's randomized map iteration.
func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
