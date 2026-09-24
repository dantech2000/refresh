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

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// An add-on or nodegroup that cannot be described is a failure on the
// details, for the command layer to report. It used to be a Warn log line
// only, so the add-on was just missing from the output.
func TestDescribe_PartialFailuresBecomeFailures(t *testing.T) {
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
	svc := &ServiceImpl{eksClient: mock, cache: NewCache(time.Minute), logger: quietLogger(), awsConfig: aws.Config{Region: "us-east-1"}}

	details, err := svc.Describe(context.Background(), "prod", DescribeOptions{IncludeAddons: true, Detailed: true})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(details.AddonList()) != 1 || details.AddonList()[0].Name != "coredns" {
		t.Errorf("addons = %+v, want coredns only", details.AddonList())
	}
	want := []struct {
		kind       diag.Kind
		name, op   string
		clusterKey string
	}{
		{diag.KindAddon, "vpc-cni", diag.OpDescribeAddon, "prod"},
		{diag.KindCluster, "prod", diag.OpListNodegroups, ""},
	}
	if len(details.Failures) != len(want) {
		t.Fatalf("failures = %+v, want %d", details.Failures, len(want))
	}
	for i, w := range want {
		f := details.Failures[i]
		if f.Kind != w.kind || f.Name != w.name || f.Operation != w.op || f.Cluster != w.clusterKey ||
			f.Region != "us-east-1" || f.Reason != diag.ReasonAccessDenied || f.AWSErrorCode != "AccessDeniedException" {
			t.Errorf("failure %d = %+v, want %s %s (%s) AccessDenied", i, f, w.kind, w.name, w.op)
		}
		if strings.Contains(f.Error, "\n") {
			t.Errorf("failure error is not one line: %q", f.Error)
		}
	}

	// The machine document leaves out a failed collection (never [], which
	// would claim "no nodegroups") and carries the failure in failures.
	b, err := json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, want := range []string{`"failures":[{`, `"operation":"eks:ListNodegroups"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("JSON lacks %s:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, `"nodegroups"`) {
		t.Errorf("JSON has nodegroups, which were not read:\n%s", doc)
	}
}

// Without --detailed, nodegroups were not collected: the key is left out,
// not []. Add-ons that were collected and are none are []. Lists and maps
// that were read and are empty are [] and {}, never null.
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
	for _, want := range []string{`"addons":[]`, `"failures":[]`, `"tags":{}`, `"subnetIds":[]`, `"securityGroupIds":[]`, `"loggingEnabled":[]`} {
		if !strings.Contains(doc, want) {
			t.Errorf("JSON lacks %s:\n%s", want, doc)
		}
	}
	for _, absent := range []string{`"nodegroups"`, `null`} {
		if strings.Contains(doc, absent) {
			t.Errorf("JSON has %s:\n%s", absent, doc)
		}
	}
}

// A cluster whose DescribeCluster fails still gets a row, marked incomplete
// and carrying the failure.
func TestList_DescribeFailureRowIsIncomplete(t *testing.T) {
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
	if len(rows) != 1 || rows[0].Status != "UNKNOWN" || !rows[0].Incomplete || len(rows[0].Failures) != 1 {
		t.Fatalf("rows = %+v, want one incomplete UNKNOWN row with one failure", rows)
	}
	if f := rows[0].Failures[0]; f.Kind != diag.KindCluster || f.Name != "prod" || f.Operation != diag.OpDescribeCluster || f.Reason != diag.ReasonAccessDenied {
		t.Errorf("failure = %+v, want the DescribeCluster AccessDenied of prod", f)
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
	if !errors.Is(err, cause) {
		t.Errorf("err = %v, want it to wrap %v", err, cause)
	}
	for _, f := range res.Failed[1:] {
		if f.Kind != diag.KindRegion || f.Reason != diag.ReasonNotAttempted || f.Error != "not queried: "+cause.Error() || f.Region != f.Name {
			t.Errorf("region failure = %+v, want NotAttempted, not queried: %v", f, cause)
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
	if f := res.Failed[0]; f.Kind != diag.KindRegion || f.Name != "eu-west-1" || f.Operation != diag.OpListClusters || f.Reason != diag.ReasonAccessDenied {
		t.Errorf("region failure = %+v, want an AccessDenied eks:ListClusters failure of eu-west-1", f)
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
