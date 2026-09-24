package updatecheck

import (
	"fmt"
	"strings"
	"testing"
)

// FuzzParseSemver checks that parseSemver accepts only "[v]X.Y.Z" with
// plain decimal components, and that an accepted version survives a
// format-and-parse round trip.
func FuzzParseSemver(f *testing.F) {
	for _, s := range []string{
		"v0.11.0", "1.2.3", " v1.2.3 ", "dev", "", "v", "v1.2", "v1.2.3.4",
		"v1.2.3-rc.1", "v+1.2.3", "v1.-2.3", "v01.2.3", "v1..3", "vv1.2.3",
		"v99999999999999999999.0.0", "v1.2.3\n", "v１.2.3",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		got, ok := parseSemver(v)
		if !ok {
			// The only caller, compareSemver, discards the value when ok is
			// false; FuzzCompareSemver checks that it then returns 0.
			return
		}
		body := strings.TrimPrefix(strings.TrimSpace(v), "v")
		for _, r := range body {
			if r != '.' && (r < '0' || r > '9') {
				t.Fatalf("parseSemver(%q) accepted a non-digit %q", v, r)
			}
		}
		for i, n := range got {
			if n < 0 {
				t.Fatalf("parseSemver(%q)[%d] = %d, want >= 0", v, i, n)
			}
		}
		formatted := fmt.Sprintf("v%d.%d.%d", got[0], got[1], got[2])
		again, ok := parseSemver(formatted)
		if !ok || again != got {
			t.Fatalf("round trip: parseSemver(%q) = %v, parseSemver(%q) = %v, %v", v, got, formatted, again, ok)
		}
		if cmp, ok := compareSemver(v, formatted); !ok || cmp != 0 {
			t.Fatalf("compareSemver(%q, %q) = %d, %v; want 0, true", v, formatted, cmp, ok)
		}
	})
}

// FuzzCompareSemver checks that compareSemver is a total order on the
// versions it accepts, that ok does not depend on argument order, and that
// UpgradeHint never offers an upgrade in both directions.
func FuzzCompareSemver(f *testing.F) {
	for _, s := range [][3]string{
		{"v0.11.0", "v0.12.0", "0.11.1"},
		{"1.2.3", "v1.2.3", " v1.2.3"},
		{"dev", "v1.0.0", "v2.0.0"},
		{"v1.10.0", "v1.9.9", "v1.9.10"},
		{"v1.2.3-rc.1", "v1.2.3", ""},
	} {
		f.Add(s[0], s[1], s[2])
	}
	f.Fuzz(func(t *testing.T, a, b, c string) {
		ab, okAB := compareSemver(a, b)
		ba, okBA := compareSemver(b, a)
		if okAB != okBA {
			t.Fatalf("ok differs by order: (%q, %q) -> %v, reversed -> %v", a, b, okAB, okBA)
		}
		if !okAB {
			if ab != 0 {
				t.Fatalf("compareSemver(%q, %q) = %d, false; want 0 when not ok", a, b, ab)
			}
			if h := UpgradeHint(a, b); h != "" {
				t.Fatalf("UpgradeHint(%q, %q) = %q for unparseable input", a, b, h)
			}
			return
		}
		if ab < -1 || ab > 1 {
			t.Fatalf("compareSemver(%q, %q) = %d, want -1, 0, or 1", a, b, ab)
		}
		if ab != -ba {
			t.Fatalf("antisymmetry: cmp(%q, %q) = %d, cmp(%q, %q) = %d", a, b, ab, b, a, ba)
		}
		if aa, _ := compareSemver(a, a); aa != 0 {
			t.Fatalf("compareSemver(%q, %q) = %d, want 0", a, a, aa)
		}
		if UpgradeHint(a, b) != "" && UpgradeHint(b, a) != "" {
			t.Fatalf("UpgradeHint offers an upgrade both ways for %q and %q", a, b)
		}
		if (UpgradeHint(a, b) != "") != (ab < 0) {
			t.Fatalf("UpgradeHint(%q, %q) disagrees with compareSemver = %d", a, b, ab)
		}
		bc, okBC := compareSemver(b, c)
		ac, okAC := compareSemver(a, c)
		if okBC && okAC && ab <= 0 && bc <= 0 && ac > 0 {
			t.Fatalf("transitivity: %q <= %q <= %q but cmp(a, c) = %d", a, b, c, ac)
		}
	})
}
