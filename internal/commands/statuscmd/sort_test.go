package statuscmd

import (
	"slices"
	"testing"

	statussvc "github.com/dantech2000/refresh/internal/services/status"
)

// Every --sort key orders by its field and breaks ties by name, then region.
func TestSortStatuses_Keys(t *testing.T) {
	tier := func(t statussvc.SupportTier) statussvc.SupportPosture { return statussvc.SupportPosture{Tier: t} }
	fleet := func() []statussvc.ClusterStatus {
		return []statussvc.ClusterStatus{
			{Name: "b", Region: "us-west-2", Version: "1.31", Support: tier(statussvc.SupportExtended)},
			{Name: "a", Region: "us-west-2", Version: "1.33", Support: tier(statussvc.SupportStandard)},
			{Name: "c", Region: "eu-west-1", Version: "1.29", Support: tier(statussvc.SupportUnsupported)},
			{Name: "a", Region: "eu-west-1", Version: "1.33", Support: tier(statussvc.SupportUnknown)},
			{Name: "d", Region: "eu-west-1", Version: "1.31", Support: tier("SOMETHING_NEW")},
		}
	}
	key := func(s []statussvc.ClusterStatus) []string {
		var out []string
		for _, c := range s {
			out = append(out, c.Name+"/"+c.Region)
		}
		return out
	}
	cases := []struct {
		sort string
		desc bool
		want []string
	}{
		{"cluster", false, []string{"a/eu-west-1", "a/us-west-2", "b/us-west-2", "c/eu-west-1", "d/eu-west-1"}},
		{" Region ", false, []string{"a/eu-west-1", "c/eu-west-1", "d/eu-west-1", "a/us-west-2", "b/us-west-2"}},
		{"version", false, []string{"c/eu-west-1", "b/us-west-2", "d/eu-west-1", "a/eu-west-1", "a/us-west-2"}},
		// Support order: standard, then unknown and unrecognised (tied), then extended, then unsupported.
		{"support", false, []string{"a/us-west-2", "a/eu-west-1", "d/eu-west-1", "b/us-west-2", "c/eu-west-1"}},
		{"support", true, []string{"c/eu-west-1", "b/us-west-2", "d/eu-west-1", "a/eu-west-1", "a/us-west-2"}},
		{"bogus", false, []string{"a/eu-west-1", "a/us-west-2", "b/us-west-2", "c/eu-west-1", "d/eu-west-1"}},
	}
	for _, tc := range cases {
		s := fleet()
		sortStatuses(s, tc.sort, tc.desc)
		if got := key(s); !slices.Equal(got, tc.want) {
			t.Errorf("sort %q desc=%v = %v, want %v", tc.sort, tc.desc, got, tc.want)
		}
	}
}
