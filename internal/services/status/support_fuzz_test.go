package status

import (
	"fmt"
	"strings"
	"testing"
)

// FuzzParseMinor checks that parseMinor accepts only "<digits>.<digits>",
// that an accepted version survives a format-and-parse round trip, and that
// fallbackPosture calls a version outside the calendar Unsupported only when
// it parses and is older than the oldest row, and Unknown otherwise.
func FuzzParseMinor(f *testing.F) {
	for _, s := range []string{
		"1.32", "1.28", "1.27", "0.99", "2.0", "1", "", ".", "1.", ".5",
		"1.-5", "-1.30", "1.+29", "1.32.0", "v1.32", " 1.32", "01.027",
		"1.99999999999999999999",
	} {
		f.Add(s)
	}
	oldMaj, oldMinor, _ := parseMinor(fallbackCalendar[0].version)
	now := date(2026, 9, 23)
	f.Fuzz(func(t *testing.T, v string) {
		maj, minor, ok := parseMinor(v)
		posture := fallbackPosture(v, now)
		inTable := false
		for _, e := range fallbackCalendar {
			inTable = inTable || e.version == v
		}
		if inTable {
			return // dated rows are classified by date, not by parseMinor
		}
		if !ok {
			if posture.Tier != SupportUnknown {
				t.Fatalf("fallbackPosture(%q) = %s for an unparseable version, want unknown", v, posture.Tier)
			}
			return
		}
		a, b, _ := strings.Cut(v, ".")
		for _, part := range []string{a, b} {
			if part == "" || strings.TrimLeft(part, "0123456789") != "" {
				t.Fatalf("parseMinor(%q) = %d, %d, true; accepted a non-digit component %q", v, maj, minor, part)
			}
		}
		if maj < 0 || minor < 0 {
			t.Fatalf("parseMinor(%q) = %d, %d; want non-negative", v, maj, minor)
		}
		formatted := fmt.Sprintf("%d.%d", maj, minor)
		m2, n2, ok2 := parseMinor(formatted)
		if !ok2 || m2 != maj || n2 != minor {
			t.Fatalf("round trip: parseMinor(%q) = %d.%d, parseMinor(%q) = %d.%d, %v", v, maj, minor, formatted, m2, n2, ok2)
		}
		older := maj < oldMaj || (maj == oldMaj && minor < oldMinor)
		if older != (posture.Tier == SupportUnsupported) {
			t.Fatalf("fallbackPosture(%q) = %s; older than the calendar = %v", v, posture.Tier, older)
		}
	})
}
