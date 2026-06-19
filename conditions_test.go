package featureflip

import (
	"encoding/json"
	"testing"
)

// --- getAttributeValue ---

func TestGetAttributeValue_UserID(t *testing.T) {
	ctx := EvaluationContext{UserID: "user-42"}
	val, ok := getAttributeValue(ctx, "user_id")
	if !ok {
		t.Fatal("expected ok=true for user_id")
	}
	if val != "user-42" {
		t.Errorf("got %q, want %q", val, "user-42")
	}
}

func TestGetAttributeValue_UserID_Camel(t *testing.T) {
	ctx := EvaluationContext{UserID: "user-42"}
	val, ok := getAttributeValue(ctx, "userId")
	if !ok {
		t.Fatal("expected ok=true for userId")
	}
	if val != "user-42" {
		t.Errorf("got %q, want %q", val, "user-42")
	}
}

// The userId/user_id alias is case-sensitive, matching the engine's
// EvaluationContext.GetAttribute and the other SDKs (#1460). A differently
// cased spelling does NOT resolve to the built-in UserID — it falls through to
// a (here absent) custom attribute lookup.
func TestGetAttributeValue_UserID_CaseSensitive(t *testing.T) {
	ctx := EvaluationContext{UserID: "user-42"}
	for _, alias := range []string{"User_ID", "USERID", "UserId", "userid"} {
		if _, ok := getAttributeValue(ctx, alias); ok {
			t.Errorf("alias %q should NOT resolve to the built-in UserID (case-sensitive)", alias)
		}
	}
}

func TestGetAttributeValue_UserID_Empty(t *testing.T) {
	ctx := EvaluationContext{}
	_, ok := getAttributeValue(ctx, "user_id")
	if ok {
		t.Error("expected ok=false for empty UserID")
	}
}

func TestGetAttributeValue_CustomAttribute(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "US"},
	}
	val, ok := getAttributeValue(ctx, "country")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if val != "US" {
		t.Errorf("got %q, want %q", val, "US")
	}
}

func TestGetAttributeValue_MissingAttribute(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "US"},
	}
	_, ok := getAttributeValue(ctx, "email")
	if ok {
		t.Error("expected ok=false for missing attribute")
	}
}

func TestGetAttributeValue_NilAttributes(t *testing.T) {
	ctx := EvaluationContext{UserID: "user-1"}
	_, ok := getAttributeValue(ctx, "email")
	if ok {
		t.Error("expected ok=false when Attributes is nil")
	}
}

func TestGetAttributeValue_NumericAttribute(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"age": 25},
	}
	val, ok := getAttributeValue(ctx, "age")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if val != "25" {
		t.Errorf("got %q, want %q", val, "25")
	}
}

// A context key set to an explicit nil is "absent", not "<nil>" — the
// cross-SDK contract (#1460) requires resolution to report the attribute as
// missing so callers short-circuit to condition.Negate rather than
// stringifying the nil and running the operator against it (#1484).
func TestGetAttributeValue_PresentButNilAttribute(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"plan": nil},
	}
	_, ok := getAttributeValue(ctx, "plan")
	if ok {
		t.Error("expected ok=false for a present-but-nil attribute")
	}
}

// --- evaluateOperator: Equals/In ---

func TestOperator_Equals(t *testing.T) {
	if !evaluateOperator("Equals", "hello", []string{"hello"}) {
		t.Error("Equals should match exact value")
	}
	if !evaluateOperator("Equals", "Hello", []string{"hello"}) {
		t.Error("Equals should be case-insensitive")
	}
	if evaluateOperator("Equals", "hello", []string{"world"}) {
		t.Error("Equals should not match different value")
	}
}

func TestOperator_In(t *testing.T) {
	if !evaluateOperator("In", "US", []string{"US", "CA", "GB"}) {
		t.Error("In should match when value is in targets")
	}
	if evaluateOperator("In", "FR", []string{"US", "CA", "GB"}) {
		t.Error("In should not match when value is not in targets")
	}
	if !evaluateOperator("In", "us", []string{"US", "CA"}) {
		t.Error("In should be case-insensitive")
	}
}

// --- evaluateOperator: NotEquals/NotIn ---

func TestOperator_NotEquals(t *testing.T) {
	if !evaluateOperator("NotEquals", "hello", []string{"world"}) {
		t.Error("NotEquals should match when value differs")
	}
	if evaluateOperator("NotEquals", "hello", []string{"hello"}) {
		t.Error("NotEquals should not match when value equals target")
	}
	if evaluateOperator("NotEquals", "Hello", []string{"hello"}) {
		t.Error("NotEquals should be case-insensitive")
	}
}

func TestOperator_NotIn(t *testing.T) {
	if !evaluateOperator("NotIn", "FR", []string{"US", "CA"}) {
		t.Error("NotIn should match when value is not in targets")
	}
	if evaluateOperator("NotIn", "US", []string{"US", "CA"}) {
		t.Error("NotIn should not match when value is in targets")
	}
}

// --- evaluateOperator: Contains/NotContains ---

func TestOperator_Contains(t *testing.T) {
	if !evaluateOperator("Contains", "hello world", []string{"world"}) {
		t.Error("Contains should match substring")
	}
	if !evaluateOperator("Contains", "Hello World", []string{"hello"}) {
		t.Error("Contains should be case-insensitive")
	}
	if evaluateOperator("Contains", "hello", []string{"xyz"}) {
		t.Error("Contains should not match absent substring")
	}
}

func TestOperator_NotContains(t *testing.T) {
	if !evaluateOperator("NotContains", "hello", []string{"xyz"}) {
		t.Error("NotContains should match when substring absent")
	}
	if evaluateOperator("NotContains", "hello world", []string{"world"}) {
		t.Error("NotContains should not match when substring present")
	}
}

// --- evaluateOperator: StartsWith/EndsWith ---

func TestOperator_StartsWith(t *testing.T) {
	if !evaluateOperator("StartsWith", "hello world", []string{"hello"}) {
		t.Error("StartsWith should match prefix")
	}
	if !evaluateOperator("StartsWith", "Hello World", []string{"hello"}) {
		t.Error("StartsWith should be case-insensitive")
	}
	if evaluateOperator("StartsWith", "hello", []string{"world"}) {
		t.Error("StartsWith should not match non-prefix")
	}
}

func TestOperator_EndsWith(t *testing.T) {
	if !evaluateOperator("EndsWith", "hello world", []string{"world"}) {
		t.Error("EndsWith should match suffix")
	}
	if !evaluateOperator("EndsWith", "Hello World", []string{"WORLD"}) {
		t.Error("EndsWith should be case-insensitive")
	}
	if evaluateOperator("EndsWith", "hello", []string{"world"}) {
		t.Error("EndsWith should not match non-suffix")
	}
}

// --- evaluateOperator: MatchesRegex ---

func TestOperator_MatchesRegex(t *testing.T) {
	if !evaluateOperator("MatchesRegex", "user-123", []string{`^user-\d+$`}) {
		t.Error("MatchesRegex should match valid pattern")
	}
	// Case-sensitive matching mirrors the evaluation engine (RegexOptions.None).
	if evaluateOperator("MatchesRegex", "USER-123", []string{`^user-\d+$`}) {
		t.Error("MatchesRegex should be case-sensitive (engine parity)")
	}
	// Case-insensitivity is opt-in via the (?i) inline flag.
	if !evaluateOperator("MatchesRegex", "USER-123", []string{`(?i)^user-\d+$`}) {
		t.Error("MatchesRegex should honor the (?i) inline case-insensitive flag")
	}
	if evaluateOperator("MatchesRegex", "admin-123", []string{`^user-\d+$`}) {
		t.Error("MatchesRegex should not match invalid input")
	}
	// Invalid regex should return false
	if evaluateOperator("MatchesRegex", "hello", []string{`[invalid`}) {
		t.Error("MatchesRegex should return false for invalid regex")
	}
}

// --- evaluateOperator: Numeric comparisons ---

func TestOperator_GreaterThan(t *testing.T) {
	if !evaluateOperator("GreaterThan", "10", []string{"5"}) {
		t.Error("10 > 5 should be true")
	}
	if evaluateOperator("GreaterThan", "5", []string{"10"}) {
		t.Error("5 > 10 should be false")
	}
	if evaluateOperator("GreaterThan", "5", []string{"5"}) {
		t.Error("5 > 5 should be false")
	}
}

func TestOperator_LessThan(t *testing.T) {
	if !evaluateOperator("LessThan", "5", []string{"10"}) {
		t.Error("5 < 10 should be true")
	}
	if evaluateOperator("LessThan", "10", []string{"5"}) {
		t.Error("10 < 5 should be false")
	}
}

func TestOperator_GreaterThanOrEqual(t *testing.T) {
	if !evaluateOperator("GreaterThanOrEqual", "10", []string{"5"}) {
		t.Error("10 >= 5 should be true")
	}
	if !evaluateOperator("GreaterThanOrEqual", "5", []string{"5"}) {
		t.Error("5 >= 5 should be true")
	}
	if evaluateOperator("GreaterThanOrEqual", "3", []string{"5"}) {
		t.Error("3 >= 5 should be false")
	}
}

func TestOperator_LessThanOrEqual(t *testing.T) {
	if !evaluateOperator("LessThanOrEqual", "5", []string{"10"}) {
		t.Error("5 <= 10 should be true")
	}
	if !evaluateOperator("LessThanOrEqual", "5", []string{"5"}) {
		t.Error("5 <= 5 should be true")
	}
	if evaluateOperator("LessThanOrEqual", "10", []string{"5"}) {
		t.Error("10 <= 5 should be false")
	}
}

func TestOperator_NumericParseError(t *testing.T) {
	if evaluateOperator("GreaterThan", "abc", []string{"5"}) {
		t.Error("non-numeric value should return false")
	}
	if evaluateOperator("LessThan", "5", []string{"abc"}) {
		t.Error("non-numeric target should return false")
	}
}

func TestOperator_NumericFloat(t *testing.T) {
	if !evaluateOperator("GreaterThan", "10.5", []string{"10.1"}) {
		t.Error("10.5 > 10.1 should be true")
	}
	if !evaluateOperator("LessThan", "3.14", []string{"3.15"}) {
		t.Error("3.14 < 3.15 should be true")
	}
}

// --- evaluateOperator: Before/After ---

func TestOperator_Before(t *testing.T) {
	if !evaluateOperator("Before", "2024-01-01T00:00:00Z", []string{"2024-06-01T00:00:00Z"}) {
		t.Error("Before should match earlier date")
	}
	if evaluateOperator("Before", "2024-06-01T00:00:00Z", []string{"2024-01-01T00:00:00Z"}) {
		t.Error("Before should not match later date")
	}
}

func TestOperator_After(t *testing.T) {
	if !evaluateOperator("After", "2024-06-01T00:00:00Z", []string{"2024-01-01T00:00:00Z"}) {
		t.Error("After should match later date")
	}
	if evaluateOperator("After", "2024-01-01T00:00:00Z", []string{"2024-06-01T00:00:00Z"}) {
		t.Error("After should not match earlier date")
	}
}

func TestOperator_BeforeAfter_EmptyTargets(t *testing.T) {
	if evaluateOperator("Before", "2024-01-01T00:00:00Z", []string{}) {
		t.Error("Before with empty targets should return false")
	}
	if evaluateOperator("After", "2024-01-01T00:00:00Z", []string{}) {
		t.Error("After with empty targets should return false")
	}
}

// --- evaluateOperator: Before/After date-time parity (Issue #1455) ---

// Before/After must parse both operands as real UTC instants (mirroring the
// server engine's CompareDateTime): timezone offsets are honored, no-offset
// strings are assumed UTC, integers fall back to unix seconds, and an
// unparseable value yields NO match (never a lexical string compare).
func TestOperator_BeforeAfter_DateTimeParity(t *testing.T) {
	cases := []struct {
		name     string
		op       string
		value    string
		targets  []string
		expected bool
	}{
		{"offset normalized before", "Before", "2026-01-01T12:00:00+05:00", []string{"2026-01-01T08:00:00Z"}, true},
		{"offset normalized after", "After", "2026-01-01T12:00:00+05:00", []string{"2026-01-01T08:00:00Z"}, false},
		{"unix value after", "After", "1700000000", []string{"2020-01-01T00:00:00Z"}, true},
		{"unix value before", "Before", "1700000000", []string{"2020-01-01T00:00:00Z"}, false},
		{"unparseable value before is no match", "Before", "hello", []string{"world"}, false},
		{"unparseable value after is no match", "After", "hello", []string{"world"}, false},
		{"no offset assumed utc", "Before", "2026-01-01T08:00:00", []string{"2026-01-01T09:00:00Z"}, true},
		{"utc after", "After", "2026-06-01T00:00:00Z", []string{"2026-01-01T00:00:00Z"}, true},
		{"utc before", "Before", "2026-06-01T00:00:00Z", []string{"2026-01-01T00:00:00Z"}, false},
		{"any-of after", "After", "2026-03-01T00:00:00Z", []string{"2030-01-01T00:00:00Z", "2020-01-01T00:00:00Z"}, true},
		{"skip unparseable target", "Before", "2026-01-01T07:30:00Z", []string{"garbage", "2026-01-01T08:00:00Z"}, true},
		{"unix as condition value", "After", "2023-11-15T00:00:00Z", []string{"1700000000"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := evaluateOperator(tc.op, tc.value, tc.targets); got != tc.expected {
				t.Errorf("%s(%q, %v) = %v, want %v", tc.op, tc.value, tc.targets, got, tc.expected)
			}
		})
	}
}

// --- evaluateOperator: multi-value relational (Issue #1443) ---

// Numeric/date operators must match if the value satisfies the comparison
// against ANY supplied condition value (mirroring the server engine), not just
// targets[0].

func TestOperator_GreaterThan_MatchesAnyValue(t *testing.T) {
	// any(15 > 20, 15 > 10) -> true; targets[0]-only would be false
	if !evaluateOperator("GreaterThan", "15", []string{"20", "10"}) {
		t.Error("GreaterThan should match when a non-first value is satisfied")
	}
	// below every value -> false
	if evaluateOperator("GreaterThan", "5", []string{"20", "10"}) {
		t.Error("GreaterThan should not match when no value is satisfied")
	}
}

func TestOperator_Before_MatchesAnyValue(t *testing.T) {
	// any(2025 < 2020, 2025 < 2030) -> true; targets[0]-only would be false
	if !evaluateOperator("Before", "2025-06-15T00:00:00Z",
		[]string{"2020-01-01T00:00:00Z", "2030-01-01T00:00:00Z"}) {
		t.Error("Before should match when a non-first value is satisfied")
	}
}

func TestOperator_After_MatchesAnyValue(t *testing.T) {
	// any(2025 > 2030, 2025 > 2020) -> true; targets[0]-only would be false
	if !evaluateOperator("After", "2025-06-15T00:00:00Z",
		[]string{"2030-01-01T00:00:00Z", "2020-01-01T00:00:00Z"}) {
		t.Error("After should match when a non-first value is satisfied")
	}
}

func TestOperator_Numeric_EmptyTargets(t *testing.T) {
	if evaluateOperator("GreaterThan", "10", []string{}) {
		t.Error("GreaterThan with empty targets should return false")
	}
	if evaluateOperator("LessThan", "10", []string{}) {
		t.Error("LessThan with empty targets should return false")
	}
}

// --- evaluateOperator: Semver comparisons (Issue #1431) ---

// Semver* operators compare the attribute value against each condition value as
// a semantic version (https://semver.org) rather than a decimal, mirroring the
// server engine's SemverComparer and the JS SDK reference evaluator.

func TestOperator_SemverEquals(t *testing.T) {
	if !evaluateOperator("SemverEquals", "1.2.3", []string{"1.2.3"}) {
		t.Error("1.2.3 == 1.2.3 should be true")
	}
	// Missing trailing segments compare as 0, so 2.0 == 2.0.0.
	if !evaluateOperator("SemverEquals", "2.0", []string{"2.0.0"}) {
		t.Error("2.0 == 2.0.0 should be true")
	}
	// Optional leading v/V is stripped.
	if !evaluateOperator("SemverEquals", "v1.2.3", []string{"1.2.3"}) {
		t.Error("v1.2.3 == 1.2.3 should be true")
	}
	// Build metadata is ignored for precedence.
	if !evaluateOperator("SemverEquals", "1.0.0+build.5", []string{"1.0.0"}) {
		t.Error("1.0.0+build.5 == 1.0.0 should be true")
	}
	if evaluateOperator("SemverEquals", "1.2.3", []string{"1.2.4"}) {
		t.Error("1.2.3 == 1.2.4 should be false")
	}
}

func TestOperator_SemverGreaterThan(t *testing.T) {
	// Multi-segment regression: as decimals 2.10 < 2.9, but as semver 2.10 > 2.9.
	if !evaluateOperator("SemverGreaterThan", "2.10", []string{"2.9"}) {
		t.Error("2.10 > 2.9 should be true")
	}
	if evaluateOperator("SemverGreaterThan", "2.9", []string{"2.10"}) {
		t.Error("2.9 > 2.10 should be false")
	}
	// A release ranks above its prerelease.
	if !evaluateOperator("SemverGreaterThan", "1.0.0", []string{"1.0.0-alpha"}) {
		t.Error("1.0.0 > 1.0.0-alpha should be true")
	}
	if evaluateOperator("SemverGreaterThan", "1.2.3", []string{"1.2.3"}) {
		t.Error("1.2.3 > 1.2.3 should be false")
	}
}

func TestOperator_SemverGreaterThanOrEqual(t *testing.T) {
	// Key regression from #1431: the decimal path silently returned false here.
	if !evaluateOperator("SemverGreaterThanOrEqual", "2.10.1", []string{"2.0"}) {
		t.Error("2.10.1 >= 2.0 should be true")
	}
	if !evaluateOperator("SemverGreaterThanOrEqual", "1.2.3", []string{"1.2.3"}) {
		t.Error("1.2.3 >= 1.2.3 should be true")
	}
	if evaluateOperator("SemverGreaterThanOrEqual", "1.2.3", []string{"2.0.0"}) {
		t.Error("1.2.3 >= 2.0.0 should be false")
	}
}

func TestOperator_SemverLessThan(t *testing.T) {
	if !evaluateOperator("SemverLessThan", "2.9", []string{"2.10"}) {
		t.Error("2.9 < 2.10 should be true")
	}
	// A prerelease ranks below its release.
	if !evaluateOperator("SemverLessThan", "1.0.0-alpha", []string{"1.0.0"}) {
		t.Error("1.0.0-alpha < 1.0.0 should be true")
	}
	if evaluateOperator("SemverLessThan", "2.10", []string{"2.9"}) {
		t.Error("2.10 < 2.9 should be false")
	}
}

func TestOperator_SemverLessThanOrEqual(t *testing.T) {
	if !evaluateOperator("SemverLessThanOrEqual", "1.2.3", []string{"2.0.0"}) {
		t.Error("1.2.3 <= 2.0.0 should be true")
	}
	if !evaluateOperator("SemverLessThanOrEqual", "1.2.3", []string{"1.2.3"}) {
		t.Error("1.2.3 <= 1.2.3 should be true")
	}
	if evaluateOperator("SemverLessThanOrEqual", "2.0.0", []string{"1.2.3"}) {
		t.Error("2.0.0 <= 1.2.3 should be false")
	}
}

func TestOperator_Semver_PrereleasePrecedence(t *testing.T) {
	// Numeric identifiers rank below alphanumeric ones (semver §11).
	if !evaluateOperator("SemverLessThan", "1.0.0-1", []string{"1.0.0-alpha"}) {
		t.Error("1.0.0-1 < 1.0.0-alpha should be true (numeric < alphanumeric)")
	}
	// alpha < beta lexically.
	if !evaluateOperator("SemverLessThan", "1.0.0-alpha", []string{"1.0.0-beta"}) {
		t.Error("1.0.0-alpha < 1.0.0-beta should be true")
	}
	// When all shared identifiers are equal, the longer prerelease wins.
	if !evaluateOperator("SemverLessThan", "1.0.0-alpha", []string{"1.0.0-alpha.1"}) {
		t.Error("1.0.0-alpha < 1.0.0-alpha.1 should be true")
	}
	// Numeric prerelease identifiers compare numerically, not lexically.
	if !evaluateOperator("SemverLessThan", "1.0.0-2", []string{"1.0.0-10"}) {
		t.Error("1.0.0-2 < 1.0.0-10 should be true (numeric prerelease)")
	}
}

func TestOperator_Semver_MixedCasePrereleaseAsciiOrder(t *testing.T) {
	// Semver §11: alphanumeric prerelease identifiers compare in ASCII sort order,
	// which is case-sensitive — A–Z (65–90) sort before a–z (97–122). A case-folding
	// comparer disagrees with the spec-correct JS/Python evaluators (#1447).

	// 'B'(66) < 'a'(97): "Beta" < "alpha". Case-folding would order "beta" > "alpha".
	if !evaluateOperator("SemverLessThan", "1.0.0-Beta", []string{"1.0.0-alpha"}) {
		t.Error("1.0.0-Beta < 1.0.0-alpha should be true (ASCII order, B < a)")
	}
	// "1.0.0-RC" and "1.0.0-rc" are distinct identifiers — a case-folding comparer
	// would treat them as equal.
	if evaluateOperator("SemverEquals", "1.0.0-RC", []string{"1.0.0-rc"}) {
		t.Error("1.0.0-RC should not equal 1.0.0-rc (ASCII order is case-sensitive)")
	}
	// 'R'(82) < 'r'(114): "RC" < "rc".
	if !evaluateOperator("SemverLessThan", "1.0.0-RC", []string{"1.0.0-rc"}) {
		t.Error("1.0.0-RC < 1.0.0-rc should be true (ASCII order, R < r)")
	}
}

func TestOperator_Semver_Unparseable(t *testing.T) {
	// An unparseable value matches nothing.
	if evaluateOperator("SemverGreaterThan", "not-a-version", []string{"1.0.0"}) {
		t.Error("unparseable value should return false")
	}
	// An unparseable target contributes no match (it is skipped).
	if evaluateOperator("SemverGreaterThan", "1.0.0", []string{"not-a-version"}) {
		t.Error("unparseable target should return false")
	}
	// A non-numeric release segment is not a version.
	if evaluateOperator("SemverEquals", "1.x.0", []string{"1.0.0"}) {
		t.Error("non-numeric release segment should return false")
	}
}

func TestOperator_Semver_EmptyTargets(t *testing.T) {
	if evaluateOperator("SemverEquals", "1.0.0", []string{}) {
		t.Error("SemverEquals with empty targets should return false")
	}
	if evaluateOperator("SemverGreaterThan", "1.0.0", []string{}) {
		t.Error("SemverGreaterThan with empty targets should return false")
	}
}

func TestOperator_Semver_MatchesAnyValue(t *testing.T) {
	// any(2.0.0 > 3.0.0, 2.0.0 > 1.0.0) -> true; targets[0]-only would be false.
	if !evaluateOperator("SemverGreaterThan", "2.0.0", []string{"3.0.0", "1.0.0"}) {
		t.Error("SemverGreaterThan should match when a non-first value is satisfied")
	}
	// Satisfies none of the supplied versions -> false.
	if evaluateOperator("SemverGreaterThan", "0.5.0", []string{"3.0.0", "1.0.0"}) {
		t.Error("SemverGreaterThan should not match when no value is satisfied")
	}
}

// --- evaluateOperator: Unknown ---

func TestOperator_Unknown(t *testing.T) {
	if evaluateOperator("FakeOperator", "value", []string{"target"}) {
		t.Error("Unknown operator should return false")
	}
}

// --- evaluateCondition ---

func TestEvaluateCondition_Match(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "US"},
	}
	c := condition{
		Attribute: "country",
		Operator:  "Equals",
		Values:    []string{"US"},
		Negate:    false,
	}
	if !evaluateCondition(c, ctx) {
		t.Error("condition should match")
	}
}

func TestEvaluateCondition_NoMatch(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "FR"},
	}
	c := condition{
		Attribute: "country",
		Operator:  "Equals",
		Values:    []string{"US"},
		Negate:    false,
	}
	if evaluateCondition(c, ctx) {
		t.Error("condition should not match")
	}
}

func TestEvaluateCondition_Negate(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "US"},
	}
	c := condition{
		Attribute: "country",
		Operator:  "Equals",
		Values:    []string{"US"},
		Negate:    true,
	}
	if evaluateCondition(c, ctx) {
		t.Error("negated match should return false")
	}
}

func TestEvaluateCondition_Negate_NoMatch(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "FR"},
	}
	c := condition{
		Attribute: "country",
		Operator:  "Equals",
		Values:    []string{"US"},
		Negate:    true,
	}
	if !evaluateCondition(c, ctx) {
		t.Error("negated non-match should return true")
	}
}

func TestEvaluateCondition_MissingAttribute_NoNegate(t *testing.T) {
	ctx := EvaluationContext{}
	c := condition{
		Attribute: "country",
		Operator:  "Equals",
		Values:    []string{"US"},
		Negate:    false,
	}
	if evaluateCondition(c, ctx) {
		t.Error("missing attribute without negate should return false")
	}
}

func TestEvaluateCondition_MissingAttribute_WithNegate(t *testing.T) {
	ctx := EvaluationContext{}
	c := condition{
		Attribute: "country",
		Operator:  "Equals",
		Values:    []string{"US"},
		Negate:    true,
	}
	if !evaluateCondition(c, ctx) {
		t.Error("missing attribute with negate should return true")
	}
}

// A present-but-nil attribute must be treated identically to an absent one:
// short-circuit to condition.Negate, never stringify the nil to "<nil>" and
// run the operator against it. The engine + JS/Python/Java/C#/PHP all return
// Negate here; Go was the lone holdout (#1484). This is the issue's exact
// reproduction: Contains "nil" matched "<nil>" and returned true.
func TestEvaluateCondition_PresentButNilAttribute_NoNegate(t *testing.T) {
	ctx := EvaluationContext{Attributes: map[string]any{"plan": nil}}
	c := condition{
		Attribute: "plan",
		Operator:  "Contains",
		Values:    []string{"nil"},
		Negate:    false,
	}
	if evaluateCondition(c, ctx) {
		t.Error("present-but-nil attribute without negate should return false")
	}
}

func TestEvaluateCondition_PresentButNilAttribute_WithNegate(t *testing.T) {
	ctx := EvaluationContext{Attributes: map[string]any{"plan": nil}}
	c := condition{
		Attribute: "plan",
		Operator:  "Contains",
		Values:    []string{"nil"},
		Negate:    true,
	}
	if !evaluateCondition(c, ctx) {
		t.Error("present-but-nil attribute with negate should return true")
	}
}

func TestEvaluateCondition_UserID(t *testing.T) {
	ctx := EvaluationContext{UserID: "user-42"}
	c := condition{
		Attribute: "user_id",
		Operator:  "Equals",
		Values:    []string{"user-42"},
		Negate:    false,
	}
	if !evaluateCondition(c, ctx) {
		t.Error("user_id condition should match")
	}
}

// --- evaluateCondition: type-aware numeric Equals coercion (Issue #1458) ---

// When the raw attribute value is numeric (Go decodes JSON numbers as float64,
// but user-supplied contexts may carry int/int64/float32), the equality
// operators (Equals/In/NotEquals/NotIn) must compare numerically against each
// condition value parsed strictly as a float — mirroring the engine, so that
// e.g. a float64(1.0) attribute matches the literal "1.0" AND "1" rather than
// disagreeing on stringification ("1" vs "1.0"). bool/string/nil attributes
// keep the existing case-insensitive string path; non-equality operators
// (Contains/StartsWith/...) are never coerced.
func TestEvaluateCondition_NumericEqualsCoercion(t *testing.T) {
	cases := []struct {
		name     string
		attr     any
		operator string
		values   []string
		negate   bool
		expected bool
	}{
		{"float64 1.0 equals 1.0", float64(1.0), "Equals", []string{"1.0"}, false, true},
		{"float64 1.0 equals 1", float64(1.0), "Equals", []string{"1"}, false, true},
		{"int 1 equals 1.0", int(1), "Equals", []string{"1.0"}, false, true},
		{"int 1 equals 1", int(1), "Equals", []string{"1"}, false, true},
		{"float64 1.5 equals 1.5", float64(1.5), "Equals", []string{"1.5"}, false, true},
		{"float64 1.5 equals 1 no match", float64(1.5), "Equals", []string{"1"}, false, false},
		{"float64 2.0 in [1, 2.0]", float64(2.0), "In", []string{"1", "2.0"}, false, true},
		{"float64 3.0 in [1, 2] no match", float64(3.0), "In", []string{"1", "2"}, false, false},
		{"float64 1.0 notequals 1.0 no match", float64(1.0), "NotEquals", []string{"1.0"}, false, false},
		{"float64 1.0 notequals 2 match", float64(1.0), "NotEquals", []string{"2"}, false, true},
		{"float64 3.0 notin [1, 2] match", float64(3.0), "NotIn", []string{"1", "2"}, false, true},
		{"float64 1.0 equals abc no match", float64(1.0), "Equals", []string{"abc"}, false, false},
		{"float64 1.0 equals 1abc strict no match", float64(1.0), "Equals", []string{"1abc"}, false, false},
		{"bool true equals 1 no match (bool not numeric)", true, "Equals", []string{"1"}, false, false},
		{"bool true equals true string path match", true, "Equals", []string{"true"}, false, true},
		{"string 1.0 equals 1 no match", "1.0", "Equals", []string{"1"}, false, false},
		{"string 01234 equals 1234 no match", "01234", "Equals", []string{"1234"}, false, false},
		{"float64 1.0 equals 2 negate match", float64(1.0), "Equals", []string{"2"}, true, true},
		// json.Number (from a UseNumber()-configured decoder) is treated as numeric (#1458)
		{"json.Number 1.0 equals 1", json.Number("1.0"), "Equals", []string{"1"}, false, true},
		{"json.Number 2 in [1, 2.0]", json.Number("2"), "In", []string{"1", "2.0"}, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := EvaluationContext{
				Attributes: map[string]any{"attr": tc.attr},
			}
			c := condition{
				Attribute: "attr",
				Operator:  tc.operator,
				Values:    tc.values,
				Negate:    tc.negate,
			}
			if got := evaluateCondition(c, ctx); got != tc.expected {
				t.Errorf("evaluateCondition(attr=%#v, %s, %v, negate=%v) = %v, want %v",
					tc.attr, tc.operator, tc.values, tc.negate, got, tc.expected)
			}
		})
	}
}

// --- evaluateConditions ---

func TestEvaluateConditions_Empty(t *testing.T) {
	ctx := EvaluationContext{}
	if !evaluateConditions(nil, "And", ctx) {
		t.Error("empty conditions should return true")
	}
	if !evaluateConditions([]condition{}, "Or", ctx) {
		t.Error("empty conditions should return true")
	}
}

func TestEvaluateConditions_And_AllMatch(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "US", "plan": "pro"},
	}
	conds := []condition{
		{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
		{Attribute: "plan", Operator: "Equals", Values: []string{"pro"}},
	}
	if !evaluateConditions(conds, "And", ctx) {
		t.Error("And with all matching should return true")
	}
}

func TestEvaluateConditions_And_OneFails(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "US", "plan": "free"},
	}
	conds := []condition{
		{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
		{Attribute: "plan", Operator: "Equals", Values: []string{"pro"}},
	}
	if evaluateConditions(conds, "And", ctx) {
		t.Error("And with one failing should return false")
	}
}

func TestEvaluateConditions_Or_OneMatches(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "FR"},
	}
	conds := []condition{
		{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
		{Attribute: "country", Operator: "Equals", Values: []string{"FR"}},
	}
	if !evaluateConditions(conds, "Or", ctx) {
		t.Error("Or with one matching should return true")
	}
}

func TestEvaluateConditions_Or_NoneMatch(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "DE"},
	}
	conds := []condition{
		{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
		{Attribute: "country", Operator: "Equals", Values: []string{"FR"}},
	}
	if evaluateConditions(conds, "Or", ctx) {
		t.Error("Or with none matching should return false")
	}
}

func TestEvaluateConditions_DefaultLogicIsAnd(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "US", "plan": "free"},
	}
	conds := []condition{
		{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
		{Attribute: "plan", Operator: "Equals", Values: []string{"pro"}},
	}
	// Empty logic string defaults to And
	if evaluateConditions(conds, "", ctx) {
		t.Error("default (And) logic with one failing should return false")
	}
}

func TestEvaluateConditions_Or_CaseInsensitive(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "US"},
	}
	conds := []condition{
		{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
	}
	if !evaluateConditions(conds, "or", ctx) {
		t.Error("logic 'or' (lowercase) should work")
	}
}

// --- evaluateConditionGroups ---

func TestEvaluateConditionGroups_Empty(t *testing.T) {
	ctx := EvaluationContext{}
	if !evaluateConditionGroups(nil, ctx) {
		t.Error("empty groups should return true")
	}
	if !evaluateConditionGroups([]conditionGroup{}, ctx) {
		t.Error("empty groups should return true")
	}
}

func TestEvaluateConditionGroups_SingleGroup(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "US"},
	}
	groups := []conditionGroup{
		{
			Operator: "And",
			Conditions: []condition{
				{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
			},
		},
	}
	if !evaluateConditionGroups(groups, ctx) {
		t.Error("single matching group should return true")
	}
}

func TestEvaluateConditionGroups_SingleGroup_NoMatch(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "FR"},
	}
	groups := []conditionGroup{
		{
			Operator: "And",
			Conditions: []condition{
				{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
			},
		},
	}
	if evaluateConditionGroups(groups, ctx) {
		t.Error("single non-matching group should return false")
	}
}

func TestEvaluateConditionGroups_MultipleGroups_AllMatch(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "US", "plan": "pro"},
	}
	groups := []conditionGroup{
		{
			Operator: "And",
			Conditions: []condition{
				{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
			},
		},
		{
			Operator: "And",
			Conditions: []condition{
				{Attribute: "plan", Operator: "Equals", Values: []string{"pro"}},
			},
		},
	}
	if !evaluateConditionGroups(groups, ctx) {
		t.Error("all groups matching should return true (AND between groups)")
	}
}

func TestEvaluateConditionGroups_MultipleGroups_OneFailsAND(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "US", "plan": "free"},
	}
	groups := []conditionGroup{
		{
			Operator: "And",
			Conditions: []condition{
				{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
			},
		},
		{
			Operator: "And",
			Conditions: []condition{
				{Attribute: "plan", Operator: "Equals", Values: []string{"pro"}},
			},
		},
	}
	if evaluateConditionGroups(groups, ctx) {
		t.Error("one failing group should return false (AND between groups)")
	}
}

func TestEvaluateConditionGroups_OrWithinGroup(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "FR"},
	}
	groups := []conditionGroup{
		{
			Operator: "Or",
			Conditions: []condition{
				{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
				{Attribute: "country", Operator: "Equals", Values: []string{"FR"}},
			},
		},
	}
	if !evaluateConditionGroups(groups, ctx) {
		t.Error("Or group with one matching condition should return true")
	}
}

func TestEvaluateConditionGroups_MixedOperators(t *testing.T) {
	ctx := EvaluationContext{
		Attributes: map[string]any{"country": "FR", "plan": "pro", "role": "admin"},
	}
	groups := []conditionGroup{
		{
			Operator: "Or",
			Conditions: []condition{
				{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
				{Attribute: "country", Operator: "Equals", Values: []string{"FR"}},
			},
		},
		{
			Operator: "And",
			Conditions: []condition{
				{Attribute: "plan", Operator: "Equals", Values: []string{"pro"}},
				{Attribute: "role", Operator: "Equals", Values: []string{"admin"}},
			},
		},
	}
	if !evaluateConditionGroups(groups, ctx) {
		t.Error("both groups match so result should be true")
	}
}
