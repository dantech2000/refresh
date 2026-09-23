package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// An add-on or nodegroup that cannot be described is a warning on the
// details, for the command layer to report. It used to be a Warn log line
// only, so the add-on was just missing from the output.
func TestDescribe_PartialFailuresBecomeWarnings(t *testing.T) {
	mock := &mocks.EKSAPI{
		DescribeClusterFn: func(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
			return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{Name: in.Name, Version: aws.String("1.32")}}, nil
		},
		ListAddonsFn: func(context.Context, *eks.ListAddonsInput, ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
			return &eks.ListAddonsOutput{Addons: []string{"coredns", "vpc-cni"}}, nil
		},
		DescribeAddonFn: func(_ context.Context, in *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
			if aws.ToString(in.AddonName) == "vpc-cni" {
				return nil, mocks.AccessDenied()
			}
			return &eks.DescribeAddonOutput{Addon: &ekstypes.Addon{AddonName: in.AddonName, AddonVersion: aws.String("v1")}}, nil
		},
		ListNodegroupsFn: func(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return nil, mocks.AccessDenied()
		},
	}
	svc := &ServiceImpl{eksClient: mock, cache: NewCache(time.Minute), logger: quietLogger()}

	details, err := svc.Describe(context.Background(), "prod", DescribeOptions{IncludeAddons: true, Detailed: true})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(details.Addons) != 1 || details.Addons[0].Name != "coredns" {
		t.Errorf("addons = %+v, want coredns only", details.Addons)
	}
	joined := strings.Join(details.Warnings, "\n")
	for _, want := range []string{"add-on vpc-cni: AccessDeniedException", "could not list nodegroups: AccessDeniedException"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings lack %q:\n%s", want, joined)
		}
	}
	for _, w := range details.Warnings {
		if strings.Contains(w, "\n") {
			t.Errorf("warning is not one line: %q", w)
		}
	}

	// The machine document keeps a failed collection as null (never [],
	// which would claim "no nodegroups") and carries the failure in warnings.
	b, err := json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, want := range []string{`"nodegroups":null`, `"warnings":[`, `could not list nodegroups: AccessDeniedException`} {
		if !strings.Contains(doc, want) {
			t.Errorf("JSON lacks %s:\n%s", want, doc)
		}
	}
}

// Without --detailed, nodegroups were not collected: null, not []. Add-ons
// that were collected and are none are [].
func TestDescribe_NotCollectedIsNullCollectedEmptyIsEmptyList(t *testing.T) {
	mock := &mocks.EKSAPI{
		DescribeClusterFn: func(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
			return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{Name: in.Name, Version: aws.String("1.32")}}, nil
		},
		ListAddonsFn: func(context.Context, *eks.ListAddonsInput, ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
			return &eks.ListAddonsOutput{}, nil
		},
		ListNodegroupsFn: func(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			t.Error("nodegroups listed without --detailed")
			return &eks.ListNodegroupsOutput{}, nil
		},
	}
	svc := &ServiceImpl{eksClient: mock, cache: NewCache(time.Minute), logger: quietLogger()}
	details, err := svc.Describe(context.Background(), "prod", DescribeOptions{IncludeAddons: true})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	b, err := json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, want := range []string{`"nodegroups":null`, `"addons":[]`} {
		if !strings.Contains(doc, want) {
			t.Errorf("JSON lacks %s:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, `"warnings"`) {
		t.Errorf("no failures, but JSON has warnings:\n%s", doc)
	}
}

// A cluster whose DescribeCluster fails still gets a row, with a warning.
func TestList_DescribeFailureRowHasWarning(t *testing.T) {
	mock := &mocks.EKSAPI{
		ListClustersFn: func(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
			return &eks.ListClustersOutput{Clusters: []string{"prod"}}, nil
		},
		DescribeClusterFn: func(context.Context, *eks.DescribeClusterInput, ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
			return nil, mocks.AccessDenied()
		},
	}
	svc := &ServiceImpl{eksClient: mock, cache: NewCache(time.Minute), logger: quietLogger()}
	rows, err := svc.List(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != "UNKNOWN" || len(rows[0].Warnings) != 1 ||
		!strings.Contains(rows[0].Warnings[0], "could not describe cluster") {
		t.Errorf("rows = %+v, want one UNKNOWN row with a describe warning", rows)
	}
}

// When the context ends while regions wait for a slot, those regions count as
// failed with the context's cause, and a sweep where no region answered is an
// error (it used to return no clusters and no error).
func TestListAllRegions_UndispatchedRegionsFail(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("deadline for the sweep")
	var calls atomic.Int32
	svc := &ServiceImpl{logger: quietLogger(), regionLister: func(rctx context.Context, _ string, _ ListOptions) ([]ClusterSummary, error) {
		calls.Add(1)
		cancel(cause)
		// Hold the only slot a little after the cancel, so the dispatcher
		// sees only the ended context and starts no other region.
		time.Sleep(20 * time.Millisecond)
		return nil, context.Cause(rctx)
	}}

	res, err := svc.ListAllRegions(ctx, ListOptions{Regions: []string{"us-east-1", "us-west-2", "eu-west-1"}, MaxConcurrency: 1})
	if err == nil {
		t.Fatalf("ListAllRegions = %+v, nil; want an error", res)
	}
	if calls.Load() != 1 {
		t.Errorf("regions started = %d, want 1 (-C 1, then cancelled)", calls.Load())
	}
	if len(res.Failed) != 3 || res.Queried != 0 {
		t.Fatalf("failed = %d, queried = %d; want 3 failed, 0 queried", len(res.Failed), res.Queried)
	}
	for _, f := range res.Failed[1:] {
		if !errors.Is(f.Err, cause) || !strings.Contains(f.Err.Error(), "not queried") {
			t.Errorf("region %s err = %v, want not queried: %v", f.Region, f.Err, cause)
		}
	}
}

// Partial failure is not an error: the result carries the failed regions.
// Only the default sweep skips regions closed to these credentials.
func TestListAllRegions_PartialAndSkipped(t *testing.T) {
	t.Setenv("REFRESH_EKS_REGIONS", "")
	lister := func(_ context.Context, region string, _ ListOptions) ([]ClusterSummary, error) {
		if region == "us-east-1" {
			return []ClusterSummary{{Name: "prod"}}, nil
		}
		return nil, mocks.AccessDenied()
	}
	svc := &ServiceImpl{logger: quietLogger(), regionLister: lister, awsConfig: aws.Config{Region: "us-east-1"}}

	// Named regions: the denied one is a failure.
	res, err := svc.ListAllRegions(context.Background(), ListOptions{Regions: []string{"us-east-1", "eu-west-1"}})
	if err != nil {
		t.Fatalf("partial failure returned an error: %v", err)
	}
	if res.Queried != 1 || len(res.Failed) != 1 || res.Failed[0].Region != "eu-west-1" || len(res.Skipped) != 0 {
		t.Errorf("named sweep = %+v, want eu-west-1 failed", res)
	}
	if len(res.Summaries) != 1 || res.Summaries[0].Region != "us-east-1" {
		t.Errorf("summaries = %+v, want prod stamped us-east-1", res.Summaries)
	}

	// Default sweep: denied regions are skipped, not failed.
	res, err = svc.ListAllRegions(context.Background(), ListOptions{AllRegions: true})
	if err != nil {
		t.Fatalf("default sweep: %v", err)
	}
	if len(res.Failed) != 0 || len(res.Skipped) != res.Regions-1 {
		t.Errorf("default sweep: failed %d, skipped %d of %d; want every other region skipped", len(res.Failed), len(res.Skipped), res.Regions)
	}
}
