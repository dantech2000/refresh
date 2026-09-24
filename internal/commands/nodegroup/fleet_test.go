package nodegroup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/diag"
)

func TestFleetExit_WorstOutcome(t *testing.T) {
	denied := []diag.Failure{diag.New(diag.KindRegion, "eu-west-1", diag.ReasonAccessDenied, "denied")}
	cases := []struct {
		name     string
		statuses []clusterStatus
		regions  []diag.Failure
		want     int
	}{
		{"all clean", []clusterStatus{clusterSucceeded}, nil, 0},
		{"health blocked", []clusterStatus{clusterHealthBlocked}, nil, 3},
		{"health warnings exit 2 like the single-cluster path", []clusterStatus{clusterHealthWarned}, nil, 2},
		{"a block outranks warnings", []clusterStatus{clusterHealthWarned, clusterHealthBlocked}, nil, 3},
		{"update failed", []clusterStatus{clusterFailed}, nil, 4},
		{"incomplete data", []clusterStatus{clusterIncomplete}, nil, 4},
		{"verify failed", []clusterStatus{clusterVerifyFailed}, nil, 5},
		{"worst wins (block + verify → 5)", []clusterStatus{clusterHealthBlocked, clusterVerifyFailed}, nil, 5},
		{"interrupt exits 1 like the single-cluster path", []clusterStatus{clusterInterrupted}, nil, 1},
		{"monitor timeout exits 1 like the single-cluster path", []clusterStatus{clusterTimedOut}, nil, 1},
		{"a cluster never reached exits 1", []clusterStatus{clusterSucceeded, clusterNotAttempted}, nil, 1},
		{"a failed cluster outranks an interrupted one", []clusterStatus{clusterInterrupted, clusterFailed}, nil, 4},
		{"a region that could not be listed fails an otherwise clean run", []clusterStatus{clusterSucceeded}, denied, 4},
		{"verification still outranks a discovery error", []clusterStatus{clusterVerifyFailed}, denied, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var results []clusterUpdateResult
			for _, s := range tc.statuses {
				results = append(results, clusterUpdateResult{Status: s})
			}
			if got := exitCodeOf(fleetExit(results, tc.regions)); got != tc.want {
				t.Errorf("fleetExit = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSummarizeClusterResult_InterruptAndTimeout(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prev })

	started := []nodegroupResult{{Name: "ng", Status: ngInProgress, UpdateID: "u-1"}}
	got := summarizeClusterResult(clusterUpdateResult{Cluster: "prod", Status: clusterInterrupted, Nodegroups: started})
	want := "interrupted (update continues in AWS; check with refresh nodegroup list prod)"
	if got != want {
		t.Errorf("interrupted = %q, want %q", got, want)
	}
	if got := summarizeClusterResult(clusterUpdateResult{Cluster: "prod", Status: clusterInterrupted}); got != "interrupted before any update started" {
		t.Errorf("interrupted before start = %q", got)
	}
	if got := summarizeClusterResult(clusterUpdateResult{Cluster: "prod", Status: clusterTimedOut}); !strings.Contains(got, "timed out") || strings.Contains(got, "failed") {
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

func apiErr(code string) error {
	// Wrapped the way the SDK's operation errors wrap the API error.
	return fmt.Errorf("operation error EKS: ListClusters: %w", &smithy.GenericAPIError{Code: code, Message: code + " message"})
}

func captureFleetStderr(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := fleetStderr
	fleetStderr = &buf
	t.Cleanup(func() { fleetStderr = prev })
	return &buf
}

func TestDiscoverFleetTargets_CollectsRegionErrors(t *testing.T) {
	list := fakeLister(
		map[string][]string{"us-east-1": {"a", "b"}, "us-west-2": {"c"}},
		map[string]error{"eu-west-1": apiErr("ThrottlingException")},
	)
	d, err := discoverFleetTargets(context.Background(), aws.Config{}, []string{"us-east-1", "eu-west-1", "us-west-2"}, true, list)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tg := range d.targets {
		got = append(got, tg.region+"/"+tg.cluster)
		if tg.awsCfg.Region != tg.region {
			t.Errorf("target %s has config region %q", tg.cluster, tg.awsCfg.Region)
		}
	}
	if strings.Join(got, ",") != "us-east-1/a,us-east-1/b,us-west-2/c" {
		t.Errorf("targets = %v", got)
	}
	// Throttling that outlived the retries is a failure even in the default
	// sweep: a Region failure with the reason and the IAM action.
	if len(d.failed) != 1 || len(d.skipped) != 0 {
		t.Fatalf("failed = %+v, skipped = %v", d.failed, d.skipped)
	}
	f := d.failed[0]
	if f.Kind != diag.KindRegion || f.Name != "eu-west-1" || f.Region != "eu-west-1" || f.Reason != diag.ReasonThrottled ||
		f.Operation != diag.OpListClusters || !f.Retryable || f.AWSErrorCode != "ThrottlingException" {
		t.Errorf("failure = %+v", f)
	}
}

// Default sweep: regions an SCP denies are skipped with one note (sorted, like
// status -A and cluster list -A), and a run
// whose reachable clusters update cleanly exits 0.
func TestFleetDiscovery_DefaultSweepSkipsDeniedRegions(t *testing.T) {
	buf := captureFleetStderr(t)
	regions := []string{"us-east-1", "sa-east-1", "ap-south-1"}
	list := fakeLister(
		map[string][]string{"us-east-1": {"prod"}},
		map[string]error{"sa-east-1": apiErr("AccessDeniedException"), "ap-south-1": apiErr("UnrecognizedClientException")},
	)
	d, err := discoverFleetTargets(context.Background(), aws.Config{}, regions, true, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.failed) != 0 || strings.Join(d.skipped, ",") != "ap-south-1,sa-east-1" || len(d.targets) != 1 {
		t.Fatalf("discovery = %+v", d)
	}
	if err := checkDiscovery(len(regions), d); err != nil {
		t.Fatalf("checkDiscovery: %v", err)
	}
	want := "Skipped 2 region(s) not accessible to these credentials: ap-south-1, sa-east-1 (scope with -r or REFRESH_EKS_REGIONS)"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("stderr = %q, want %q", buf.String(), want)
	}
	if strings.Count(buf.String(), "\n") != 1 {
		t.Errorf("want one stderr line, got %q", buf.String())
	}
	clean := []clusterUpdateResult{{Cluster: "prod", Status: clusterSucceeded}}
	if err := fleetExit(clean, d.failed); err != nil {
		t.Errorf("fleetExit = %v, want nil", err)
	}
}

// Regions the user asked for (-r / REFRESH_EKS_REGIONS) are never skipped: a
// denied one fails the run.
func TestFleetDiscovery_ExplicitRegionDeniedFails(t *testing.T) {
	_ = captureFleetStderr(t)
	regions := []string{"us-east-1", "sa-east-1"}
	list := fakeLister(map[string][]string{"us-east-1": {"prod"}}, map[string]error{"sa-east-1": apiErr("AccessDeniedException")})
	d, err := discoverFleetTargets(context.Background(), aws.Config{}, regions, false, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.skipped) != 0 || len(d.failed) != 1 || d.failed[0].Reason != diag.ReasonAccessDenied {
		t.Fatalf("discovery = %+v", d)
	}
	if err := checkDiscovery(len(regions), d); err != nil {
		t.Fatalf("checkDiscovery: %v (clusters were found, the run continues)", err)
	}
	clean := []clusterUpdateResult{{Cluster: "prod", Status: clusterSucceeded}}
	if got := exitCodeOf(fleetExit(clean, d.failed)); got != 4 {
		t.Errorf("fleetExit = %d, want 4", got)
	}
}

// If every default region is denied, nothing is reachable: fail.
func TestFleetDiscovery_AllDefaultRegionsDenied(t *testing.T) {
	_ = captureFleetStderr(t)
	regions := []string{"us-east-1", "eu-west-1"}
	list := fakeLister(nil, map[string]error{"us-east-1": apiErr("AccessDeniedException"), "eu-west-1": apiErr("OptInRequired")})
	d, err := discoverFleetTargets(context.Background(), aws.Config{}, regions, true, list)
	if err != nil {
		t.Fatal(err)
	}
	err = checkDiscovery(len(regions), d)
	if err == nil || runner.ExitCodeOf(err) != 1 || !strings.Contains(err.Error(), "-r or REFRESH_EKS_REGIONS") {
		t.Fatalf("checkDiscovery = %v, want exit 1 (nothing gathered) with the scope hint", err)
	}
}

func TestDiscoverFleetTargets_CancelledContextIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	list := func(c context.Context, _ aws.Config) ([]string, error) {
		cancel()
		return nil, c.Err()
	}
	d, err := discoverFleetTargets(ctx, aws.Config{}, []string{"us-east-1", "us-west-2"}, true, list)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d.targets != nil || d.failed != nil || d.skipped != nil {
		t.Errorf("want no partial result, got %+v", d)
	}
}

func TestDiscoveryStopError(t *testing.T) {
	// The --wait-timeout bound on discovery gathered nothing: exit 1, and the
	// message names the timeout.
	err := discoveryStopError(context.Background(), fmt.Errorf("fleet discovery stopped: %w", context.DeadlineExceeded), 0)
	if err == nil || runner.ExitCodeOf(err) != 1 || !strings.Contains(err.Error(), "--wait-timeout") {
		t.Errorf("deadline: exit = %d, want 1 naming --wait-timeout (err %v)", runner.ExitCodeOf(err), err)
	}
	// A user interrupt passes through unchanged.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := fmt.Errorf("fleet discovery stopped: %w", context.Canceled)
	if got := discoveryStopError(ctx, in, 0); !errors.Is(got, context.Canceled) {
		t.Errorf("interrupt: got %v", got)
	}
}

// checkDiscovery fails only when no region could be listed. It prints the
// skipped-region notice, but not the failed regions: the run reports them
// once, with its other failures.
func TestCheckDiscovery(t *testing.T) {
	buf := captureFleetStderr(t)

	denied := []diag.Failure{diag.New(diag.KindRegion, "eu-west-1", diag.ReasonAccessDenied, "denied")}
	three := make([]clusterTarget, 3)
	cases := []struct {
		name    string
		regions int
		d       fleetDiscovery
		want    int
	}{
		{"clean", 2, fleetDiscovery{targets: three}, 0},
		{"clean but empty", 2, fleetDiscovery{}, 0},
		{"all regions failed", 1, fleetDiscovery{failed: denied}, 1},
		{"some failed, none found elsewhere", 2, fleetDiscovery{failed: denied}, 0},
		{"some failed, clusters found elsewhere", 2, fleetDiscovery{targets: three, failed: denied}, 0},
		{"skipped plus failed covers every region", 2, fleetDiscovery{failed: denied, skipped: []string{"sa-east-1"}}, 1},
		{"skipped only, reachable region empty", 2, fleetDiscovery{skipped: []string{"sa-east-1"}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			if got := runner.ExitCodeOf(checkDiscovery(tc.regions, tc.d)); got != tc.want {
				t.Errorf("exit = %d, want %d", got, tc.want)
			}
			if strings.Contains(buf.String(), "eu-west-1") {
				t.Errorf("checkDiscovery printed a failed region; the run reports it once: %q", buf.String())
			}
		})
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

// The fleet batch confirmation is a prompt: it goes to stderr, so stdout
// stays clean.
func TestPromptYesNo_WritesToStderr(t *testing.T) {
	dir := t.TempDir()
	stdinPath := filepath.Join(dir, "stdin")
	if err := os.WriteFile(stdinPath, []byte("y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	open := func(name string, create bool) *os.File {
		t.Helper()
		var f *os.File
		var err error
		if create {
			f, err = os.Create(filepath.Join(dir, name))
		} else {
			f, err = os.Open(filepath.Join(dir, name))
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	stdin, stdout, stderr := open("stdin", false), open("stdout", true), open("stderr", true)
	origIn, origOut, origErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = stdin, stdout, stderr
	restore := func() { os.Stdin, os.Stdout, os.Stderr = origIn, origOut, origErr }
	t.Cleanup(restore)

	ok := promptYesNo(context.Background(), "Update the fleet?")
	restore()
	if !ok {
		t.Error("promptYesNo = false, want true for \"y\"")
	}
	if out, _ := os.ReadFile(stdout.Name()); len(out) != 0 {
		t.Errorf("stdout = %q, want empty", out)
	}
	if out, _ := os.ReadFile(stderr.Name()); !strings.Contains(string(out), "Update the fleet? [y/N]") {
		t.Errorf("stderr = %q, want the prompt", out)
	}
}
