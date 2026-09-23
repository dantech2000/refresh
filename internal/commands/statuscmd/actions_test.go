package statuscmd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	"github.com/urfave/cli/v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
)

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	// Type-assert, not errors.As: cli.HandleExitCoder only honors an unwrapped
	// ExitCoder, so a wrapped one would really exit 1.
	if ec, ok := err.(cli.ExitCoder); ok { //nolint:errorlint // mirrors cli.HandleExitCoder's unwrapped check
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
			return fakeRegion{err: mocks.AccessDenied()}
		}
		return fakeRegion{statuses: []statussvc.ClusterStatus{{
			Name: "prod", Region: cfg.Region,
			Support: statussvc.SupportPosture{Tier: statussvc.SupportStandard},
		}}}
	})

	sweep := gatherFleet(context.Background(), aws.Config{}, []string{"us-east-1", "eu-west-1"}, statussvc.ListOptions{}, false)
	statuses, errs := sweep.statuses, sweep.errs
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
	sweep := gatherFleet(context.Background(), aws.Config{}, []string{"us-east-1"}, statussvc.ListOptions{}, false)
	statuses, errs := sweep.statuses, sweep.errs
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

func apiErr(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: "not authorized to perform eks:ListClusters"}
}

// deniedFleet: us-east-1 answers, sa-east-1 is SCP-denied, ap-south-1 is not
// enabled for the account, and eu-west-1 throttles past the retries. Each
// error arrives formatted, as ListAllPages returns it.
func deniedFleet(t *testing.T) {
	t.Helper()
	errs := map[string]error{
		"sa-east-1":  apiErr("AccessDeniedException"),
		"ap-south-1": apiErr("UnrecognizedClientException"),
		"eu-west-1":  apiErr("ThrottlingException"),
	}
	stubRegionService(t, func(cfg aws.Config) regionLister {
		if err, ok := errs[cfg.Region]; ok {
			return fakeRegion{err: awsinternal.FormatAWSError(err, "listing clusters in "+cfg.Region)}
		}
		return fakeRegion{statuses: []statussvc.ClusterStatus{{
			Name: "prod", Region: cfg.Region,
			Support: statussvc.SupportPosture{Tier: statussvc.SupportStandard},
		}}}
	})
}

// The default sweep skips regions closed to these credentials: one stderr
// line, not counted as failed regions. Other errors still fail, on one line.
func TestGatherFleet_DefaultSweepSkipsInaccessibleRegions(t *testing.T) {
	deniedFleet(t)
	regions := []string{"us-east-1", "sa-east-1", "ap-south-1", "eu-west-1"}
	sweep := gatherFleet(context.Background(), aws.Config{}, regions, statussvc.ListOptions{}, true)

	if strings.Join(sweep.skipped, ",") != "ap-south-1,sa-east-1" {
		t.Errorf("skipped = %v, want ap-south-1,sa-east-1", sweep.skipped)
	}
	if len(sweep.errs) != 1 || !strings.Contains(sweep.errs[0].Error(), "eu-west-1") {
		t.Fatalf("errs = %v, want only the throttled eu-west-1", sweep.errs)
	}

	var buf bytes.Buffer
	if err := reportSweep(&buf, len(regions), sweep); err != nil {
		t.Fatalf("reportSweep = %v, want nil when a region answered", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("stderr has %d lines, want 2 (skip note + one warning):\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "Skipped 2 region(s) not accessible to these credentials: ap-south-1, sa-east-1") {
		t.Errorf("skip line = %q", lines[0])
	}
	if !strings.Contains(lines[1], "warning: region eu-west-1: ThrottlingException") {
		t.Errorf("warning line = %q", lines[1])
	}

	// Only the throttled region counts toward exit 4. Clean data plus one
	// failed region is still incomplete; skipped regions alone are not.
	if got := exitCode(exitForStatuses(sweep.statuses, len(sweep.errs))); got != 4 {
		t.Errorf("exit = %d, want 4 for the throttled region", got)
	}
	clean := gatherFleet(context.Background(), aws.Config{}, regions[:3], statussvc.ListOptions{}, true)
	if len(clean.errs) != 0 || exitCode(exitForStatuses(clean.statuses, len(clean.errs))) != 0 {
		t.Errorf("skipped regions alone must not fail the run: errs = %v", clean.errs)
	}
}

// Regions the user named (-r, REFRESH_EKS_REGIONS) are never skipped: a
// denial there fails the region, one line per region on stderr.
func TestGatherFleet_ExplicitRegionsKeepFailures(t *testing.T) {
	deniedFleet(t)
	regions := []string{"us-east-1", "sa-east-1", "ap-south-1"}
	sweep := gatherFleet(context.Background(), aws.Config{}, regions, statussvc.ListOptions{}, false)
	if len(sweep.skipped) != 0 || len(sweep.errs) != 2 {
		t.Fatalf("skipped = %v, errs = %v; want 0 skipped, 2 failed", sweep.skipped, sweep.errs)
	}
	var buf bytes.Buffer
	_ = reportSweep(&buf, len(regions), sweep)
	if n := strings.Count(strings.TrimSpace(buf.String()), "\n") + 1; n != 2 {
		t.Errorf("stderr has %d lines, want one per failed region:\n%s", n, buf.String())
	}
	if got := exitCode(exitForStatuses(sweep.statuses, len(sweep.errs))); got != 4 {
		t.Errorf("exit = %d, want 4", got)
	}
}

// Every region skipped is not a clean empty fleet. Nothing was gathered, so
// it is an error (exit 1), as in `cluster list` (REF-165).
func TestReportSweep_AllRegionsSkippedFails(t *testing.T) {
	var buf bytes.Buffer
	err := reportSweep(&buf, 2, fleetSweep{skipped: []string{"sa-east-1", "ap-south-1"}})
	if err == nil || !strings.Contains(err.Error(), "none is accessible") {
		t.Fatalf("err = %v, want a none-accessible error", err)
	}
	if got := runner.ExitCodeOf(err); got != 1 {
		t.Errorf("exit = %d, want 1", got)
	}
}

func TestResolveRegions_DefaultSweep(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		env         string
		cfgRegion   string
		wantDefault bool
	}{
		{name: "all-regions", args: []string{"-A"}, cfgRegion: "us-east-1", wantDefault: true},
		{name: "explicit -r", args: []string{"-A", "-r", "us-east-1"}, wantDefault: false},
		{name: "env regions", args: []string{"-A"}, env: "us-east-1,eu-west-1", wantDefault: false},
		{name: "config region", args: nil, cfgRegion: "us-west-2", wantDefault: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("REFRESH_EKS_REGIONS", tc.env)
			var got bool
			cmd := &cli.Command{
				Name: "status",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "all-regions", Aliases: []string{"A"}},
					&cli.StringSliceFlag{Name: "region", Aliases: []string{"r"}},
				},
				Action: func(_ context.Context, cmd *cli.Command) error {
					_, got = resolveRegions(cmd, aws.Config{Region: tc.cfgRegion})
					return nil
				},
			}
			if err := cmd.Run(t.Context(), append([]string{"status"}, tc.args...)); err != nil {
				t.Fatal(err)
			}
			if got != tc.wantDefault {
				t.Errorf("defaultSweep = %v, want %v", got, tc.wantDefault)
			}
		})
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

// Nodegroups behind the control plane count toward the "stale" sort key.
func TestSortStatuses_ByStaleCountsBehindControlPlane(t *testing.T) {
	statuses := []statussvc.ClusterStatus{
		{Name: "a"},
		{Name: "b", NodegroupsBehindControlPlane: 3},
		{Name: "c", StaleAMI: statussvc.StaleAMISummary{Behind: 1}},
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
