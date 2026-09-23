package statuscmd

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/urfave/cli/v3"

	statussvc "github.com/dantech2000/refresh/internal/services/status"
)

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ec, ok := err.(cli.ExitCoder); ok {
		return ec.ExitCode()
	}
	return -1
}

func TestExitForStatuses(t *testing.T) {
	cases := []struct {
		name     string
		statuses []statussvc.ClusterStatus
		want     int
	}{
		{
			name:     "all current",
			statuses: []statussvc.ClusterStatus{{Support: statussvc.SupportPosture{Tier: statussvc.SupportStandard}}},
			want:     0,
		},
		{
			name: "stale only → 2",
			statuses: []statussvc.ClusterStatus{{
				Support:  statussvc.SupportPosture{Tier: statussvc.SupportStandard},
				StaleAMI: statussvc.StaleAMISummary{Behind: 1, Total: 3},
			}},
			want: 2,
		},
		{
			name: "nodegroup behind control plane → 2",
			statuses: []statussvc.ClusterStatus{{
				Support:                      statussvc.SupportPosture{Tier: statussvc.SupportStandard},
				NodegroupsBehindControlPlane: 1,
			}},
			want: 2,
		},
		{
			name: "nodegroup behind control plane + incomplete → 2 (beats 4)",
			statuses: []statussvc.ClusterStatus{{
				Support:                      statussvc.SupportPosture{Tier: statussvc.SupportStandard},
				NodegroupsBehindControlPlane: 1,
				Errors:                       []string{"describe nodegroup(s): ng-broken: boom"},
			}},
			want: 2,
		},
		{
			name: "extended support → 3 (beats stale)",
			statuses: []statussvc.ClusterStatus{{
				Support:  statussvc.SupportPosture{Tier: statussvc.SupportExtended},
				StaleAMI: statussvc.StaleAMISummary{Behind: 1},
			}},
			want: 3,
		},
		{
			name: "unsupported → 3",
			statuses: []statussvc.ClusterStatus{{
				Support: statussvc.SupportPosture{Tier: statussvc.SupportUnsupported},
			}},
			want: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCode(exitForStatuses(tc.statuses, 0)); got != tc.want {
				t.Errorf("exit code = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestExitForStatuses_Incomplete(t *testing.T) {
	std := statussvc.SupportPosture{Tier: statussvc.SupportStandard}
	errored := statussvc.ClusterStatus{
		Name:    "ghost",
		Support: statussvc.SupportPosture{Tier: statussvc.SupportUnknown},
		Errors:  []string{"describe cluster: access denied"},
	}
	cases := []struct {
		name          string
		statuses      []statussvc.ClusterStatus
		failedRegions int
		want          int
	}{
		{"errored row → 4", []statussvc.ClusterStatus{{Support: std}, errored}, 0, 4},
		{"failed region → 4", []statussvc.ClusterStatus{{Support: std}}, 1, 4},
		{"stale beats incomplete", []statussvc.ClusterStatus{errored, {Support: std, StaleAMI: statussvc.StaleAMISummary{Behind: 1}}}, 1, 2},
		{"support risk beats incomplete", []statussvc.ClusterStatus{errored, {Support: statussvc.SupportPosture{Tier: statussvc.SupportExtended}}}, 0, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCode(exitForStatuses(tc.statuses, tc.failedRegions)); got != tc.want {
				t.Errorf("exit code = %d, want %d", got, tc.want)
			}
		})
	}
}

type fakeRegion struct {
	statuses []statussvc.ClusterStatus
	err      error
}

func (f fakeRegion) ListClusterStatuses(context.Context, statussvc.ListOptions) ([]statussvc.ClusterStatus, error) {
	return f.statuses, f.err
}

func stubRegionService(t *testing.T, fn func(cfg aws.Config) regionLister) {
	t.Helper()
	orig := newRegionService
	t.Cleanup(func() { newRegionService = orig })
	newRegionService = func(cfg aws.Config, _ *slog.Logger) regionLister { return fn(cfg) }
}

// A region that fails ListClusters (e.g. AccessDenied) must make the run exit
// non-zero even when another region returned clean data.
func TestGatherFleet_FailedRegionIsIncomplete(t *testing.T) {
	stubRegionService(t, func(cfg aws.Config) regionLister {
		if cfg.Region == "eu-west-1" {
			return fakeRegion{err: errors.New("AccessDeniedException: not authorized to perform eks:ListClusters")}
		}
		return fakeRegion{statuses: []statussvc.ClusterStatus{{
			Name: "prod", Region: cfg.Region,
			Support: statussvc.SupportPosture{Tier: statussvc.SupportStandard},
		}}}
	})

	statuses, errs := gatherFleet(context.Background(), aws.Config{}, []string{"us-east-1", "eu-west-1"}, statussvc.ListOptions{}, 2)
	if len(statuses) != 1 || len(errs) != 1 {
		t.Fatalf("got %d statuses / %d region errors, want 1 / 1", len(statuses), len(errs))
	}
	if got := exitCode(exitForStatuses(statuses, len(errs))); got != 4 {
		t.Errorf("exit code = %d, want 4 (incomplete data)", got)
	}
}

// A region whose sweep timed out keeps its partial rows (the service marks the
// clusters it never reached) and still reports the error.
func TestGatherFleet_KeepsPartialRowsOnError(t *testing.T) {
	stubRegionService(t, func(cfg aws.Config) regionLister {
		return fakeRegion{
			statuses: []statussvc.ClusterStatus{{Name: "late", Region: cfg.Region, Errors: []string{"not evaluated: context deadline exceeded"}}},
			err:      context.DeadlineExceeded,
		}
	})
	statuses, errs := gatherFleet(context.Background(), aws.Config{}, []string{"us-east-1"}, statussvc.ListOptions{}, 1)
	if len(statuses) != 1 || statuses[0].Name != "late" {
		t.Fatalf("partial rows dropped: %+v", statuses)
	}
	if len(errs) != 1 || !errors.Is(errs[0], context.DeadlineExceeded) {
		t.Fatalf("errs = %v, want the deadline error", errs)
	}
	if got := exitCode(exitForStatuses(statuses, len(errs))); got != 4 {
		t.Errorf("exit code = %d, want 4", got)
	}
}

func TestSortStatuses_ByStaleDescending(t *testing.T) {
	statuses := []statussvc.ClusterStatus{
		{Name: "a", StaleAMI: statussvc.StaleAMISummary{Behind: 0}},
		{Name: "b", StaleAMI: statussvc.StaleAMISummary{Behind: 5}},
		{Name: "c", StaleAMI: statussvc.StaleAMISummary{Behind: 2}},
	}
	sortStatuses(statuses, "stale", true)
	if statuses[0].Name != "b" || statuses[1].Name != "c" || statuses[2].Name != "a" {
		t.Errorf("stale desc order = %s,%s,%s, want b,c,a", statuses[0].Name, statuses[1].Name, statuses[2].Name)
	}
}

func TestSortStatuses_ByClusterName(t *testing.T) {
	statuses := []statussvc.ClusterStatus{{Name: "c"}, {Name: "a"}, {Name: "b"}}
	sortStatuses(statuses, "cluster", false)
	if statuses[0].Name != "a" || statuses[2].Name != "c" {
		t.Errorf("name order = %s..%s, want a..c", statuses[0].Name, statuses[2].Name)
	}
}
