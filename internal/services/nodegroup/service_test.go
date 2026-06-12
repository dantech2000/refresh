package nodegroup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/dantech2000/refresh/internal/mocks"
	"github.com/dantech2000/refresh/internal/types"
)

// silentLogger discards all log output during tests.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestService builds a ServiceImpl with the given EKS mock and nil optional clients.
// nil ec2/asg/ssmClient is safe as long as test nodegroups use AMITypesCustom (no SSM
// lookup) and nil Resources/LaunchTemplate (no EC2/ASG lookup).
func newTestService(eksClient EKSAPI) *ServiceImpl {
	return &ServiceImpl{
		eksClient: eksClient,
		logger:    silentLogger(),
	}
}

// stubNodegroup returns a minimal Nodegroup fixture that avoids any EC2/ASG/SSM calls.
// Use status=Updating to get AMIUpdating without any client calls.
func stubNodegroup(name string, status ekstypes.NodegroupStatus) *ekstypes.Nodegroup {
	return &ekstypes.Nodegroup{
		NodegroupName: aws.String(name),
		Status:        status,
		// AMITypesCustom → buildSSMParameterPath returns "" → LatestAmiIDForType returns ""
		// without touching ssmClient.
		AmiType:       ekstypes.AMITypesCustom,
		InstanceTypes: []string{"m5.large"},
		ScalingConfig: &ekstypes.NodegroupScalingConfig{
			DesiredSize: aws.Int32(2),
			MinSize:     aws.Int32(1),
			MaxSize:     aws.Int32(4),
		},
		// nil Resources + nil LaunchTemplate → CurrentAmiID returns "" without touching clients.
	}
}

// clusterFn returns a DescribeCluster stub for the given k8s version.
func clusterFn(version string) func(context.Context, *eks.DescribeClusterInput, ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	return func(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
		return &eks.DescribeClusterOutput{
			Cluster: &ekstypes.Cluster{Name: in.Name, Version: aws.String(version)},
		}, nil
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// List tests
// ──────────────────────────────────────────────────────────────────────────────

func TestList_ReturnsNodegroupSummary(t *testing.T) {
	ng := stubNodegroup("workers", ekstypes.NodegroupStatusActive)
	mock := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.29"),
		ListNodegroupsFn: func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return &eks.ListNodegroupsOutput{Nodegroups: []string{"workers"}}, nil
		},
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: ng}, nil
		},
	}

	svc := newTestService(mock)
	summaries, err := svc.List(context.Background(), "my-cluster", ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	s := summaries[0]
	if s.Name != "workers" {
		t.Errorf("Name = %q, want %q", s.Name, "workers")
	}
	if s.InstanceType != "m5.large" {
		t.Errorf("InstanceType = %q, want %q", s.InstanceType, "m5.large")
	}
	if s.DesiredSize != 2 {
		t.Errorf("DesiredSize = %d, want 2", s.DesiredSize)
	}
}

func TestList_DescribeClusterError(t *testing.T) {
	mock := &mocks.EKSAPI{
		DescribeClusterFn: func(_ context.Context, _ *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
			return nil, errors.New("access denied")
		},
	}
	svc := newTestService(mock)
	_, err := svc.List(context.Background(), "my-cluster", ListOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestList_ListNodegroupsError(t *testing.T) {
	mock := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.29"),
		ListNodegroupsFn: func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return nil, errors.New("throttled")
		},
	}
	svc := newTestService(mock)
	_, err := svc.List(context.Background(), "my-cluster", ListOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestList_DescribeNodegroupErrorSkipsNodegroup(t *testing.T) {
	mock := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.29"),
		ListNodegroupsFn: func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return &eks.ListNodegroupsOutput{Nodegroups: []string{"good", "bad"}}, nil
		},
		DescribeNodegroupFn: func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			if aws.ToString(in.NodegroupName) == "bad" {
				return nil, errors.New("not found")
			}
			return &eks.DescribeNodegroupOutput{Nodegroup: stubNodegroup("good", ekstypes.NodegroupStatusActive)}, nil
		},
	}
	svc := newTestService(mock)
	summaries, err := svc.List(context.Background(), "my-cluster", ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Name != "good" {
		t.Errorf("expected only 'good' nodegroup, got %v", summaries)
	}
}

func TestList_AMIStatusUpdating(t *testing.T) {
	ng := stubNodegroup("workers", ekstypes.NodegroupStatusUpdating)
	mock := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.29"),
		ListNodegroupsFn: func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return &eks.ListNodegroupsOutput{Nodegroups: []string{"workers"}}, nil
		},
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: ng}, nil
		},
	}
	svc := newTestService(mock)
	summaries, err := svc.List(context.Background(), "my-cluster", ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summaries[0].AMIStatus != types.AMIUpdating {
		t.Errorf("AMIStatus = %v, want AMIUpdating", summaries[0].AMIStatus)
	}
}

func TestList_AMIStatusCustomForCustomAMI(t *testing.T) {
	// stubNodegroup uses AmiType=CUSTOM; EKS doesn't manage that AMI, so it must
	// classify as Custom (not stale/current/unknown).
	ng := stubNodegroup("workers", ekstypes.NodegroupStatusActive)
	mock := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.29"),
		ListNodegroupsFn: func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return &eks.ListNodegroupsOutput{Nodegroups: []string{"workers"}}, nil
		},
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: ng}, nil
		},
	}
	svc := newTestService(mock)
	summaries, err := svc.List(context.Background(), "my-cluster", ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summaries[0].AMIStatus != types.AMICustom {
		t.Errorf("AMIStatus = %v, want AMICustom", summaries[0].AMIStatus)
	}
}

func TestClassifyAMI(t *testing.T) {
	const al2 = ekstypes.AMITypesAl2X8664
	cases := []struct {
		name            string
		amiType         ekstypes.AMITypes
		status          ekstypes.NodegroupStatus
		current, latest string
		want            types.AMIStatus
	}{
		{"updating wins", al2, ekstypes.NodegroupStatusUpdating, "ami-1", "ami-2", types.AMIUpdating},
		{"custom ami", ekstypes.AMITypesCustom, ekstypes.NodegroupStatusActive, "ami-1", "ami-2", types.AMICustom},
		{"empty ids unknown", al2, ekstypes.NodegroupStatusActive, "", "", types.AMIUnknown},
		{"latest", al2, ekstypes.NodegroupStatusActive, "ami-1", "ami-1", types.AMILatest},
		{"outdated", al2, ekstypes.NodegroupStatusActive, "ami-1", "ami-2", types.AMIOutdated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyAMI(tc.amiType, tc.status, tc.current, tc.latest); got != tc.want {
				t.Errorf("classifyAMI = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestList_EmptyCluster(t *testing.T) {
	mock := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.29"),
		ListNodegroupsFn: func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return &eks.ListNodegroupsOutput{}, nil
		},
	}
	svc := newTestService(mock)
	summaries, err := svc.List(context.Background(), "my-cluster", ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("expected 0 summaries, got %d", len(summaries))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Describe tests
// ──────────────────────────────────────────────────────────────────────────────

func TestDescribe_ReturnsNodegroupDetails(t *testing.T) {
	ng := stubNodegroup("workers", ekstypes.NodegroupStatusActive)
	ng.AmiType = ekstypes.AMITypesCustom
	ng.CapacityType = ekstypes.CapacityTypesOnDemand

	mock := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.29"),
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: ng}, nil
		},
	}
	svc := newTestService(mock)
	details, err := svc.Describe(context.Background(), "my-cluster", "workers", DescribeOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if details.Name != "workers" {
		t.Errorf("Name = %q, want %q", details.Name, "workers")
	}
	if details.Scaling.DesiredSize != 2 {
		t.Errorf("Scaling.DesiredSize = %d, want 2", details.Scaling.DesiredSize)
	}
	if details.InstanceType != "m5.large" {
		t.Errorf("InstanceType = %q, want %q", details.InstanceType, "m5.large")
	}
}

func TestDescribe_DescribeClusterError(t *testing.T) {
	mock := &mocks.EKSAPI{
		DescribeClusterFn: func(_ context.Context, _ *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
			return nil, errors.New("cluster not found")
		},
	}
	svc := newTestService(mock)
	_, err := svc.Describe(context.Background(), "my-cluster", "workers", DescribeOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDescribe_DescribeNodegroupError(t *testing.T) {
	mock := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.29"),
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return nil, errors.New("nodegroup not found")
		},
	}
	svc := newTestService(mock)
	_, err := svc.Describe(context.Background(), "my-cluster", "workers", DescribeOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDescribe_AMIStatusUpdating(t *testing.T) {
	ng := stubNodegroup("workers", ekstypes.NodegroupStatusUpdating)
	mock := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.29"),
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: ng}, nil
		},
	}
	svc := newTestService(mock)
	details, err := svc.Describe(context.Background(), "my-cluster", "workers", DescribeOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if details.AMIStatus != types.AMIUpdating {
		t.Errorf("AMIStatus = %v, want AMIUpdating", details.AMIStatus)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Scale tests
// ──────────────────────────────────────────────────────────────────────────────

func TestScale_DryRunSkipsUpdate(t *testing.T) {
	mock := &mocks.EKSAPI{}
	svc := newTestService(mock)

	err := svc.Scale(context.Background(), "my-cluster", "workers", aws.Int32(3), nil, nil, ScaleOptions{DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.Calls.UpdateNodegroupConfig != 0 {
		t.Errorf("UpdateNodegroupConfig called %d times, want 0 (dry run)", mock.Calls.UpdateNodegroupConfig)
	}
}

func TestScale_CallsUpdateNodegroupConfig(t *testing.T) {
	var capturedDesired int32
	mock := &mocks.EKSAPI{
		UpdateNodegroupConfigFn: func(_ context.Context, in *eks.UpdateNodegroupConfigInput, _ ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error) {
			if in.ScalingConfig != nil && in.ScalingConfig.DesiredSize != nil {
				capturedDesired = *in.ScalingConfig.DesiredSize
			}
			return &eks.UpdateNodegroupConfigOutput{}, nil
		},
	}
	svc := newTestService(mock)

	err := svc.Scale(context.Background(), "my-cluster", "workers", aws.Int32(5), nil, nil, ScaleOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.Calls.UpdateNodegroupConfig != 1 {
		t.Errorf("UpdateNodegroupConfig called %d times, want 1", mock.Calls.UpdateNodegroupConfig)
	}
	if capturedDesired != 5 {
		t.Errorf("desired size = %d, want 5", capturedDesired)
	}
}

func TestScale_PropagatesUpdateError(t *testing.T) {
	mock := &mocks.EKSAPI{
		UpdateNodegroupConfigFn: func(_ context.Context, _ *eks.UpdateNodegroupConfigInput, _ ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error) {
			return nil, errors.New("invalid scaling config")
		},
	}
	svc := newTestService(mock)

	err := svc.Scale(context.Background(), "my-cluster", "workers", aws.Int32(3), nil, nil, ScaleOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
