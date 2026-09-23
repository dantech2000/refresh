package status

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
)

var throttled = &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}

// flakyClusterAPI throttles the first call to each operation, then answers
// like fakeClusterAPI. ListClusters serves two pages.
type flakyClusterAPI struct {
	fakeClusterAPI
	listCalls, versionCalls int
}

func (f *flakyClusterAPI) ListClusters(_ context.Context, in *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	f.listCalls++
	if f.listCalls == 1 {
		return nil, throttled
	}
	if in.NextToken == nil {
		return &eks.ListClustersOutput{Clusters: []string{"a"}, NextToken: aws.String("p2")}, nil
	}
	return &eks.ListClustersOutput{Clusters: []string{"b"}}, nil
}

func (f *flakyClusterAPI) DescribeClusterVersions(ctx context.Context, in *eks.DescribeClusterVersionsInput, opts ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error) {
	f.versionCalls++
	if f.versionCalls == 1 {
		return nil, throttled
	}
	return f.fakeClusterAPI.DescribeClusterVersions(ctx, in, opts...)
}

// flakyEC2 throttles the first call to each operation.
type flakyEC2 struct {
	imageCalls, instanceCalls int
}

func (f *flakyEC2) DescribeImages(context.Context, *ec2.DescribeImagesInput, ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	f.imageCalls++
	if f.imageCalls == 1 {
		return nil, throttled
	}
	return &ec2.DescribeImagesOutput{Images: []ec2types.Image{{CreationDate: aws.String("2026-01-01T00:00:00Z")}}}, nil
}

func (f *flakyEC2) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.instanceCalls++
	if f.instanceCalls == 1 {
		return nil, throttled
	}
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{}}}}}, nil
}

func TestStatusAWSCalls_RetryThrottling(t *testing.T) {
	api := &flakyClusterAPI{fakeClusterAPI: fakeClusterAPI{versions: map[string]ekstypes.ClusterVersionInformation{
		"1.32": {ClusterVersion: aws.String("1.32"), EndOfStandardSupportDate: aws.Time(date(2027, 1, 1))},
	}}}
	ec := &flakyEC2{}
	svc := &Service{region: "us-east-1", clusterAPI: api, ec2: ec, now: func() time.Time { return date(2026, 6, 11) }}
	ctx := t.Context()

	names, err := svc.listClusterNames(ctx)
	if err != nil || len(names) != 2 {
		t.Errorf("listClusterNames = %v, %v; want [a b] after a retried first page", names, err)
	}
	if _, _, ok := supportDatesFromAPI(ctx, api, "1.32"); !ok || api.versionCalls != 2 {
		t.Errorf("supportDatesFromAPI ok=%v calls=%d; want a retried success", ok, api.versionCalls)
	}
	if days := svc.amiOldestDays(ctx, []string{"ami-1"}); days == nil || ec.imageCalls != 2 {
		t.Errorf("amiOldestDays = %v, calls=%d; want a retried success", days, ec.imageCalls)
	}
	if !svc.hasKarpenterInstances(ctx, "a") || ec.instanceCalls != 2 {
		t.Errorf("hasKarpenterInstances calls=%d; want a retried true", ec.instanceCalls)
	}
}

// taggedEC2 answers DescribeInstances by evaluating the request's filters
// against a fixed set of instance tag maps, the way EC2 does: filters AND
// together, a filter's values OR together.
type taggedEC2 struct {
	instances []map[string]string
	filters   [][]ec2types.Filter
}

func (f *taggedEC2) DescribeImages(context.Context, *ec2.DescribeImagesInput, ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	return &ec2.DescribeImagesOutput{}, nil
}

func (f *taggedEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.filters = append(f.filters, in.Filters)
	var matched []ec2types.Instance
	for _, tags := range f.instances {
		if matchesAll(tags, in.Filters) {
			matched = append(matched, ec2types.Instance{})
		}
	}
	if len(matched) == 0 {
		return &ec2.DescribeInstancesOutput{}, nil
	}
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: matched}}}, nil
}

func matchesAll(tags map[string]string, filters []ec2types.Filter) bool {
	for _, f := range filters {
		name := aws.ToString(f.Name)
		ok := false
		for _, v := range f.Values {
			switch {
			case name == "instance-state-name":
				ok = ok || v == "running"
			case name == "tag-key":
				_, has := tags[v]
				ok = ok || has
			case strings.HasPrefix(name, "tag:"):
				val, has := tags[strings.TrimPrefix(name, "tag:")]
				ok = ok || (has && val == v)
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// Karpenter detection is scoped to the cluster: another cluster's Karpenter
// nodes in the same region must not mark this one as Karpenter-managed.
func TestHasKarpenterInstances_ScopedToCluster(t *testing.T) {
	other := map[string]string{"karpenter.sh/nodepool": "default", "kubernetes.io/cluster/other": "owned", "eks:eks-cluster-name": "other"}
	current := map[string]string{"karpenter.sh/nodepool": "default", "kubernetes.io/cluster/prod": "owned", "eks:eks-cluster-name": "prod"}
	legacy := map[string]string{"karpenter.sh/provisioner-name": "default", "kubernetes.io/cluster/legacy": "owned"}
	eksTagOnly := map[string]string{"karpenter.sh/nodepool": "default", "eks:eks-cluster-name": "tagged"}
	nonKarpenter := map[string]string{"kubernetes.io/cluster/plain": "owned", "eks:eks-cluster-name": "plain"}

	ec := &taggedEC2{instances: []map[string]string{other, current, legacy, eksTagOnly, nonKarpenter}}
	svc := &Service{region: "us-east-1", ec2: ec}
	ctx := t.Context()

	for cluster, want := range map[string]bool{
		"prod":    true,  // current Karpenter tags
		"legacy":  true,  // legacy provisioner tag, kubernetes.io/cluster only
		"tagged":  true,  // eks:eks-cluster-name only
		"plain":   false, // cluster instances, but not Karpenter's
		"nothing": false, // Karpenter runs in the region, for other clusters
		"":        false,
	} {
		if got := svc.hasKarpenterInstances(ctx, cluster); got != want {
			t.Errorf("hasKarpenterInstances(%q) = %v, want %v", cluster, got, want)
		}
	}

	// Every query carries a cluster-scoping filter.
	for _, fs := range ec.filters {
		scoped := false
		for _, f := range fs {
			if n := aws.ToString(f.Name); n == "tag:eks:eks-cluster-name" || strings.HasPrefix(n, "tag:kubernetes.io/cluster/") {
				scoped = true
			}
		}
		if !scoped {
			t.Errorf("DescribeInstances filters %v are not scoped to a cluster", fs)
		}
	}
}
