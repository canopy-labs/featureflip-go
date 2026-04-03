package featureflip

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// getAttributeValue resolves an attribute from the evaluation context.
// The special attributes "userId" and "user_id" map to ctx.UserID.
// All other attributes are looked up in ctx.Attributes.
func getAttributeValue(ctx EvaluationContext, attr string) (string, bool) {
	lower := strings.ToLower(attr)
	if lower == "userid" || lower == "user_id" {
		if ctx.UserID == "" {
			return "", false
		}
		return ctx.UserID, true
	}

	if ctx.Attributes == nil {
		return "", false
	}

	v, ok := ctx.Attributes[attr]
	if !ok {
		return "", false
	}

	return fmt.Sprintf("%v", v), true
}

// evaluateConditionGroups evaluates condition groups for a rule.
// All groups must match (AND). Within each group, conditions use the group's operator.
// Empty groups return true.
func evaluateConditionGroups(groups []conditionGroup, ctx EvaluationContext) bool {
	if len(groups) == 0 {
		return true
	}

	for _, group := range groups {
		if !evaluateConditions(group.Conditions, group.Operator, ctx) {
			return false
		}
	}
	return true
}

// evaluateConditions evaluates a list of conditions using And/Or logic.
// Empty conditions return true.
func evaluateConditions(conds []condition, logic string, ctx EvaluationContext) bool {
	if len(conds) == 0 {
		return true
	}

	if strings.ToLower(logic) == "or" {
		for _, c := range conds {
			if evaluateCondition(c, ctx) {
				return true
			}
		}
		return false
	}

	// Default: And
	for _, c := range conds {
		if !evaluateCondition(c, ctx) {
			return false
		}
	}
	return true
}

// evaluateCondition evaluates a single condition against the context.
// If the attribute is missing from the context, returns c.Negate.
func evaluateCondition(c condition, ctx EvaluationContext) bool {
	value, ok := getAttributeValue(ctx, c.Attribute)
	if !ok {
		return c.Negate
	}

	result := evaluateOperator(c.Operator, value, c.Values)

	if c.Negate {
		return !result
	}
	return result
}

// evaluateOperator evaluates a single operator against a value and targets.
// All string comparisons are case-insensitive.
func evaluateOperator(op string, value string, targets []string) bool {
	lower := strings.ToLower(value)

	switch strings.ToLower(op) {
	case "equals", "in":
		for _, t := range targets {
			if strings.ToLower(t) == lower {
				return true
			}
		}
		return false

	case "notequals", "notin":
		for _, t := range targets {
			if strings.ToLower(t) == lower {
				return false
			}
		}
		return true

	case "contains":
		for _, t := range targets {
			if strings.Contains(lower, strings.ToLower(t)) {
				return true
			}
		}
		return false

	case "notcontains":
		for _, t := range targets {
			if strings.Contains(lower, strings.ToLower(t)) {
				return false
			}
		}
		return true

	case "startswith":
		for _, t := range targets {
			if strings.HasPrefix(lower, strings.ToLower(t)) {
				return true
			}
		}
		return false

	case "endswith":
		for _, t := range targets {
			if strings.HasSuffix(lower, strings.ToLower(t)) {
				return true
			}
		}
		return false

	case "matchesregex":
		for _, t := range targets {
			// Add case-insensitive flag
			pattern := "(?i)" + t
			matched, err := regexp.MatchString(pattern, value)
			if err == nil && matched {
				return true
			}
		}
		return false

	case "greaterthan":
		return compareNumeric(value, targets, func(a, b float64) bool { return a > b })

	case "lessthan":
		return compareNumeric(value, targets, func(a, b float64) bool { return a < b })

	case "greaterthanorequal":
		return compareNumeric(value, targets, func(a, b float64) bool { return a >= b })

	case "lessthanorequal":
		return compareNumeric(value, targets, func(a, b float64) bool { return a <= b })

	case "before":
		// ISO 8601 string comparison (alphabetical works for ISO dates)
		if len(targets) == 0 {
			return false
		}
		return value < targets[0]

	case "after":
		if len(targets) == 0 {
			return false
		}
		return value > targets[0]

	default:
		return false
	}
}

// compareNumeric parses value and the first target as float64 and applies cmp.
// Returns false if either fails to parse.
func compareNumeric(value string, targets []string, cmp func(a, b float64) bool) bool {
	if len(targets) == 0 {
		return false
	}
	a, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return false
	}
	b, err := strconv.ParseFloat(targets[0], 64)
	if err != nil {
		return false
	}
	return cmp(a, b)
}
