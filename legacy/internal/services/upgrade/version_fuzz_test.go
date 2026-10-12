package upgrade

import (
	"fmt"
	"strings"
	"testing"
)

// checkMinorVersion calls minorVersion(v) and, when it succeeds, checks
// that v is "1.<digits>[.anything]" and that the minor round-trips.
func checkMinorVersion(t *testing.T, v string) (int, error) {
	t.Helper()
	m, err := minorVersion(v)
	if err != nil {
		return m, err
	}
	parts := strings.SplitN(strings.TrimSpace(v), ".", 3)
	if parts[0] != "1" || parts[1] == "" || strings.TrimLeft(parts[1], "0123456789") != "" {
		t.Fatalf("minorVersion(%q) = %d; accepted a malformed version", v, m)
	}
	if m < 0 {
		t.Fatalf("minorVersion(%q) = %d, want >= 0", v, m)
	}
	if again, err := minorVersion(fmt.Sprintf("1.%d", m)); err != nil || again != m {
		t.Fatalf("round trip: minorVersion(%q) = %d, then %d, %v", v, m, again, err)
	}
	return m, nil
}

// checkHops checks expandHops(a, b) against the parsed minors ma and mb.
func checkHops(t *testing.T, a, b string, ma, mb int, hops []string, err error) {
	t.Helper()
	switch {
	case mb < ma:
		if err == nil {
			t.Fatalf("expandHops(%q, %q) = %v, want an error for an older target", a, b, hops)
		}
	case mb-ma > maxHops:
		if err == nil {
			t.Fatalf("expandHops(%q, %q) returned %d hops, over maxHops", a, b, len(hops))
		}
	default:
		if err != nil {
			t.Fatalf("expandHops(%q, %q): %v", a, b, err)
		}
		if len(hops) != mb-ma {
			t.Fatalf("expandHops(%q, %q) = %d hops, want %d", a, b, len(hops), mb-ma)
		}
		for i, h := range hops {
			if want := fmt.Sprintf("1.%d", ma+i+1); h != want {
				t.Fatalf("expandHops(%q, %q)[%d] = %q, want %q", a, b, i, h, want)
			}
		}
	}
}

// FuzzUpgradeVersions checks the minor-version helpers the upgrade planner
// builds its hops and skew checks from:
//
//   - minorVersion accepts only "1.<digits>[.anything]" and round-trips
//     through "1.%d";
//   - expandHops returns exactly the consecutive minors after from up to and
//     including to, never more than maxHops, and fails when to is older;
//   - versionAtLeast is reflexive and total on parseable versions, and false
//     when either side does not parse;
//   - beyondKubeletSkew holds only when the control plane leads the
//     nodegroup by more than kubeletSkew minors.
func FuzzUpgradeVersions(f *testing.F) {
	for _, s := range [][2]string{
		{"1.31", "1.33"}, {"1.33", "1.31"}, {"1.31", "1.31"}, {"1.28", "1.32"},
		{"1.31.2", "1.32"}, {" 1.30", "1.30 "}, {"2.0", "1.30"}, {"1", "1.3"},
		{"1.-3", "1.2"}, {"1.+33", "1.31"}, {"1.0", "1.2000000000"},
		{"1.31", "1.99"}, {"v1.31", "1.32"}, {"1.", "1.1"}, {"1.9223372036854775807", "1.0"},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		ma, errA := checkMinorVersion(t, a)
		mb, errB := checkMinorVersion(t, b)

		hops, err := expandHops(a, b)
		if errA != nil || errB != nil {
			if err == nil {
				t.Fatalf("expandHops(%q, %q) = %v with an unparseable version", a, b, hops)
			}
			if versionAtLeast(a, b) || versionAtLeast(b, a) {
				t.Fatalf("versionAtLeast true for unparseable %q / %q", a, b)
			}
			if beyondKubeletSkew(a, b) {
				t.Fatalf("beyondKubeletSkew(%q, %q) true for an unparseable version", a, b)
			}
			return
		}

		checkHops(t, a, b, ma, mb, hops, err)

		if !versionAtLeast(a, a) {
			t.Fatalf("versionAtLeast(%q, %q) = false", a, a)
		}
		if !versionAtLeast(a, b) && !versionAtLeast(b, a) {
			t.Fatalf("versionAtLeast is not total for %q, %q", a, b)
		}
		if versionAtLeast(a, b) != (ma >= mb) {
			t.Fatalf("versionAtLeast(%q, %q) disagrees with minors %d, %d", a, b, ma, mb)
		}
		if got, want := beyondKubeletSkew(a, b), mb-ma > kubeletSkew; got != want {
			t.Fatalf("beyondKubeletSkew(%q, %q) = %v, want %v", a, b, got, want)
		}
		if beyondKubeletSkew(a, b) && beyondKubeletSkew(b, a) {
			t.Fatalf("beyondKubeletSkew holds both ways for %q, %q", a, b)
		}
	})
}
