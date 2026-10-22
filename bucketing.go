package featureflip

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// bucket computes a deterministic bucket in [0, 99] for a given salt and value.
// Used for percentage rollout flag evaluation.
func bucket(salt, value string) int {
	h := md5.Sum([]byte(salt + ":" + value))
	n := binary.LittleEndian.Uint32(h[:4])
	return int(n % 100)
}

// bucketValue returns the text a rollout hashes for the bucketBy attribute, or
// ok=false when the context has no value to bucket on (#4236). It differs from
// getAttributeValue, which conditions share and which must not change, in two
// ways: floating-point values are spelled by canonicalNumber, and the built-in
// userId/user_id aliases fall back to an attribute of that exact name when
// EvaluationContext.UserID is empty, as the engine does (so a user_id of 0 or
// false supplied as an attribute still buckets).
func bucketValue(ctx EvaluationContext, attr string) (string, bool) {
	v, ok := getRawAttributeValue(ctx, attr)
	if !ok && (attr == "userId" || attr == "user_id") && ctx.Attributes != nil {
		v, ok = ctx.Attributes[attr]
		if v == nil {
			ok = false
		}
	}
	if !ok {
		return "", false
	}
	switch n := v.(type) {
	case bool:
		return strconv.FormatBool(n), true
	case float64:
		return canonicalNumber(n, 64), true
	case float32:
		return canonicalNumber(float64(n), 32), true
	case json.Number:
		// An integer literal keeps its digits; anything else is a decimal number.
		if isIntegerLiteral(n.String()) {
			return integerLiteralText(n.String()), true
		}
		// An out-of-range literal (1e400) yields ±Inf with ErrRange, which
		// canonicalNumber spells "Infinity"/"-Infinity" like the engine.
		if f, err := n.Float64(); err == nil || errors.Is(err, strconv.ErrRange) {
			return canonicalNumber(f, 64), true
		}
		return n.String(), true
	}
	return fmt.Sprintf("%v", v), true
}

// integerLiteralText is an integer literal's decimal text; negative zero ("-0")
// is "0", like every JSON parser's 0 and the engine (#4236).
func integerLiteralText(s string) string {
	if strings.Trim(s, "-0") == "" {
		return "0"
	}
	return s
}

// isIntegerLiteral reports whether s is an optional minus sign followed by digits only.
func isIntegerLiteral(s string) bool {
	s = strings.TrimPrefix(s, "-")
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
