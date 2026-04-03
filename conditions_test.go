package featureflip

import "testing"

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

func TestGetAttributeValue_UserID_CaseInsensitive(t *testing.T) {
	ctx := EvaluationContext{UserID: "user-42"}
	val, ok := getAttributeValue(ctx, "User_ID")
	if !ok {
		t.Fatal("expected ok=true for User_ID")
	}
	if val != "user-42" {
		t.Errorf("got %q, want %q", val, "user-42")
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
	if !evaluateOperator("MatchesRegex", "USER-123", []string{`^user-\d+$`}) {
		t.Error("MatchesRegex should be case-insensitive")
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
