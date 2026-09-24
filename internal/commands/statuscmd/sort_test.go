package statuscmd

import (
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/dantech2000/refresh/internal/commands/runner"
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
	}
	for _, tc := range cases {
		s := fleet()
		sortStatuses(s, tc.sort, tc.desc)
		if got := key(s); !slices.Equal(got, tc.want) {
			t.Errorf("sort %q desc=%v = %v, want %v", tc.sort, tc.desc, got, tc.want)
		}
	}
}

// An unknown --sort key is a usage error naming the valid keys, raised
// before any region is swept; it never falls back to sorting by cluster.
func TestRunStatus_UnknownSortIsAnError(t *testing.T) {
	for _, key := range []string{"cluster", " Region ", "STALE", ""} {
		if err := validateSort(key); err != nil {
			t.Errorf("validateSort(%q) = %v, want nil", key, err)
		}
	}

	fakeAWSEnv(t)
	swept := false
	stubRegionService(t, func(aws.Config) regionLister {
		swept = true
		return fakeRegion{}
	})
	out, err := runStatusCLI(t, "-r", "us-east-1", "-o", "json", "--sort", "bogus")
	if err == nil {
		t.Fatal("--sort bogus succeeded, want an error")
	}
	if got := runner.ExitCodeOf(err); got != 1 {
		t.Errorf("exit = %d, want 1", got)
	}
	for _, want := range []string{`"bogus"`, "cluster, region, version, support, stale"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if swept || out != "" {
		t.Errorf("an invalid --sort must fail before the sweep (swept=%v, stdout=%q)", swept, out)
	}
}
