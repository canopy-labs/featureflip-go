package featureflip

import (
	"fmt"
	"math"
	"testing"
)

func TestBucket_Deterministic(t *testing.T) {
	// Same inputs must always produce the same output.
	salt := "test-salt"
	value := "user-123"

	result1 := bucket(salt, value)
	result2 := bucket(salt, value)
	result3 := bucket(salt, value)

	if result1 != result2 || result2 != result3 {
		t.Errorf("bucket is not deterministic: got %d, %d, %d", result1, result2, result3)
	}
}

func TestBucket_DifferentInputs(t *testing.T) {
	// Different inputs should (very likely) produce different outputs.
	a := bucket("salt", "user-a")
	b := bucket("salt", "user-b")
	c := bucket("different-salt", "user-a")

	// While collisions are possible, it's extremely unlikely all three match.
	if a == b && b == c {
		t.Error("all three different inputs produced the same bucket, extremely unlikely")
	}
}

func TestBucket_Range(t *testing.T) {
	// All outputs must be in [0, 99].
	for i := 0; i < 1000; i++ {
		v := bucket("salt", fmt.Sprintf("user-%d", i))
		if v < 0 || v > 99 {
			t.Fatalf("bucket(%q, %q) = %d, want [0, 99]", "salt", fmt.Sprintf("user-%d", i), v)
		}
	}
}

func TestBucket_Distribution(t *testing.T) {
	// With 10000 inputs, each of the 100 buckets should get roughly 100 hits.
	// Allow a generous tolerance (chi-squared style check).
	counts := make([]int, 100)
	n := 10000

	for i := 0; i < n; i++ {
		b := bucket("distribution-test", fmt.Sprintf("user-%d", i))
		counts[b]++
	}

	expected := float64(n) / 100.0 // 100.0

	// Chi-squared test: sum of (observed - expected)^2 / expected
	// With 99 degrees of freedom, chi-squared critical value at p=0.001 is ~148.2
	var chiSquared float64
	for _, c := range counts {
		diff := float64(c) - expected
		chiSquared += (diff * diff) / expected
	}

	// Use a very generous threshold to avoid flaky tests
	if chiSquared > 200 {
		t.Errorf("distribution appears non-uniform: chi-squared = %.2f (threshold 200)", chiSquared)
	}

	// Also verify no bucket is empty and none has more than 5x expected
	for i, c := range counts {
		if c == 0 {
			t.Errorf("bucket %d has 0 entries", i)
		}
		if float64(c) > expected*5 {
			t.Errorf("bucket %d has %d entries (>5x expected %.0f)", i, c, expected)
		}
	}

	// Log min/max/stddev for visibility
	min, max := counts[0], counts[0]
	sum := 0.0
	for _, c := range counts {
		if c < min {
			min = c
		}
		if c > max {
			max = c
		}
		diff := float64(c) - expected
		sum += diff * diff
	}
	stddev := math.Sqrt(sum / 100.0)
	t.Logf("distribution: min=%d, max=%d, stddev=%.2f, chi-squared=%.2f", min, max, stddev, chiSquared)
}
