package cluster

import (
	"fmt"
	"strings"
	"testing"
)

// FuzzMinorVersion checks that the skew report's minorVersion reads only a
// plain-digit minor from "[v]<major>.<minor>[.<rest>]", never a negative
// one, and that the result round-trips through "1.%d".
func FuzzMinorVersion(f *testing.F) {
	for _, s := range []string{
		"1.31", "v1.31.2", " 1.30 ", "1", "", "1.", "1.-2", "1.+3", "v1.31-eks",
		"1.99999999999999999999", "1.0", "2.5.1", "v1..2",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		n, ok := minorVersion(v)
		if !ok {
			if n != 0 {
				t.Fatalf("minorVersion(%q) = %d, false; want 0 when not ok", v, n)
			}
			return
		}
		parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".", 3)
		if parts[1] == "" || strings.TrimLeft(parts[1], "0123456789") != "" {
			t.Fatalf("minorVersion(%q) = %d; accepted a non-digit minor %q", v, n, parts[1])
		}
		if n < 0 {
			t.Fatalf("minorVersion(%q) = %d, want >= 0", v, n)
		}
		if again, ok := minorVersion(fmt.Sprintf("1.%d", n)); !ok || again != n {
			t.Fatalf("round trip: minorVersion(%q) = %d, then %d, %v", v, n, again, ok)
		}
	})
}
