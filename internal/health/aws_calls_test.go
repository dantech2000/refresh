package health

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	asgtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/mocks"
)

var throttle = &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}

// pagingASGAPI behaves like the real DescribeAutoScalingGroups: it returns at
// most MaxRecords groups per page (50 when unset) and a NextToken for the rest.
type pagingASGAPI struct {
	mu         sync.Mutex
	maxRecords []int32
}

func (f *pagingASGAPI) DescribeAutoScalingGroups(_ context.Context, in *autoscaling.DescribeAutoScalingGroupsInput, _ ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
	f.mu.Lock()
	f.maxRecords = append(f.maxRecords, aws.ToInt32(in.MaxRecords))
	f.mu.Unlock()

	pageSize := int(aws.ToInt32(in.MaxRecords))
	if pageSize == 0 {
		pageSize = 50
	}
	start := 0
	if in.NextToken != nil {
		start, _ = strconv.Atoi(*in.NextToken)
	}
	end := min(start+pageSize, len(in.AutoScalingGroupNames))
	out := &autoscaling.DescribeAutoScalingGroupsOutput{}
	for _, name := range in.AutoScalingGroupNames[start:end] {
		out.AutoScalingGroups = append(out.AutoScalingGroups, asgtypes.AutoScalingGroup{
			AutoScalingGroupName: aws.String(name),
			Instances:            []asgtypes.Instance{{InstanceId: aws.String("i-" + name)}},
		})
	}
	if end < len(in.AutoScalingGroupNames) {
		out.NextToken = aws.String(strconv.Itoa(end))
	}
	return out, nil
}

// Regression: a 100-name chunk without MaxRecords returned only the first 50
// groups, so on a >50-ASG cluster the rest silently vanished.
func TestInstanceIDsForASGs_KeepsEveryGroup(t *testing.T) {
	names := make([]string, 130)
	for i := range names {
		names[i] = fmt.Sprintf("asg-%03d", i)
	}
	api := &pagingASGAPI{}
	hc := &HealthChecker{asgClient: api}

	ids, err := hc.instanceIDsForASGs(context.Background(), names)
	if err != nil {
		t.Fatalf("instanceIDsForASGs: %v", err)
	}
	if len(ids) != len(names) {
		t.Fatalf("got %d instance IDs, want %d", len(ids), len(names))
	}
	for _, mr := range api.maxRecords {
		if mr != 100 {
			t.Errorf("MaxRecords = %d, want 100", mr)
		}
	}
}

// Nodegroup list and describe calls retry throttling, describe in parallel,
// and report a permanent failure on one line with its API code.
func TestCheckNodeHealth_RetriesAndFormatsAWSErrors(t *testing.T) {
	var mu sync.Mutex
	listCalls, describeA := 0, 0
	api := mocks.NewEKSAPI().Build()
	api.ListNodegroupsFn = func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		listCalls++
		if listCalls == 1 {
			return nil, throttle
		}
		return &eks.ListNodegroupsOutput{Nodegroups: []string{"ng-a", "ng-b"}}, nil
	}
	api.DescribeNodegroupFn = func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		if aws.ToString(in.NodegroupName) == "ng-b" {
			return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform eks:DescribeNodegroup"}
		}
		mu.Lock()
		describeA++
		first := describeA == 1
		mu.Unlock()
		if first {
			return nil, throttle
		}
		return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{
			Status:        ekstypes.NodegroupStatusActive,
			ScalingConfig: &ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(3)},
		}}, nil
	}
	hc := &HealthChecker{eksClient: api}

	r := hc.CheckNodeHealth(context.Background(), "prod")
	if r.Status == StatusFail {
		t.Fatalf("throttling should be retried, got %+v", r)
	}
	if listCalls != 2 || describeA != 2 {
		t.Errorf("ListNodegroups calls = %d, ng-a describes = %d, want 2 and 2", listCalls, describeA)
	}
	var denied string
	for _, d := range r.Details {
		if strings.Contains(d, "ng-b") && strings.Contains(d, "Failed") {
			denied = d
		}
	}
	if !strings.Contains(denied, "AccessDeniedException") || strings.Contains(denied, "\n") {
		t.Errorf("ng-b detail = %q, want a one-line AccessDeniedException summary", denied)
	}
}

// sequenceMetricsAPI returns errs in order, then out.
type sequenceMetricsAPI struct {
	errs  []error
	out   *cloudwatch.GetMetricDataOutput
	calls int
}

func (f *sequenceMetricsAPI) GetMetricData(_ context.Context, _ *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	f.calls++
	if f.calls <= len(f.errs) {
		return nil, f.errs[f.calls-1]
	}
	return f.out, nil
}

func TestFetchControlPlaneMetrics_RetriesThrottling(t *testing.T) {
	api := &sequenceMetricsAPI{errs: []error{throttle}, out: &cloudwatch.GetMetricDataOutput{
		MetricDataResults: []cwtypes.MetricDataResult{{Id: aws.String("etcd"), Values: []float64{1}}},
	}}
	m, err := fetchControlPlaneMetrics(context.Background(), api, "prod")
	if err != nil || !m.hasData || api.calls != 2 {
		t.Fatalf("err = %v, hasData = %v, calls = %d; want a retried success", err, m.hasData, api.calls)
	}
}

func TestServiceQuotaCalls_RetryAndSummarize(t *testing.T) {
	usage := &sequenceMetricsAPI{errs: []error{throttle}, out: &cloudwatch.GetMetricDataOutput{
		MetricDataResults: []cwtypes.MetricDataResult{{Id: aws.String("vcpu"), Values: []float64{10}}},
	}}
	if v, ok, err := onDemandVCPUUsage(context.Background(), usage); err != nil || !ok || v != 10 || usage.calls != 2 {
		t.Errorf("usage = %v ok=%v err=%v calls=%d; want a retried success", v, ok, err, usage.calls)
	}

	denied := &fakeServiceQuotas{err: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no servicequotas:GetServiceQuota"}}
	r := checkServiceQuotas(context.Background(), denied, usage)
	if !r.Skipped || !strings.Contains(r.Message, "AccessDeniedException") || strings.Contains(r.Message, "\n") {
		t.Errorf("result = %+v, want a skipped one-line AccessDeniedException summary", r)
	}
}
