package change

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/fleet"
)

// ver parses a Kubernetes version of a test.
func ver(s string) fleet.Version {
	v, err := fleet.ParseVersion(s)
	if err != nil {
		panic(err)
	}
	return v
}

func TestHops(t *testing.T) {
	tests := []struct {
		from, to string
		want     []string
		refusal  RefusalReason
	}{
		{from: "1.31", to: "1.33", want: []string{"1.32", "1.33"}},
		{from: "1.31", to: "1.32", want: []string{"1.32"}},
		// Equal versions give no hop: the upgrade of a cluster that is at
		// its target is a catch-up, not an error.
		{from: "1.31", to: "1.31"},
		{from: "1.33", to: "1.31", refusal: RefusalOlderTarget},
		{from: "1.0", to: "1.100", want: nil},
		{from: "1.0", to: "1.101", refusal: RefusalTooManyHops},
		{from: "1.0", to: "1.2000000000", refusal: RefusalTooManyHops},
		{from: "1.33", to: "2.0", refusal: RefusalOtherMajor},
		{from: "2.0", to: "1.30", refusal: RefusalOtherMajor},
	}
	for _, tt := range tests {
		t.Run(tt.from+" to "+tt.to, func(t *testing.T) {
			hops, err := Hops(ver(tt.from), ver(tt.to))
			var refusal *RefusalError
			if tt.refusal != "" {
				if !errors.As(err, &refusal) || refusal.Reason != tt.refusal {
					t.Fatalf("err = %v, want a refusal for %s", err, tt.refusal)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.to == "1.100" {
				if len(hops) != maxHops {
					t.Fatalf("%d hops, want %d", len(hops), maxHops)
				}
				return
			}
			var got []string
			for _, h := range hops {
				got = append(got, h.String())
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("hops (-want +got):\n%s", diff)
			}
		})
	}
	if _, err := Hops(fleet.Version{}, ver("1.33")); err == nil {
		t.Error("a version that is not known gave hops")
	}
}

// FuzzUpgradeVersions checks the version helpers that the upgrade planner
// builds its hops and its skew checks from, for every pair of versions
// that fleet.ParseVersion accepts. The parser has its own fuzz test.
//
//   - Hops returns exactly the minor versions after from, up to and with
//     to, never more than maxHops, and fails when to is older or in
//     another major version.
//   - atLeast is reflexive and total on versions that are known.
//   - beyondKubeletSkew holds only when the control plane leads the
//     nodegroup by more than kubeletSkew minor versions, and never both
//     ways.
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
		from, errA := fleet.ParseVersion(a)
		to, errB := fleet.ParseVersion(b)
		if errA != nil || errB != nil {
			return
		}
		hops, err := Hops(from, to)
		switch {
		case from.IsZero() || to.IsZero(), from.Major != to.Major, to.Minor < from.Minor, to.Minor-from.Minor > maxHops:
			if err == nil {
				t.Fatalf("Hops(%s, %s) = %v, want an error", from, to, hops)
			}
		case err != nil:
			t.Fatalf("Hops(%s, %s): %v", from, to, err)
		case len(hops) != to.Minor-from.Minor:
			t.Fatalf("Hops(%s, %s) = %d hops, want %d", from, to, len(hops), to.Minor-from.Minor)
		default:
			for i, h := range hops {
				if want := (fleet.Version{Major: from.Major, Minor: from.Minor + i + 1}); h != want {
					t.Fatalf("Hops(%s, %s)[%d] = %s, want %s", from, to, i, h, want)
				}
			}
		}
		if from.IsZero() || to.IsZero() {
			if atLeast(from, to) || atLeast(to, from) || beyondKubeletSkew(from, to) {
				t.Fatalf("a version that is not known compares: %q, %q", a, b)
			}
			return
		}
		if !atLeast(from, from) {
			t.Fatalf("atLeast(%s, %s) = false", from, from)
		}
		if !atLeast(from, to) && !atLeast(to, from) {
			t.Fatalf("atLeast is not total for %s, %s", from, to)
		}
		if atLeast(from, to) != (from.Compare(to) >= 0) {
			t.Fatalf("atLeast(%s, %s) disagrees with Compare", from, to)
		}
		want := from.Major == to.Major && to.Minor-from.Minor > kubeletSkew
		if got := beyondKubeletSkew(from, to); got != want {
			t.Fatalf("beyondKubeletSkew(%s, %s) = %v, want %v", from, to, got, want)
		}
		if beyondKubeletSkew(from, to) && beyondKubeletSkew(to, from) {
			t.Fatalf("beyondKubeletSkew holds both ways for %s, %s", from, to)
		}
	})
}
