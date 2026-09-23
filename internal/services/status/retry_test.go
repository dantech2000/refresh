package status

import (
	"context"
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
	if !svc.hasKarpenterInstances(ctx) || ec.instanceCalls != 2 {
		t.Errorf("hasKarpenterInstances calls=%d; want a retried true", ec.instanceCalls)
	}
}
