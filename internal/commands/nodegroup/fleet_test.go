package nodegroup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/monitoring"
)

func TestFleetExit_WorstOutcome(t *testing.T) {
	cases := []struct {
		name       string
		results    []clusterUpdateResult
		regionErrs []regionDiscoveryError
		want       int
	}{
		{"all clean", []clusterUpdateResult{{Outcomes: updateOutcomes{Started: []string{"a"}}}}, nil, 0},
		{"health blocked", []clusterUpdateResult{{HealthBlocked: true, Error: "block"}}, nil, 3},
		{"update failed", []clusterUpdateResult{{Outcomes: updateOutcomes{Failed: []string{"a"}}}}, nil, 4},
		{"verify failed", []clusterUpdateResult{{VerifyFailed: true, Outcomes: updateOutcomes{Started: []string{"a"}}}}, nil, 5},
		{
			"worst wins (block + verify → 5)",
			[]clusterUpdateResult{
				{HealthBlocked: true, Error: "x"},
				{VerifyFailed: true, Outcomes: updateOutcomes{Started: []string{"a"}}},
			},
			nil,
			5,
		},
		{
			"error counts as 4",
			[]clusterUpdateResult{{Error: "monitor boom"}},
			nil,
			4,
		},
		{
			"interrupt exits 1 like the single-cluster path",
			[]clusterUpdateResult{{Interrupted: true, Outcomes: updateOutcomes{Started: []string{"a"}}}},
			nil,
			1,
		},
		{
			"monitor timeout exits 1 like the single-cluster path",
			[]clusterUpdateResult{{TimedOut: true, Outcomes: updateOutcomes{Started: []string{"a"}}}},
			nil,
			1,
		},
		{
			"interrupt does not mask a failed start",
			[]clusterUpdateResult{{Interrupted: true, Outcomes: updateOutcomes{Failed: []string{"a"}}}},
			nil,
			4,
		},
		{
			"a region that could not be listed fails an otherwise clean run",
			[]clusterUpdateResult{{Outcomes: updateOutcomes{Started: []string{"a"}}}},
			[]regionDiscoveryError{{Region: "eu-west-1", Error: "denied"}},
			4,
		},
		{
			"verification still outranks a discovery error",
			[]clusterUpdateResult{{VerifyFailed: true, Outcomes: updateOutcomes{Started: []string{"a"}}}},
			[]regionDiscoveryError{{Region: "eu-west-1", Error: "denied"}},
			5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeOf(fleetExit(tc.results, tc.regionErrs)); got != tc.want {
				t.Errorf("fleetExit = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRecordMonitorError(t *testing.T) {
	cases := []struct {
		name                  string
		err                   error
		interrupted, timedOut bool
		wantError             string
	}{
		{"nil", nil, false, false, ""},
		{"cancelled", monitoring.ErrCancelled, true, false, ""},
		{"wrapped cancelled", fmt.Errorf("x: %w", monitoring.ErrCancelled), true, false, ""},
		{"timeout", monitoring.ErrMonitorTimeout, false, true, ""},
		{"other", errors.New("nodegroup ng failed"), false, false, "nodegroup ng failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r clusterUpdateResult
			recordMonitorError(&r, tc.err)
			if r.Interrupted != tc.interrupted || r.TimedOut != tc.timedOut || r.Error != tc.wantError {
				t.Errorf("got interrupted=%v timedOut=%v error=%q", r.Interrupted, r.TimedOut, r.Error)
			}
		})
	}
}

func TestSummarizeClusterResult_InterruptAndTimeout(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prev })

	got := summarizeClusterResult(clusterUpdateResult{Cluster: "prod", Interrupted: true, Outcomes: updateOutcomes{Started: []string{"ng"}}})
	want := "interrupted (update continues in AWS; check with refresh nodegroup list prod)"
	if got != want {
		t.Errorf("interrupted = %q, want %q", got, want)
	}
	if got := summarizeClusterResult(clusterUpdateResult{Cluster: "prod", Interrupted: true}); got != "interrupted before any update started" {
		t.Errorf("interrupted before start = %q", got)
	}
	if got := summarizeClusterResult(clusterUpdateResult{Cluster: "prod", TimedOut: true}); !strings.Contains(got, "monitoring timed out") || strings.Contains(got, "failed") {
		t.Errorf("timed out = %q", got)
	}
}

func fakeLister(byRegion map[string][]string, errs map[string]error) listClustersFunc {
	return func(_ context.Context, cfg aws.Config) ([]string, error) {
		if err := errs[cfg.Region]; err != nil {
			return nil, err
		}
		return byRegion[cfg.Region], nil
	}
}

func TestDiscoverFleetTargets_CollectsRegionErrors(t *testing.T) {
	list := fakeLister(
		map[string][]string{"us-east-1": {"a", "b"}, "us-west-2": {"c"}},
		map[string]error{"eu-west-1": errors.New("AccessDeniedException: eks:ListClusters")},
	)
	targets, regionErrs, err := discoverFleetTargets(context.Background(), aws.Config{}, []string{"us-east-1", "eu-west-1", "us-west-2"}, list)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tg := range targets {
		got = append(got, tg.region+"/"+tg.cluster)
		if tg.awsCfg.Region != tg.region {
			t.Errorf("target %s has config region %q", tg.cluster, tg.awsCfg.Region)
		}
	}
	if strings.Join(got, ",") != "us-east-1/a,us-east-1/b,us-west-2/c" {
		t.Errorf("targets = %v", got)
	}
	if len(regionErrs) != 1 || regionErrs[0].Region != "eu-west-1" || !strings.Contains(regionErrs[0].Error, "AccessDenied") {
		t.Errorf("regionErrs = %+v", regionErrs)
	}
}

func TestDiscoverFleetTargets_CancelledContextIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	list := func(c context.Context, _ aws.Config) ([]string, error) {
		cancel()
		return nil, c.Err()
	}
	targets, regionErrs, err := discoverFleetTargets(ctx, aws.Config{}, []string{"us-east-1", "us-west-2"}, list)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if targets != nil || regionErrs != nil {
		t.Errorf("want no partial result, got targets=%v regionErrs=%v", targets, regionErrs)
	}
}

func TestDiscoveryStopError(t *testing.T) {
	// The --timeout bound on discovery is a failure (exit 4).
	err := discoveryStopError(context.Background(), fmt.Errorf("fleet discovery stopped: %w", context.DeadlineExceeded), 0)
	if exitCodeOf(err) != 4 {
		t.Errorf("deadline: exit = %d, want 4 (err %v)", exitCodeOf(err), err)
	}
	// A user interrupt passes through unchanged.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := fmt.Errorf("fleet discovery stopped: %w", context.Canceled)
	if got := discoveryStopError(ctx, in, 0); !errors.Is(got, context.Canceled) {
		t.Errorf("interrupt: got %v", got)
	}
}

func TestCheckDiscovery(t *testing.T) {
	var buf bytes.Buffer
	prev := fleetStderr
	fleetStderr = &buf
	t.Cleanup(func() { fleetStderr = prev })

	denied := []regionDiscoveryError{{Region: "eu-west-1", Error: "denied"}}
	cases := []struct {
		name             string
		regions, targets int
		errs             []regionDiscoveryError
		want             int
	}{
		{"clean", 2, 3, nil, 0},
		{"clean but empty", 2, 0, nil, 0},
		{"all regions failed", 1, 0, denied, 4},
		{"some failed, none found elsewhere", 2, 0, denied, 4},
		{"some failed, clusters found elsewhere", 2, 3, denied, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			if got := exitCodeOf(checkDiscovery(tc.regions, tc.targets, tc.errs)); got != tc.want {
				t.Errorf("exit = %d, want %d", got, tc.want)
			}
			if len(tc.errs) > 0 && !strings.Contains(buf.String(), "skipping region eu-west-1: denied") {
				t.Errorf("missing stderr warning, got %q", buf.String())
			}
		})
	}
	if exitCodeOf(discoveryExit(denied)) != 4 || discoveryExit(nil) != nil {
		t.Error("discoveryExit: want 4 with region errors, nil without")
	}
}

// runFleetValidation parses argv with the real `nodegroup update` flags and
// returns validateFleetFlags' verdict.
func runFleetValidation(t *testing.T, argv ...string) error {
	t.Helper()
	cmd := updateAMICommand()
	var verr error
	cmd.Action = func(_ context.Context, c *cli.Command) error {
		verr = validateFleetFlags(c)
		return nil
	}
	if err := cmd.Run(context.Background(), append([]string{"update"}, argv...)); err != nil {
		t.Fatal(err)
	}
	return verr
}

func TestValidateFleetFlags(t *testing.T) {
	t.Setenv("EKS_CLUSTER_NAME", "")
	cases := []struct {
		name    string
		argv    []string
		wantErr string
	}{
		{"plain fleet run", []string{"--all-clusters", "--yes"}, ""},
		{"nodegroup flag is fine", []string{"--all-clusters", "-n", "ng-spot"}, ""},
		{"positional nodegroup", []string{"--all-clusters", "ng-spot", "--yes"}, "positional"},
		{"positional before flag", []string{"prod", "--all-clusters"}, "positional"},
		{"cluster flag", []string{"--all-clusters", "--cluster", "prod"}, "--cluster"},
		{"cluster short flag", []string{"--all-clusters", "-c", "prod"}, "--cluster"},
		{"kube-context", []string{"--all-clusters", "--kube-context", "admin@prod"}, "--kube-context"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runFleetValidation(t, tc.argv...)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want mention of %q", err, tc.wantErr)
			}
		})
	}
}

// An exported EKS_CLUSTER_NAME must not block fleet mode; only a --cluster
// given on the command line does.
func TestValidateFleetFlags_EnvClusterIsNotAnError(t *testing.T) {
	t.Setenv("EKS_CLUSTER_NAME", "prod")
	if err := runFleetValidation(t, "--all-clusters", "--yes"); err != nil {
		t.Fatalf("env cluster rejected: %v", err)
	}
	if err := runFleetValidation(t, "--all-clusters", "--cluster", "staging"); err == nil {
		t.Fatal("--cluster on the command line was accepted with EKS_CLUSTER_NAME set")
	}
}
