package featureflip

import (
	"encoding/json"
	"math"
	"testing"
)

func TestCanonicalNumber(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1.0, "1"}, {1.5, "1.5"}, {0.1, "0.1"}, {0.30000000000000004, "0.30000000000000004"},
		{math.Copysign(0, -1), "0"}, {0, "0"}, {100.25, "100.25"}, {1e21, "1e+21"},
		{1.5e21, "1.5e+21"}, {1e20, "100000000000000000000"}, {1e-7, "1e-7"},
		{0.000001, "0.000001"}, {2.5e-7, "2.5e-7"}, {123456.789, "123456.789"},
		{-1.5, "-1.5"}, {-1e-7, "-1e-7"}, {-1e20, "-100000000000000000000"}, {100, "100"},
		{math.NaN(), "NaN"}, {math.Inf(1), "Infinity"}, {math.Inf(-1), "-Infinity"},
		{5e-324, "5e-324"}, {math.MaxFloat64, "1.7976931348623157e+308"},
	}
	for _, c := range cases {
		if got := canonicalNumber(c.in, 64); got != c.want {
			t.Errorf("canonicalNumber(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := canonicalNumber(float64(float32(0.1)), 32); got != "0.1" {
		t.Errorf("float32 0.1 = %q", got)
	}
}

func TestBucketValue(t *testing.T) {
	cases := []struct {
		name string
		ctx  EvaluationContext
		attr string
		want string
		ok   bool
	}{
		{"int", EvaluationContext{Attributes: map[string]any{"a": 42}}, "a", "42", true},
		{"negative int", EvaluationContext{Attributes: map[string]any{"a": -7}}, "a", "-7", true},
		{"float64 whole", EvaluationContext{Attributes: map[string]any{"a": 1.0}}, "a", "1", true},
		{"bool", EvaluationContext{Attributes: map[string]any{"a": false}}, "a", "false", true},
		{"json number int", EvaluationContext{Attributes: map[string]any{"a": json.Number("12345678901234567890")}}, "a", "12345678901234567890", true},
		{"json number decimal", EvaluationContext{Attributes: map[string]any{"a": json.Number("1.0")}}, "a", "1", true},
		{"user_id attribute zero", EvaluationContext{Attributes: map[string]any{"user_id": 0}}, "user_id", "0", true},
		{"userId empty", EvaluationContext{}, "userId", "", false},
		{"nil", EvaluationContext{Attributes: map[string]any{"a": nil}}, "a", "", false},
	}
	for _, c := range cases {
		got, ok := bucketValue(c.ctx, c.attr)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}
