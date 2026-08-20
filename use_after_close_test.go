package featureflip

import (
	"testing"
	"time"
)

// A closed handle must return the caller's default from every accessor and
// report Initialized() == false (#2289).
//
// Close() releases the core, which stops streaming/polling and shuts down the
// event processor — but the in-memory store stays readable. Without a guard the
// handle keeps serving a frozen snapshot that can never update again, while
// still reporting itself initialized. Python and PHP already implement and
// document the guarded behaviour; this brings Go in line.

// newClosableClient builds a network-free client over the inspector fixtures.
// flag-on is enabled and serves true via fallthrough.
func newClosableClient() *Client {
	cfg := defaultConfig()
	core := newSharedCore("use-after-close-key", cfg)
	core.store.setAll(inspectorFlags(), nil)
	core.initialized = true
	core.ep = newEventProcessor(nil, 100, time.Hour)
	return &Client{core: core}
}

func TestUseAfterClose_EvaluationsReturnTheCallerDefault(t *testing.T) {
	client := newClosableClient()
	ctx := EvaluationContext{UserID: "u"}

	// Sanity: the fixture really does serve a non-default value while open, so a
	// pass below can't come from an empty store.
	if got := client.BoolVariation("flag-on", ctx, false); got != true {
		t.Fatalf("precondition: BoolVariation = %v, want true while open", got)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	if got := client.BoolVariation("flag-on", ctx, false); got != false {
		t.Errorf("BoolVariation after close = %v, want false (the caller's default), not the stale value", got)
	}
	if got := client.StringVariation("flag-string", ctx, "DEF"); got != "DEF" {
		t.Errorf("StringVariation after close = %q, want %q", got, "DEF")
	}
	if got := client.Float64Variation("flag-number", ctx, -1); got != -1 {
		t.Errorf("Float64Variation after close = %v, want -1", got)
	}
	if got := client.JSONVariation("flag-string", ctx, nil); got != nil {
		t.Errorf("JSONVariation after close = %#v, want nil (the caller's default)", got)
	}
}

func TestUseAfterClose_InitializedReportsFalse(t *testing.T) {
	client := newClosableClient()

	if !client.Initialized() {
		t.Fatal("precondition: Initialized() should be true while open")
	}

	_ = client.Close()

	if client.Initialized() {
		t.Error("Initialized() after close = true, want false")
	}
}

func TestUseAfterClose_VariationDetailReturnsTheDefault(t *testing.T) {
	client := newClosableClient()
	ctx := EvaluationContext{UserID: "u"}
	_ = client.Close()

	detail := client.VariationDetail("flag-on", ctx, false)
	if detail.Value != false {
		t.Errorf("VariationDetail value after close = %#v, want false (the caller's default)", detail.Value)
	}
}

// Close() is documented as idempotent — that part already worked and must stay.
func TestUseAfterClose_DoubleCloseIsANoOp(t *testing.T) {
	client := newClosableClient()

	if err := client.Close(); err != nil {
		t.Fatalf("first Close() = %v, want nil", err)
	}
	if err := client.Close(); err != nil {
		t.Errorf("second Close() = %v, want nil", err)
	}
}

// An open handle must be completely unaffected by the guard.
func TestUseAfterClose_OpenClientIsUnaffected(t *testing.T) {
	client := newClosableClient()
	defer client.Close()
	ctx := EvaluationContext{UserID: "u"}

	if got := client.BoolVariation("flag-on", ctx, false); got != true {
		t.Errorf("BoolVariation = %v, want true", got)
	}
	if !client.Initialized() {
		t.Error("Initialized() = false, want true")
	}
}
