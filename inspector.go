package featureflip

import (
	"log"
	"time"
)

// EvaluationEvent describes a single flag evaluation. One event is handed to
// every registered [EvaluationInspector] on every variation call, on every exit
// path (flag found, flag not found, evaluation error).
//
// The field set is the frozen cross-SDK inspector contract. Reason uses this
// SDK's native [EvaluationReason] casing (PascalCase, e.g. "RuleMatch") — the
// same values the local evaluator produces.
type EvaluationEvent struct {
	// FlagKey is the key of the flag that was evaluated.
	FlagKey string

	// Context is a copy of the full evaluation context the caller passed. The
	// Attributes map is cloned one level deep, so mutating it (or the struct)
	// cannot affect the caller's object. Treat it as read-only.
	Context EvaluationContext

	// Value is the value the caller actually receives — the typed accessors
	// notify after their coercion, so a flag whose served value does not match
	// the accessor's type reports the substituted default here, not the raw
	// evaluated value. For [Client.VariationDetail] (which applies no coercion)
	// it is the returned detail's own Value.
	Value any

	// VariationKey is the winning variation's key. Empty when the flag was not
	// found.
	VariationKey string

	// Reason is why this value was served, in this SDK's native casing.
	Reason EvaluationReason

	// RuleID is set only when Reason is [ReasonRuleMatch].
	RuleID string

	// PrerequisiteKey is set only when Reason is [ReasonPrerequisiteFailed]; it
	// names the prerequisite flag that did not serve its expected variation.
	PrerequisiteKey string

	// Timestamp is the instant the evaluation completed: an ISO-8601 (RFC 3339)
	// UTC string with sub-second precision, matching the cross-SDK contract
	// (C#, Python, PHP and Java all emit fractional seconds). Whole-second
	// truncation would make an analytics sink that de-duplicates on
	// (flagKey, userId, timestamp) silently drop repeat exposures within the
	// same second. Parse with [time.Parse] and [time.RFC3339Nano] if you need a
	// [time.Time].
	Timestamp string
}

// EvaluationInspector observes evaluations in-process, synchronously, on the
// calling goroutine. Register inspectors at client construction with
// [WithInspectors].
//
// Inspectors are void observers: they return nothing and their result cannot
// influence evaluation. A panicking inspector is isolated — the variation call
// still returns the correct value, the remaining inspectors still fire, and a
// warning is logged. Keep implementations fast and non-blocking; they run on
// the evaluation hot path.
type EvaluationInspector func(event EvaluationEvent)

// filterInspectors returns a defensive copy of the configured inspectors with
// nil entries dropped. Called once at core construction: the resulting slice is
// never mutated afterwards, so the evaluation hot path reads it without a lock.
func filterInspectors(in []EvaluationInspector) []EvaluationInspector {
	if len(in) == 0 {
		return nil
	}
	out := make([]EvaluationInspector, 0, len(in))
	for _, inspector := range in {
		if inspector != nil {
			out = append(out, inspector)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// copyContext returns a copy of ctx whose Attributes map is a distinct
// one-level clone, so an inspector cannot mutate the caller's map. Nested
// values are shared, matching the JS reference implementation's shallow copy.
func copyContext(ctx EvaluationContext) EvaluationContext {
	out := EvaluationContext{UserID: ctx.UserID}
	if ctx.Attributes != nil {
		attrs := make(map[string]any, len(ctx.Attributes))
		for k, v := range ctx.Attributes {
			attrs[k] = v
		}
		out.Attributes = attrs
	}
	return out
}

// notifyInspectors fires the registered evaluation inspectors once per
// variation call with the detail the caller actually receives. Called by each
// public variation method after it has applied its coercion — never by
// evaluateFlag — so exactly one event is emitted per call. Allocates nothing
// when no inspectors are registered.
//
// Once the shared core has shut down (the last [Client] handle was closed) no
// event is emitted, matching the Python and Ruby SDKs. The variation call still
// returns its normal value; only the notification is suppressed.
func (sc *sharedCore) notifyInspectors(flagKey string, ctx EvaluationContext, detail EvaluationDetail) {
	if len(sc.inspectors) == 0 || sc.isShutDown() {
		return
	}

	event := EvaluationEvent{
		FlagKey:         flagKey,
		Context:         copyContext(ctx),
		Value:           detail.Value,
		VariationKey:    detail.Variation,
		Reason:          detail.Reason,
		RuleID:          detail.RuleID,
		PrerequisiteKey: detail.PrerequisiteKey,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
	}

	for _, inspector := range sc.inspectors {
		invokeInspector(inspector, event)
	}
}

// invokeInspector calls one inspector with panic isolation: a panic is
// recovered and logged so it neither breaks evaluation nor stops the remaining
// inspectors from firing.
func invokeInspector(inspector EvaluationInspector, event EvaluationEvent) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[featureflip] evaluation inspector panicked: %v", r)
		}
	}()
	inspector(event)
}
