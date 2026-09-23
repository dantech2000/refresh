package status

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
	"github.com/dantech2000/refresh/internal/services/addons"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func listClusters(names ...string) func(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	return func(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
		return &eks.ListClustersOutput{Clusters: names}, nil
	}
}

// A cluster whose DescribeCluster fails must come back flagged Incomplete, so
// the view and exit code never treat it as current.
func TestListClusterStatuses_DescribeErrorMarksIncomplete(t *testing.T) {
	api := mocks.NewEKSAPI().Build()
	api.ListClustersFn = listClusters("ghost")
	api.DescribeClusterFn = func(context.Context, *eks.DescribeClusterInput, ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
		return nil, &ekstypes.ResourceNotFoundException{Message: aws.String("gone")}
	}
	svc := newTestService(nil, &fakeNodegroups{}, &fakeAddons{})
	svc.clusterAPI = api

	statuses, err := svc.ListClusterStatuses(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("a single bad cluster must not fail the sweep: %v", err)
	}
	if len(statuses) != 1 || !statuses[0].Incomplete() {
		t.Fatalf("want one incomplete row, got %+v", statuses)
	}
	if statuses[0].NeedsAttention() {
		t.Error("an undescribable cluster must not be reported as stale")
	}
}

// When the sweep context is cancelled mid-flight, every cluster still gets a
// named, region-tagged row with an error, and the cancellation is returned.
func TestListClusterStatuses_CancelledSweep(t *testing.T) {
	const n = 50
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("c%02d", i)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	api := mocks.NewEKSAPI().WithCluster("any", "1.32").Build()
	api.ListClustersFn = func(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
		cancel() // the sweep times out right after listing
		return &eks.ListClustersOutput{Clusters: names}, nil
	}
	svc := newTestService(nil, &fakeNodegroups{}, &fakeAddons{})
	svc.clusterAPI = api

	statuses, err := svc.ListClusterStatuses(ctx, ListOptions{MaxConcurrency: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(statuses) != n {
		t.Fatalf("got %d rows, want %d", len(statuses), n)
	}
	notEvaluated := 0
	for i, c := range statuses {
		if c.Name != names[i] || c.Region != "us-east-1" {
			t.Errorf("row %d = %q/%q, want %q/us-east-1", i, c.Name, c.Region, names[i])
		}
		if !c.Incomplete() {
			t.Errorf("row %s has no error after cancellation", c.Name)
		}
		for _, e := range c.Errors {
			if strings.HasPrefix(e, "not evaluated: ") {
				notEvaluated++
			}
		}
	}
	// With 50 clusters and concurrency 1, ForEachParallel stops dispatching
	// long before the end once ctx is done.
	if notEvaluated == 0 {
		t.Error(`expected undispatched clusters to be marked "not evaluated"`)
	}
}

// A failing DescribeAddon makes addons.List return an UNKNOWN addon with no
// version. It must not be counted as behind; it must be recorded as an error.
func TestAssembleCluster_DescribeAddonFailure(t *testing.T) {
	api := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithAddon("coredns", "v1.11.4", ekstypes.AddonStatusActive).
		WithAddonVersions("coredns", []string{"v1.11.4"}, "1.32").
		WithAddonVersions("vpc-cni", []string{"v1.18.1"}, "1.32").
		Build()
	api.ListClustersFn = listClusters("prod")
	listAddons := api.ListAddonsFn
	api.ListAddonsFn = func(ctx context.Context, in *eks.ListAddonsInput, opts ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
		out, err := listAddons(ctx, in, opts...)
		if err != nil {
			return nil, err
		}
		out.Addons = append(out.Addons, "vpc-cni")
		return out, nil
	}
	describeAddon := api.DescribeAddonFn
	api.DescribeAddonFn = func(ctx context.Context, in *eks.DescribeAddonInput, opts ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
		if aws.ToString(in.AddonName) == "vpc-cni" {
			return nil, &ekstypes.InvalidRequestException{Message: aws.String("access denied")}
		}
		return describeAddon(ctx, in, opts...)
	}

	svc := newTestService(nil, &fakeNodegroups{}, nil)
	svc.clusterAPI = api
	svc.addons = addons.NewService(api, discardLogger())

	statuses, err := svc.ListClusterStatuses(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := statuses[0]
	if c.AddonsBehind.Behind != 0 {
		t.Errorf("addons behind = %d (%v), want 0: an unreadable addon is not stale", c.AddonsBehind.Behind, c.AddonsBehind.Names)
	}
	if c.AddonsBehind.Total != 2 {
		t.Errorf("addons total = %d, want 2", c.AddonsBehind.Total)
	}
	if !c.Incomplete() || !strings.Contains(strings.Join(c.Errors, ";"), "vpc-cni") {
		t.Errorf("want an error naming vpc-cni, got %v", c.Errors)
	}
	if c.NeedsAttention() {
		t.Error("a failed DescribeAddon must not flag the cluster as stale")
	}
}
