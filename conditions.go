package featureflip

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// getAttributeValue resolves an attribute from the evaluation context and
// stringifies it. It delegates resolution to getRawAttributeValue so the
// userId/user_id alias and lookup logic live in exactly one place.
func getAttributeValue(ctx EvaluationContext, attr string) (string, bool) {
	v, ok := getRawAttributeValue(ctx, attr)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%v", v), true
}

// getRawAttributeValue resolves an attribute from the evaluation context WITHOUT
// stringifying it, preserving the runtime type so the equality operators can
// branch on numeric values (Issue #1458). The special attributes "userId" and
// "user_id" map to ctx.UserID; all others are looked up in ctx.Attributes.
// The alias is matched case-sensitively ("userId"/"user_id" only, not "USERID"
// or "UserId") to mirror the engine's EvaluationContext.GetAttribute and the
// other SDKs — a single documented aliasing rule across engine + SDKs (#1460).
func getRawAttributeValue(ctx EvaluationContext, attr string) (any, bool) {
	if attr == "userId" || attr == "user_id" {
		if ctx.UserID == "" {
			return nil, false
		}
		return ctx.UserID, true
	}

	if ctx.Attributes == nil {
		return nil, false
	}

	// A present-but-nil value is "absent", not "<nil>": report ok=false so the
	// caller short-circuits to condition.Negate instead of stringifying the nil
	// and running the operator against the literal "<nil>". Mirrors the engine
	// (FlagEvaluator) + JS/Python/Java/C#/PHP per the cross-SDK contract (#1484,
	// #1460). attrAsFloat(nil) already returns (0, false), so only the string
	// path was affected.
	v, ok := ctx.Attributes[attr]
	if v == nil {
		return nil, false
	}
	return v, ok
}

// attrAsFloat reports whether v is a numeric (non-bool) value and returns it as
// a float64. Go's encoding/json decodes JSON numbers as float64 by default, or
// json.Number when the decoder uses UseNumber(); a context built from user Go
// code may carry int/int64/float32. bool is deliberately excluded — it is not
// numeric — as are string and nil.
func attrAsFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		// bool, string, nil, and any other type are not numeric.
		return 0, false
	}
}

// evaluateNumericEquality compares a numeric attribute value against each target
// parsed strictly as a float (mirroring the engine's CompareNumeric / the
// relational compareNumeric helper). The equals/in arms match if ANY target
// parses and equals the value; notequals/notin are their negation. Targets that
// don't parse strictly (e.g. "1abc", "abc") are skipped — they never match.
func evaluateNumericEquality(op string, attrFloat float64, targets []string, negate bool) bool {
	anyEqual := false
	for _, t := range targets {
		n, err := strconv.ParseFloat(t, 64)
		if err != nil {
			continue
		}
		if n == attrFloat {
			anyEqual = true
			break
		}
	}

	var result bool
	switch op {
	case "equals", "in":
		result = anyEqual
	default: // "notequals", "notin"
		result = !anyEqual
	}

	if negate {
		return !result
	}
	return result
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
	// Resolve the attribute once (a single map lookup + alias check), then either
	// take the numeric path or stringify the same value for the string path.
	raw, ok := getRawAttributeValue(ctx, c.Attribute)
	if !ok {
		return c.Negate
	}

	// Type-aware numeric coercion for the equality operators (Issue #1458):
	// when the raw attribute value is numeric, compare it numerically against
	// the condition values rather than relying on stringification, which
	// disagrees between the engine and SDK (e.g. float64(1.0) → "1" in Go but
	// "1.0" in the engine). Only equals/in/notequals/notin are coerced; all
	// other operators (contains/startswith/relational/...) keep the string path.
	op := strings.ToLower(c.Operator)
	switch op {
	case "equals", "in", "notequals", "notin":
		if attrFloat, isNum := attrAsFloat(raw); isNum {
			return evaluateNumericEquality(op, attrFloat, c.Values, c.Negate)
		}
	}

	result, recognised := evaluateOperatorChecked(c.Operator, fmt.Sprintf("%v", raw), c.Values)

	// Issue #2262: an unrecognised operator fails CLOSED. Letting Negate invert
	// it would turn "I cannot evaluate this" into "matches every user", rolling
	// the flag out to 100% of traffic. The realistic trigger is a new operator
	// shipped server-side reaching an SDK pinned to an older version.
	if !recognised {
		return false
	}

	if c.Negate {
		return !result
	}
	return result
}

// evaluateOperatorChecked evaluates a single operator against a value and
// targets. All string comparisons are case-insensitive.
//
// The second return value reports whether the operator was recognised. It is
// false — distinct from a (false, true) non-match — for an operator this SDK
// does not know, which callers must fail closed on rather than invert.
func evaluateOperatorChecked(op string, value string, targets []string) (bool, bool) {
	lower := strings.ToLower(value)

	switch strings.ToLower(op) {
	case "equals", "in":
		for _, t := range targets {
			if strings.ToLower(t) == lower {
				return true, true
			}
		}
		return false, true

	case "notequals", "notin":
		for _, t := range targets {
			if strings.ToLower(t) == lower {
				return false, true
			}
		}
		return true, true

	case "contains":
		for _, t := range targets {
			if strings.Contains(lower, strings.ToLower(t)) {
				return true, true
			}
		}
		return false, true

	case "notcontains":
		for _, t := range targets {
			if strings.Contains(lower, strings.ToLower(t)) {
				return false, true
			}
		}
		return true, true

	case "startswith":
		for _, t := range targets {
			if strings.HasPrefix(lower, strings.ToLower(t)) {
				return true, true
			}
		}
		return false, true

	case "endswith":
		for _, t := range targets {
			if strings.HasSuffix(lower, strings.ToLower(t)) {
				return true, true
			}
		}
		return false, true

	case "matchesregex":
		for _, t := range targets {
			// Case-sensitive matching mirrors the evaluation engine
			// (RegexOptions.None). Case-insensitivity is opt-in via the
			// (?i) inline flag in the pattern itself.
			//
			// No ReDoS timeout is needed here (unlike the engine's 100ms guard
			// and the other SDKs — #1460): Go's regexp uses the RE2 engine,
			// which guarantees linear-time matching and cannot catastrophically
			// backtrack. An invalid pattern returns err != nil → no match.
			matched, err := regexp.MatchString(t, value)
			if err == nil && matched {
				return true, true
			}
		}
		return false, true

	case "greaterthan":
		return compareNumeric(value, targets, func(a, b float64) bool { return a > b }), true

	case "lessthan":
		return compareNumeric(value, targets, func(a, b float64) bool { return a < b }), true

	case "greaterthanorequal":
		return compareNumeric(value, targets, func(a, b float64) bool { return a >= b }), true

	case "lessthanorequal":
		return compareNumeric(value, targets, func(a, b float64) bool { return a <= b }), true

	case "before":
		return compareDateTime(value, targets, func(a, b time.Time) bool { return a.Before(b) }), true

	case "after":
		return compareDateTime(value, targets, func(a, b time.Time) bool { return a.After(b) }), true

	case "semverequals":
		return compareSemver(value, targets, func(c int) bool { return c == 0 }), true

	case "semvergreaterthan":
		return compareSemver(value, targets, func(c int) bool { return c > 0 }), true

	case "semvergreaterthanorequal":
		return compareSemver(value, targets, func(c int) bool { return c >= 0 }), true

	case "semverlessthan":
		return compareSemver(value, targets, func(c int) bool { return c < 0 }), true

	case "semverlessthanorequal":
		return compareSemver(value, targets, func(c int) bool { return c <= 0 }), true

	default:
		// Unrecognised operator — "cannot evaluate", not "did not match".
		return false, false
	}
}

// evaluateOperator reports whether value satisfies op against targets, treating
// an unrecognised operator as a non-match. Callers that must distinguish
// "did not match" from "cannot evaluate" — anything applying Negate — have to
// use evaluateOperatorChecked instead (see #2262).
func evaluateOperator(op string, value string, targets []string) bool {
	result, _ := evaluateOperatorChecked(op, value, targets)
	return result
}

// compareNumeric parses value as float64 and applies cmp against each target,
// returning true if value satisfies the comparison for ANY target (mirroring
// the server engine + C#/Java SDKs). Returns false if value is non-numeric,
// targets is empty, or no parseable target satisfies cmp. Non-numeric targets
// are skipped.
func compareNumeric(value string, targets []string, cmp func(a, b float64) bool) bool {
	a, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return false
	}
	for _, t := range targets {
		b, err := strconv.ParseFloat(t, 64)
		if err != nil {
			continue
		}
		if cmp(a, b) {
			return true
		}
	}
	return false
}

// compareDateTime parses value as an absolute UTC instant and applies cmp
// against each target, returning true if value satisfies the comparison for ANY
// target (mirroring the server engine's CompareDateTime + C#/Java SDKs). Returns
// false if value is unparseable, targets is empty, or no parseable target
// satisfies cmp. Unparseable targets are skipped. There is NO lexical/string
// fallback: an unparseable value never matches.
func compareDateTime(value string, targets []string, cmp func(a, b time.Time) bool) bool {
	a, ok := parseDateTime(value)
	if !ok {
		return false
	}
	for _, t := range targets {
		b, ok := parseDateTime(t)
		if !ok {
			continue
		}
		if cmp(a, b) {
			return true
		}
	}
	return false
}

// parseDateTime parses s as an absolute UTC instant, mirroring the engine's
// TryParseDateTime. It accepts ISO-8601/RFC3339 date-times (offset or "Z"
// honored), offset-less date-times and plain dates (assumed UTC), and finally
// an integer Unix timestamp in seconds since epoch. Successful parses are
// normalized to UTC. ok is false when none of these forms apply.
// DateTimeOffset.MinValue / MaxValue as unix seconds — the exact bounds the engine's
// FromUnixTimeSeconds accepts before throwing (#2432).
const (
	minUnixSeconds = -62135596800
	maxUnixSeconds = 253402300799
)

// operandWhitespace is the ONLY set trimmed from a date operand, and the whole of the
// operand's permitted whitespace: U+0009..U+000D plus U+0020 -- exactly the class the
// engine's NumberStyles.Integer accepts via AllowLeadingWhite | AllowTrailingWhite.
//
// strings.TrimSpace is deliberately NOT used: it trims by unicode.IsSpace, which also
// strips U+0085, U+00A0 and every Unicode space separator, so an operand prefixed with
// any of those was trimmed to "5" and matched here while the engine rejected it (#2468).
const operandWhitespace = "\t\n\v\f\r "

// isoOperand is the ISO-8601 grammar a date operand may use: a calendar date, optionally
// followed by a time (seconds and fractional seconds optional) and an optional offset in
// either extended (+05:00 / Z) or basic (+0500) form. The separator may be "T" or a
// space, both of which the engine accepts.
var isoOperand = regexp.MustCompile(
	`^(\d{4}-\d{2}-\d{2})(?:[T ](\d{2}):(\d{2})(?::(\d{2}))?(\.\d+)?(Z|[+-]\d{2}:?\d{2})?)?$`)

// hasForbiddenOperandChar reports whether s carries a character no date operand may
// contain: a NUL or other control character, or a non-ASCII whitespace/format character.
// An interior ASCII space is allowed -- it is the ISO-8601 date/time separator.
func hasForbiddenOperandChar(s string) bool {
	for _, r := range s {
		if r == ' ' {
			continue
		}
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}

// canonicalizeISO rewrites an accepted ISO operand into strict RFC3339 (or a bare
// date), so a single layout parses every shape the engine accepts. Enumerating one
// Go layout per shape would need eight, and go's layouts cannot express "seconds
// optional" or "offset in either form" -- which is why go alone rejected
// "2024-01-01 00:00:00", "2024-01-01T00:00" and "2024-01-01T00:00:00+0500" (#2468).
// The second return value reports whether the operand carries an explicit offset.
func canonicalizeISO(s string) (string, bool, bool) {
	m := isoOperand.FindStringSubmatch(s)
	if m == nil {
		return "", false, false
	}
	date, hh, mm, ss, frac, off := m[1], m[2], m[3], m[4], m[5], m[6]
	if hh == "" {
		return date, false, true
	}
	// The engine's DateTimeOffset.TryParse rejects hour 24 outright rather than
	// rolling it over to 00:00 the next day.
	if hh >= "24" {
		return "", false, false
	}
	if ss == "" {
		ss = "00"
	}
	// Basic offset (+0500) -> extended (+05:00); RFC3339 accepts only the latter.
	if len(off) == 5 && off != "Z" {
		off = off[:3] + ":" + off[3:]
	}
	return date + "T" + hh + ":" + mm + ":" + ss + frac + off, off != "", true
}

func parseDateTime(s string) (time.Time, bool) {
	s = strings.Trim(s, operandWhitespace)
	if s == "" || hasForbiddenOperandChar(s) {
		return time.Time{}, false
	}

	// Integer fallback: Unix time in seconds since epoch.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		// Unix seconds outside DateTimeOffset's range match NOTHING rather than wrapping to a
		// far-future instant: the engine's FromUnixTimeSeconds throws there and TryParseDateTime
		// returns false. The case that matters in practice is a MILLISECONDS timestamp pasted
		// where seconds belong -- Date.now()/currentTimeMillis() is the obvious way to produce
		// one -- which would otherwise become an instant in the year 55829 and satisfy every
		// After comparison instead of matching nothing (#2432).
		if n < minUnixSeconds || n > maxUnixSeconds {
			return time.Time{}, false
		}
		return time.Unix(n, 0).UTC(), true
	}

	canonical, hasOffset, ok := canonicalizeISO(s)
	if !ok {
		return time.Time{}, false
	}
	if hasOffset {
		// RFC3339 honors the explicit offset and yields the absolute instant.
		if t, err := time.Parse(time.RFC3339, canonical); err == nil {
			return t.UTC(), true
		}
		return time.Time{}, false
	}
	// Offset-less forms are assumed UTC, matching the engine's AssumeUniversal.
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, canonical, time.UTC); err == nil {
			return t.UTC(), true
		}
	}

	return time.Time{}, false
}

// semverVersion is a parsed semantic version: the release core as dot-separated
// numeric segments plus an optional dot-separated prerelease.
type semverVersion struct {
	release    []string
	prerelease []string
}

// compareSemver parses value as a semantic version (https://semver.org) and
// applies cmp to the comparison sign against each target, returning true if the
// comparison is satisfied for ANY target (mirroring the server engine + JS/C#
// SDKs). Returns false if value is not a parseable version, targets is empty, or
// no parseable target satisfies cmp. Unparseable targets are skipped, matching
// how compareNumeric treats non-numeric input.
func compareSemver(value string, targets []string, cmp func(c int) bool) bool {
	left, ok := parseSemver(value)
	if !ok {
		return false
	}
	for _, t := range targets {
		right, ok := parseSemver(t)
		if !ok {
			continue
		}
		if cmp(compareSemverParts(left, right)) {
			return true
		}
	}
	return false
}

// parseSemver parses value as a semantic version. ok is false when the release
// core is missing or any release segment is non-numeric. Tolerant of an optional
// leading "v"/"V", "+build" metadata (ignored for precedence), and an optional
// "-prerelease" suffix.
func parseSemver(value string) (semverVersion, bool) {
	s := strings.TrimSpace(value)
	if s == "" {
		return semverVersion{}, false
	}

	// Optional leading "v"/"V".
	if s[0] == 'v' || s[0] == 'V' {
		s = s[1:]
	}

	// Build metadata ("+...") does not affect precedence.
	if plus := strings.IndexByte(s, '+'); plus >= 0 {
		s = s[:plus]
	}

	// Split the release core from the optional "-prerelease" suffix.
	core := s
	var prerelease []string
	if dash := strings.IndexByte(s, '-'); dash >= 0 {
		core = s[:dash]
		pre := s[dash+1:]
		if pre == "" {
			return semverVersion{}, false // trailing "-" with no identifiers is malformed
		}
		prerelease = strings.Split(pre, ".")
		for _, id := range prerelease {
			if id == "" {
				return semverVersion{}, false
			}
		}
	}

	if core == "" {
		return semverVersion{}, false
	}
	release := strings.Split(core, ".")
	for _, seg := range release {
		if !isAllDigits(seg) {
			return semverVersion{}, false
		}
	}

	return semverVersion{release: release, prerelease: prerelease}, true
}

// compareSemverParts returns -1, 0, or 1 comparing a to b by semantic-version
// precedence: release segments first (missing trailing segments treated as 0),
// then prerelease.
func compareSemverParts(a, b semverVersion) int {
	maxLen := len(a.release)
	if len(b.release) > maxLen {
		maxLen = len(b.release)
	}
	for i := 0; i < maxLen; i++ {
		segA := "0"
		if i < len(a.release) {
			segA = a.release[i]
		}
		segB := "0"
		if i < len(b.release) {
			segB = b.release[i]
		}
		if c := compareNumericString(segA, segB); c != 0 {
			return c
		}
	}
	return comparePrerelease(a.prerelease, b.prerelease)
}

// comparePrerelease compares two prerelease identifier lists per semver §11.
func comparePrerelease(a, b []string) int {
	// A version with no prerelease has higher precedence than one with a prerelease.
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	if len(a) == 0 {
		return 1
	}
	if len(b) == 0 {
		return -1
	}

	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	for i := 0; i < minLen; i++ {
		if c := comparePrereleaseID(a[i], b[i]); c != 0 {
			return c
		}
	}
	// All shared identifiers equal: the longer prerelease has higher precedence.
	switch {
	case len(a) == len(b):
		return 0
	case len(a) < len(b):
		return -1
	default:
		return 1
	}
}

// comparePrereleaseID compares two prerelease identifiers: numeric identifiers
// compare numerically and rank below alphanumeric ones; alphanumeric identifiers
// compare in ASCII sort order (case-sensitive) per semver §11.
func comparePrereleaseID(a, b string) int {
	aNum := isAllDigits(a)
	bNum := isAllDigits(b)
	if aNum && bNum {
		return compareNumericString(a, b)
	}
	if aNum {
		return -1
	}
	if bNum {
		return 1
	}
	return strings.Compare(a, b)
}

// compareNumericString compares two all-digit strings as non-negative integers
// without parsing (overflow-free): strip leading zeros, then the longer string
// is the larger number; equal lengths compare ordinally.
func compareNumericString(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// isAllDigits reports whether s is non-empty and contains only ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
