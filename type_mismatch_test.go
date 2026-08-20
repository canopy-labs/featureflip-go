package featureflip

import "testing"

// A typed accessor whose served value is not of the requested type already
// substituted the caller's default, but reported the evaluator's success reason —
// so callers had no signal that a mismatch had happened at all. It now reports
// ReasonError (#2281).
//
// The fixtures come from inspectorFlags(): flag-string serves "hello",
// flag-number serves 42.5, flag-on serves the boolean true.

func TestTypeMismatch_ReportsError_BoolVariationOnStringFlag(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	if got := client.BoolVariation("flag-string", EvaluationContext{UserID: "bob"}, true); got != true {
		t.Fatalf("BoolVariation = %v, want true (the caller's default)", got)
	}

	e := c.at(t, 0)
	if e.Reason != ReasonError {
		t.Errorf("Reason = %q, want %q", e.Reason, ReasonError)
	}
	if e.Value != true {
		t.Errorf("Value = %#v, want true (the value the caller received)", e.Value)
	}
}

func TestTypeMismatch_ReportsError_Float64VariationOnStringFlag(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	if got := client.Float64Variation("flag-string", EvaluationContext{UserID: "bob"}, 1.5); got != 1.5 {
		t.Fatalf("Float64Variation = %v, want 1.5 (the caller's default)", got)
	}

	if e := c.at(t, 0); e.Reason != ReasonError {
		t.Errorf("Reason = %q, want %q", e.Reason, ReasonError)
	}
}

func TestTypeMismatch_ReportsError_StringVariationOnNumberFlag(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	if got := client.StringVariation("flag-number", EvaluationContext{UserID: "bob"}, "fallback"); got != "fallback" {
		t.Fatalf("StringVariation = %q, want %q (the caller's default)", got, "fallback")
	}

	if e := c.at(t, 0); e.Reason != ReasonError {
		t.Errorf("Reason = %q, want %q", e.Reason, ReasonError)
	}
}

func TestTypeMismatch_ReportsError_StringVariationOnBoolFlag(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	if got := client.StringVariation("flag-on", EvaluationContext{UserID: "bob"}, "fallback"); got != "fallback" {
		t.Fatalf("StringVariation = %q, want %q (the caller's default)", got, "fallback")
	}

	if e := c.at(t, 0); e.Reason != ReasonError {
		t.Errorf("Reason = %q, want %q", e.Reason, ReasonError)
	}
}

// --- Matching reads must keep their real reason ---

func TestTypeMismatch_MatchingReadKeepsItsRealReason(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	if got := client.BoolVariation("flag-on", EvaluationContext{UserID: "bob"}, false); got != true {
		t.Fatalf("BoolVariation = %v, want true", got)
	}

	e := c.at(t, 0)
	if e.Reason != ReasonFallthrough {
		t.Errorf("Reason = %q, want %q", e.Reason, ReasonFallthrough)
	}
	if e.VariationKey != "on" {
		t.Errorf("VariationKey = %q, want %q", e.VariationKey, "on")
	}
}

// JSONVariation takes an `any` default, so there is no requested type to
// mismatch against — it stays unchecked and keeps serving the raw value.
func TestTypeMismatch_JSONVariationIsUnchecked(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	if got := client.JSONVariation("flag-string", EvaluationContext{UserID: "bob"}, nil); got != "hello" {
		t.Fatalf("JSONVariation = %#v, want %q", got, "hello")
	}

	if e := c.at(t, 0); e.Reason != ReasonFallthrough {
		t.Errorf("Reason = %q, want %q", e.Reason, ReasonFallthrough)
	}
}
