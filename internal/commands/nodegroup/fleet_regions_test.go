package nodegroup

import (
	"slices"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// fleetDryRunRegions runs a fleet dry-run with args and returns the region
// of each cluster entry, sorted.
func fleetDryRunRegions(t *testing.T, args ...string) (regions []string, stderr string) {
	t.Helper()
	stdout, stderr, err := fakeaws.Run(t, fakeaws.App(Command()), args...)
	if err != nil {
		t.Fatalf("%v: %v\nstderr:\n%s", args, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	for _, c := range doc["clusters"].([]any) {
		regions = append(regions, c.(map[string]any)["region"].(string))
	}
	slices.Sort(regions)
	return regions, stderr
}

// The fleet sweep follows the multi-region flag rule: a local -r is the scan
// list and wins over a global --region; a global --region before the
// subcommand only sets the home region (and so the partition), and
// REFRESH_EKS_REGIONS still applies. The fake endpoint serves the same
// cluster in every region, so each swept region yields one entry.
func TestFleetRegions_GlobalAndLocalRegion(t *testing.T) {
	update := []string{"nodegroup", "update", "--all-clusters", "--dry-run", "-o", "json"}
	for _, tc := range []struct {
		name string
		env  string
		args []string
		want []string
	}{
		{
			name: "local -r is the scan list",
			env:  "us-west-2,ap-south-1",
			args: append(slices.Clone(update), "-r", "eu-west-1"),
			want: []string{"eu-west-1"},
		},
		{
			name: "local -r wins over global --region",
			args: append([]string{"--region", "us-west-2"}, append(slices.Clone(update), "-r", "eu-west-1", "-r", "us-east-1")...),
			want: []string{"eu-west-1", "us-east-1"},
		},
		{
			name: "global --region keeps REFRESH_EKS_REGIONS",
			env:  "us-west-2,ap-south-1",
			args: append([]string{"--region", "eu-west-1"}, update...),
			want: []string{"ap-south-1", "us-west-2"},
		},
		{
			name: "global --region picks the partition",
			args: append([]string{"--region", "cn-north-1"}, update...),
			want: []string{"cn-north-1", "cn-northwest-1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
			t.Setenv("REFRESH_EKS_REGIONS", tc.env)
			got, _ := fleetDryRunRegions(t, append([]string{"refresh"}, tc.args...)...)
			if !slices.Equal(got, tc.want) {
				t.Errorf("swept regions = %v, want %v", got, tc.want)
			}
		})
	}
}

// A global --region does not make the default sweep explicit: regions closed
// to these credentials are still skipped with one note, not failed.
func TestFleetRegions_GlobalRegionKeepsInaccessibleSkip(t *testing.T) {
	srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	srv.FailRegions(func(region string) string {
		if region == "us-west-2" {
			return ""
		}
		return "AccessDeniedException"
	})
	got, stderr := fleetDryRunRegions(t, "refresh", "--region", "us-west-2", "nodegroup", "update", "--all-clusters", "--dry-run", "-o", "json")
	if !slices.Equal(got, []string{"us-west-2"}) {
		t.Errorf("swept regions = %v, want [us-west-2]", got)
	}
	if !strings.Contains(stderr, "not accessible to these credentials") || strings.Contains(stderr, "Warning: skipping region") {
		t.Errorf("stderr should carry one skip note and no region warnings:\n%s", stderr)
	}
}
