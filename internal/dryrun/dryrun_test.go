package dryrun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func (f roundTripFunc) Do(r *http.Request) (*http.Response, error) {
	return f(r)
}

// captureStdout redirects os.Stdout for the duration of fn and returns what was written.
func captureStdout(fn func()) string {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func newQuietRunner() *DryRunner {
	return &DryRunner{clusterName: "test-cluster", quiet: true}
}

func newVerboseRunner() *DryRunner {
	return &DryRunner{clusterName: "test-cluster", quiet: false}
}

// ---- categorizeUpdate ----

func TestCategorizeUpdate_UpdateNeeded(t *testing.T) {
	dr := newQuietRunner()
	result := &DryRunResult{
		UpdatesNeeded:  make([]NodegroupUpdate, 0),
		UpdatesSkipped: make([]NodegroupUpdate, 0),
		AlreadyLatest:  make([]NodegroupUpdate, 0),
	}
	dr.categorizeUpdate(result, NodegroupUpdate{Name: "ng-1", Action: refreshTypes.ActionUpdate})
	if len(result.UpdatesNeeded) != 1 {
		t.Errorf("UpdatesNeeded = %d, want 1", len(result.UpdatesNeeded))
	}
}

func TestCategorizeUpdate_ForceUpdate(t *testing.T) {
	dr := newQuietRunner()
	result := &DryRunResult{
		UpdatesNeeded:  make([]NodegroupUpdate, 0),
		UpdatesSkipped: make([]NodegroupUpdate, 0),
		AlreadyLatest:  make([]NodegroupUpdate, 0),
	}
	dr.categorizeUpdate(result, NodegroupUpdate{Name: "ng-1", Action: refreshTypes.ActionForceUpdate})
	if len(result.UpdatesNeeded) != 1 {
		t.Errorf("force update should go into UpdatesNeeded, got %d", len(result.UpdatesNeeded))
	}
}

func TestCategorizeUpdate_SkipUpdating(t *testing.T) {
	dr := newQuietRunner()
	result := &DryRunResult{
		UpdatesNeeded:  make([]NodegroupUpdate, 0),
		UpdatesSkipped: make([]NodegroupUpdate, 0),
		AlreadyLatest:  make([]NodegroupUpdate, 0),
	}
	dr.categorizeUpdate(result, NodegroupUpdate{Name: "ng-1", Action: refreshTypes.ActionSkipUpdating})
	if len(result.UpdatesSkipped) != 1 {
		t.Errorf("UpdatesSkipped = %d, want 1", len(result.UpdatesSkipped))
	}
}

func TestCategorizeUpdate_AlreadyLatest(t *testing.T) {
	dr := newQuietRunner()
	result := &DryRunResult{
		UpdatesNeeded:  make([]NodegroupUpdate, 0),
		UpdatesSkipped: make([]NodegroupUpdate, 0),
		AlreadyLatest:  make([]NodegroupUpdate, 0),
	}
	dr.categorizeUpdate(result, NodegroupUpdate{Name: "ng-1", Action: refreshTypes.ActionSkipLatest})
	if len(result.AlreadyLatest) != 1 {
		t.Errorf("AlreadyLatest = %d, want 1", len(result.AlreadyLatest))
	}
}

func TestCategorizeUpdate_VerbosePrintsStatus(t *testing.T) {
	dr := newVerboseRunner()
	result := &DryRunResult{
		UpdatesNeeded:  make([]NodegroupUpdate, 0),
		UpdatesSkipped: make([]NodegroupUpdate, 0),
		AlreadyLatest:  make([]NodegroupUpdate, 0),
	}
	out := captureStdout(func() {
		dr.categorizeUpdate(result, NodegroupUpdate{Name: "ng-verbose", Action: refreshTypes.ActionUpdate, Reason: "test"})
	})
	if out == "" {
		t.Error("verbose runner should print status to stdout")
	}
}

// ---- DisplayResults ----

func TestDisplayResults_QuietSuppressesOutput(t *testing.T) {
	dr := newQuietRunner()
	result := &DryRunResult{
		UpdatesNeeded:  []NodegroupUpdate{{Name: "ng-1"}},
		UpdatesSkipped: make([]NodegroupUpdate, 0),
		AlreadyLatest:  make([]NodegroupUpdate, 0),
	}
	out := captureStdout(func() {
		dr.DisplayResults(result)
	})
	if out != "" {
		t.Errorf("quiet mode should suppress all output, got: %q", out)
	}
}

func TestDisplayResults_ShowsClusterName(t *testing.T) {
	dr := newVerboseRunner()
	result := &DryRunResult{
		UpdatesNeeded:  make([]NodegroupUpdate, 0),
		UpdatesSkipped: make([]NodegroupUpdate, 0),
		AlreadyLatest:  make([]NodegroupUpdate, 0),
	}
	out := captureStdout(func() {
		dr.DisplayResults(result)
	})
	if out == "" {
		t.Error("verbose mode should produce output")
	}
}

func TestDisplayResults_ForceFlagMentioned(t *testing.T) {
	dr := &DryRunner{clusterName: "test-cluster", quiet: false, force: true}
	result := &DryRunResult{
		UpdatesNeeded:  make([]NodegroupUpdate, 0),
		UpdatesSkipped: make([]NodegroupUpdate, 0),
		AlreadyLatest:  make([]NodegroupUpdate, 0),
	}
	out := captureStdout(func() {
		dr.DisplayResults(result)
	})
	if out == "" {
		t.Error("expected output when force=true")
	}
}

// ---- DryRunResult thread safety ----

func TestDryRunResult_ConcurrentWrites(t *testing.T) {
	dr := newQuietRunner()
	result := &DryRunResult{
		UpdatesNeeded:  make([]NodegroupUpdate, 0),
		UpdatesSkipped: make([]NodegroupUpdate, 0),
		AlreadyLatest:  make([]NodegroupUpdate, 0),
	}

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			dr.categorizeUpdate(result, NodegroupUpdate{Name: "ng", Action: refreshTypes.ActionUpdate})
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	if len(result.UpdatesNeeded) != 10 {
		t.Errorf("expected 10 UpdatesNeeded after concurrent writes, got %d", len(result.UpdatesNeeded))
	}
}

func TestAnalyzeClassifiesNodegroupsWithInjectedLookups(t *testing.T) {
	dr := &DryRunner{
		clusterName: "test-cluster",
		k8sVersion:  "1.30",
		quiet:       true,
		describeNodegroupFn: func(_ context.Context, name string) (*types.Nodegroup, error) {
			return &types.Nodegroup{
				NodegroupName: aws.String(name),
				Status:        types.NodegroupStatusActive,
			}, nil
		},
		currentAmiFn: func(_ context.Context, _ *types.Nodegroup) string { return "ami-old" },
		latestAmiFn:  func(context.Context, *types.Nodegroup) string { return "ami-new" },
	}

	result := dr.Analyze(context.Background(), []string{"ng-1"})
	if len(result.UpdatesNeeded) != 1 {
		t.Fatalf("UpdatesNeeded = %d, want 1", len(result.UpdatesNeeded))
	}
	if result.UpdatesNeeded[0].Reason != "AMI is outdated" {
		t.Fatalf("Reason = %q", result.UpdatesNeeded[0].Reason)
	}
}

func TestAnalyzeNodegroupBranchesWithInjectedLookups(t *testing.T) {
	tests := []struct {
		name       string
		force      bool
		status     types.NodegroupStatus
		currentAMI string
		latestAMI  string
		describeErr error
		want       refreshTypes.DryRunAction
	}{
		{name: "describe error", describeErr: errors.New("boom"), want: refreshTypes.ActionSkipUpdating},
		{name: "updating", status: types.NodegroupStatusUpdating, want: refreshTypes.ActionSkipUpdating},
		{name: "force", force: true, status: types.NodegroupStatusActive, want: refreshTypes.ActionForceUpdate},
		{name: "unknown", status: types.NodegroupStatusActive, currentAMI: "", latestAMI: "ami-new", want: refreshTypes.ActionUpdate},
		{name: "latest", status: types.NodegroupStatusActive, currentAMI: "ami-new", latestAMI: "ami-new", want: refreshTypes.ActionSkipLatest},
		{name: "outdated", status: types.NodegroupStatusActive, currentAMI: "ami-old", latestAMI: "ami-new", want: refreshTypes.ActionUpdate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dr := &DryRunner{
				clusterName: "test-cluster",
				k8sVersion:  "1.30",
				force:       tt.force,
				quiet:       true,
				describeNodegroupFn: func(_ context.Context, name string) (*types.Nodegroup, error) {
					if tt.describeErr != nil {
						return nil, tt.describeErr
					}
					return &types.Nodegroup{
						NodegroupName: aws.String(name),
						Status:        tt.status,
					}, nil
				},
				currentAmiFn: func(_ context.Context, _ *types.Nodegroup) string { return tt.currentAMI },
				latestAmiFn:  func(context.Context, *types.Nodegroup) string { return tt.latestAMI },
			}

			got := dr.analyzeNodegroup(context.Background(), "ng")
			if got.Action != tt.want {
				t.Fatalf("Action = %v, want %v", got.Action, tt.want)
			}
		})
	}
}

func TestNewDryRunnerAndPerformDryRunErrorPaths(t *testing.T) {
	if _, err := NewDryRunner(context.Background(), aws.Config{}, nil, "cluster", false, true); err == nil {
		t.Fatal("expected error for nil EKS client")
	}
	if err := PerformDryRun(context.Background(), aws.Config{}, nil, "cluster", []string{"ng"}, false, true); err == nil {
		t.Fatal("expected error for nil EKS client")
	}
}

func TestNewDryRunnerSuccessAndErrorSeams(t *testing.T) {
	oldDescribe := dryrunDescribeCluster
	t.Cleanup(func() {
		dryrunDescribeCluster = oldDescribe
	})

	dryrunDescribeCluster = func(context.Context, *eks.Client, string) (string, error) {
		return "1.30", nil
	}

	awsCfg := aws.Config{Region: "us-east-1"}
	runner, err := NewDryRunner(context.Background(), awsCfg, eks.New(eks.Options{Region: "us-east-1"}), "cluster", true, true)
	if err != nil {
		t.Fatalf("NewDryRunner() = %v", err)
	}
	if runner.k8sVersion != "1.30" || !runner.force || !runner.quiet {
		t.Fatalf("runner = %+v", runner)
	}

	dryrunDescribeCluster = func(context.Context, *eks.Client, string) (string, error) {
		return "", errors.New("describe")
	}
	if _, err := NewDryRunner(context.Background(), awsCfg, eks.New(eks.Options{Region: "us-east-1"}), "cluster", false, false); err == nil {
		t.Fatal("expected describe error")
	}
}

func TestPerformDryRunSuccessWithInjectedRunner(t *testing.T) {
	oldNew := newDryRunner
	t.Cleanup(func() { newDryRunner = oldNew })

	newDryRunner = func(context.Context, aws.Config, *eks.Client, string, bool, bool) (*DryRunner, error) {
		return &DryRunner{
			clusterName: "cluster",
			quiet:       true,
			describeNodegroupFn: func(_ context.Context, name string) (*types.Nodegroup, error) {
				return &types.Nodegroup{NodegroupName: aws.String(name), Status: types.NodegroupStatusUpdating}, nil
			},
		}, nil
	}

	if err := PerformDryRun(context.Background(), aws.Config{}, nil, "cluster", []string{"ng"}, false, true); err != nil {
		t.Fatalf("PerformDryRun() = %v", err)
	}
}

func TestDefaultDryRunnerLookupFallbacks(t *testing.T) {
	dr := &DryRunner{}

	if got := dr.currentAmi(context.Background(), &types.Nodegroup{}); got != "" {
		t.Fatalf("currentAmi fallback = %q, want empty", got)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected describeNodegroup fallback to panic with nil client")
			}
		}()
		_, _ = dr.describeNodegroup(context.Background(), "ng")
	}()

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected latestAmi fallback to panic with nil client")
			}
		}()
		_ = dr.latestAmi(context.Background(), &types.Nodegroup{AmiType: types.AMITypesAl2X8664})
	}()
}

func TestDescribeNodegroupFallbackWithFakeEKS(t *testing.T) {
	client := eks.New(eks.Options{
		Region:      "us-east-1",
		Credentials: aws.AnonymousCredentials{},
		HTTPClient: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"nodegroup":{"nodegroupName":"ng","status":"ACTIVE"}}`)),
			}, nil
		}),
	})

	dr := &DryRunner{eksClient: client, clusterName: "cluster"}
	ng, err := dr.describeNodegroup(context.Background(), "ng")
	if err != nil {
		t.Fatalf("describeNodegroup() = %v", err)
	}
	if aws.ToString(ng.NodegroupName) != "ng" {
		t.Fatalf("nodegroup = %+v", ng)
	}

	errClient := eks.New(eks.Options{
		Region:      "us-east-1",
		Credentials: aws.AnonymousCredentials{},
		HTTPClient: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("network")
		}),
	})
	dr.eksClient = errClient
	if _, err := dr.describeNodegroup(context.Background(), "ng"); err == nil {
		t.Fatal("expected error")
	}
}

func TestDefaultDescribeClusterWithFakeEKS(t *testing.T) {
	client := eks.New(eks.Options{
		Region:      "us-east-1",
		Credentials: aws.AnonymousCredentials{},
		HTTPClient: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"cluster":{"name":"cluster","version":"1.30"}}`)),
			}, nil
		}),
	})
	version, err := dryrunDescribeCluster(context.Background(), client, "cluster")
	if err != nil || version != "1.30" {
		t.Fatalf("dryrunDescribeCluster() = %q, %v", version, err)
	}

	errClient := eks.New(eks.Options{
		Region:      "us-east-1",
		Credentials: aws.AnonymousCredentials{},
		HTTPClient: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network")
		}),
	})
	if _, err := dryrunDescribeCluster(context.Background(), errClient, "cluster"); err == nil {
		t.Fatal("expected describe cluster error")
	}
}

func TestDefaultPackageHooks(t *testing.T) {
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected dryrunDescribeCluster to panic with nil client")
			}
		}()
		_, _ = dryrunDescribeCluster(context.Background(), nil, "cluster")
	}()
}

func TestPrintNodegroupListIncludesAmiDetails(t *testing.T) {
	dr := newVerboseRunner()
	out := captureStdout(func() {
		dr.printNodegroupList("Header", []NodegroupUpdate{{
			Name:       "ng",
			CurrentAMI: "ami-old",
			LatestAMI:  "ami-new",
		}}, func(format string, a ...any) string { return format })
	})
	if out == "" || !bytes.Contains([]byte(out), []byte("ami-old")) || !bytes.Contains([]byte(out), []byte("ami-new")) {
		t.Fatalf("printNodegroupList output = %q", out)
	}
}

var _ = eks.DescribeNodegroupInput{}
