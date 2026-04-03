package featureflip

import "testing"

func TestForTesting_BoolVariation(t *testing.T) {
	client := ForTesting(map[string]any{
		"feature-x": true,
	})
	defer client.Close()

	ctx := EvaluationContext{UserID: "user-1"}
	result := client.BoolVariation("feature-x", ctx, false)
	if result != true {
		t.Errorf("BoolVariation = %v, want true", result)
	}
}

func TestForTesting_DefaultForUnknown(t *testing.T) {
	client := ForTesting(map[string]any{
		"feature-x": true,
	})
	defer client.Close()

	ctx := EvaluationContext{UserID: "user-1"}
	result := client.BoolVariation("nonexistent", ctx, false)
	if result != false {
		t.Errorf("BoolVariation = %v, want false (default)", result)
	}
}

func TestForTesting_StringVariation(t *testing.T) {
	client := ForTesting(map[string]any{
		"banner-text": "hello world",
	})
	defer client.Close()

	ctx := EvaluationContext{UserID: "user-1"}
	result := client.StringVariation("banner-text", ctx, "default")
	if result != "hello world" {
		t.Errorf("StringVariation = %q, want %q", result, "hello world")
	}
}

func TestForTesting_Float64Variation(t *testing.T) {
	client := ForTesting(map[string]any{
		"rate-limit": 42.5,
	})
	defer client.Close()

	ctx := EvaluationContext{UserID: "user-1"}
	result := client.Float64Variation("rate-limit", ctx, 0.0)
	if result != 42.5 {
		t.Errorf("Float64Variation = %v, want 42.5", result)
	}
}

func TestForTesting_TrackAndFlush_NoOp(t *testing.T) {
	client := ForTesting(map[string]any{
		"feature-x": true,
	})
	defer client.Close()

	ctx := EvaluationContext{UserID: "user-1"}

	// None of these should panic.
	client.Track("event-key", ctx, map[string]any{"key": "value"})
	client.Identify(ctx)
	client.Flush()
}

func TestForTesting_CloseIdempotent(t *testing.T) {
	client := ForTesting(map[string]any{
		"feature-x": true,
	})

	// Close multiple times should not panic.
	if err := client.Close(); err != nil {
		t.Errorf("first Close returned error: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Errorf("second Close returned error: %v", err)
	}
}

func TestForTesting_VariationDetail(t *testing.T) {
	client := ForTesting(map[string]any{
		"feature-x": true,
	})
	defer client.Close()

	ctx := EvaluationContext{UserID: "user-1"}
	detail := client.VariationDetail("feature-x", ctx, false)

	if detail.Reason != ReasonFallthrough {
		t.Errorf("Reason = %q, want %q", detail.Reason, ReasonFallthrough)
	}
	if detail.Value != true {
		t.Errorf("Value = %v, want true", detail.Value)
	}
	if detail.Variation != "value" {
		t.Errorf("Variation = %q, want %q", detail.Variation, "value")
	}
}

func TestForTesting_VariationDetail_NotFound(t *testing.T) {
	client := ForTesting(map[string]any{
		"feature-x": true,
	})
	defer client.Close()

	ctx := EvaluationContext{UserID: "user-1"}
	detail := client.VariationDetail("missing-flag", ctx, "fallback")

	if detail.Reason != ReasonFlagNotFound {
		t.Errorf("Reason = %q, want %q", detail.Reason, ReasonFlagNotFound)
	}
	if detail.Value != "fallback" {
		t.Errorf("Value = %v, want %q", detail.Value, "fallback")
	}
}

func TestForTesting_Initialized(t *testing.T) {
	client := ForTesting(map[string]any{})
	defer client.Close()

	if !client.Initialized() {
		t.Error("ForTesting client should be initialized")
	}
}

func TestInferType(t *testing.T) {
	tests := []struct {
		value any
		want  string
	}{
		{true, "Boolean"},
		{false, "Boolean"},
		{"hello", "String"},
		{42, "Number"},
		{42.5, "Number"},
		{int64(100), "Number"},
		{float32(1.5), "Number"},
		{map[string]any{"key": "val"}, "Json"},
		{[]int{1, 2, 3}, "Json"},
	}

	for _, tt := range tests {
		got := inferType(tt.value)
		if got != tt.want {
			t.Errorf("inferType(%v) = %q, want %q", tt.value, got, tt.want)
		}
	}
}
