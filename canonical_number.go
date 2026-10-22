package featureflip

import (
	"math"
	"strconv"
	"strings"
)

// canonicalNumber spells a floating-point rollout bucket value the way
// ECMAScript Number::toString does, matching the Evaluation API's
// BucketValueFormatter.CanonicalNumber so every SDK hashes one number to one
// bucket (#4236). Bucket path only; condition evaluation never calls it.
// bitSize is 64 for float64 and 32 for float32 (shortest round-trip at that width).
func canonicalNumber(x float64, bitSize int) string {
	switch {
	case math.IsNaN(x):
		return "NaN"
	case math.IsInf(x, 1):
		return "Infinity"
	case math.IsInf(x, -1):
		return "-Infinity"
	case x == 0:
		return "0"
	case x < 0:
		return "-" + canonicalNumber(-x, bitSize)
	}

	// Shortest round-trip digits in d.ddde±xx form.
	text := strconv.FormatFloat(x, 'e', -1, bitSize)
	mantissa, expText, _ := strings.Cut(text, "e")
	exp, _ := strconv.Atoi(expText)
	digits := strings.TrimRight(strings.Replace(mantissa, ".", "", 1), "0")
	if digits == "" {
		digits = "0"
	}
	k := len(digits)
	n := exp + 1

	switch {
	case k <= n && n <= 21:
		return digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return "0." + strings.Repeat("0", -n) + digits
	}
	e := n - 1
	head := digits[:1]
	if k > 1 {
		head += "." + digits[1:]
	}
	sign := "+"
	if e < 0 {
		sign = "-"
		e = -e
	}
	return head + "e" + sign + strconv.Itoa(e)
}
