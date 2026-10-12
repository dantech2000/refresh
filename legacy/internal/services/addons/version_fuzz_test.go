package addons

import (
	"fmt"
	"strings"
	"testing"
)

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}

// FuzzCompareVersions checks that CompareVersions is a total preorder on any
// input: reflexive, antisymmetric, and transitive. Callers sort with it
// (GetAvailableVersions, cluster insights), and a comparator that breaks
// these rules gives sort an inconsistent order.
func FuzzCompareVersions(f *testing.F) {
	for _, s := range [][3]string{
		{"v1.18.1-eksbuild.3", "v1.18.1-eksbuild.10", "v1.19.0-eksbuild.1"},
		{"v1.2.0", "v1.10.0", "1.10.0"},
		{"v1.14.0", "v1.14.0-eksbuild.1", "v1.14.0+build"},
		{"", "v", "vv1"},
		{" v1.0 ", "1.0", "1.0-"},
		{"v1.0.0-alpha", "v1.0.0-beta", "v1.0.0-1"},
		{"v1.99999999999999999999", "v1.2", "v1.9223372036854775808"},
		{"v1.007", "v1.7", "v1.08"},
		{"1..2", "1.2", "1-2+3"},
	} {
		f.Add(s[0], s[1], s[2])
	}
	f.Fuzz(func(t *testing.T, a, b, c string) {
		if got := CompareVersions(a, a); got != 0 {
			t.Fatalf("CompareVersions(%q, %q) = %d, want 0", a, a, got)
		}
		ab, ba := sign(CompareVersions(a, b)), sign(CompareVersions(b, a))
		if ab != -ba {
			t.Fatalf("antisymmetry: sign(cmp(%q, %q)) = %d, sign(cmp(%q, %q)) = %d", a, b, ab, b, a, ba)
		}
		bc, ac := sign(CompareVersions(b, c)), sign(CompareVersions(a, c))
		if ab <= 0 && bc <= 0 && ac > 0 {
			t.Fatalf("transitivity: %q <= %q <= %q but cmp(a, c) > 0", a, b, c)
		}
		if ab == 0 && bc == 0 && ac != 0 {
			t.Fatalf("transitivity: %q == %q == %q but cmp(a, c) = %d", a, b, c, ac)
		}
		// A leading "v" and surrounding space are cosmetic.
		if trimmed := strings.TrimSpace(a); !strings.HasPrefix(trimmed, "v") {
			if got := CompareVersions(a, "v"+trimmed); got != 0 {
				t.Fatalf("CompareVersions(%q, %q) = %d, want 0", a, "v"+trimmed, got)
			}
		}
	})
}

// FuzzCompareVersionsNumeric builds EKS add-on versions from numbers and
// checks that the order follows the numbers, for any size of number: a
// version with a larger component (and equal components before it) is newer.
func FuzzCompareVersionsNumeric(f *testing.F) {
	f.Add(uint64(1), uint64(18), uint64(1), uint64(3), uint8(0), uint64(1))
	f.Add(uint64(1), uint64(2), uint64(0), uint64(9), uint8(1), uint64(8))
	f.Add(uint64(0), uint64(0), uint64(0), uint64(0), uint8(3), uint64(1))
	f.Add(uint64(1), uint64(9223372036854775807), uint64(0), uint64(1), uint8(1), uint64(1))
	f.Add(uint64(1), uint64(18446744073709551614), uint64(0), uint64(1), uint8(2), uint64(1))
	f.Fuzz(func(t *testing.T, major, minor, patch, build uint64, which uint8, bump uint64) {
		if bump == 0 {
			bump = 1
		}
		parts := []uint64{major, minor, patch, build}
		i := int(which) % len(parts)
		if parts[i] > ^uint64(0)-bump {
			t.Skip("bump would overflow uint64")
		}
		newer := append([]uint64(nil), parts...)
		newer[i] += bump
		format := func(p []uint64) string {
			return fmt.Sprintf("v%d.%d.%d-eksbuild.%d", p[0], p[1], p[2], p[3])
		}
		a, b := format(parts), format(newer)
		if got := CompareVersions(b, a); got <= 0 {
			t.Fatalf("CompareVersions(%q, %q) = %d, want > 0", b, a, got)
		}
		if got := CompareVersions(a, b); got >= 0 {
			t.Fatalf("CompareVersions(%q, %q) = %d, want < 0", a, b, got)
		}
		// A numeric segment is newer than a word in the same place.
		if got := CompareVersions(a, fmt.Sprintf("v%d.%d.%d-eksbuild.x", parts[0], parts[1], parts[2])); got <= 0 {
			t.Fatalf("numeric build %q should beat a non-numeric build, got %d", a, got)
		}
	})
}
