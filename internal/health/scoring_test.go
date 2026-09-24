package health

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	fakek8s "k8s.io/client-go/kubernetes/fake"

	"github.com/dantech2000/refresh/internal/mocks"
)

// listedNodegroups is an EKS mock that lists names and answers every
// DescribeNodegroup with describe.
func listedNodegroups(names []string, describe func(name string) (*eks.DescribeNodegroupOutput, error)) *mocks.EKSAPI {
	return &mocks.EKSAPI{
		ListNodegroupsFn: func(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return &eks.ListNodegroupsOutput{Nodegroups: names}, nil
		},
		DescribeNodegroupFn: func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return describe(aws.ToString(in.NodegroupName))
		},
	}
}

func nodegroupWith(name string, status ekstypes.NodegroupStatus, desired int32) *eks.DescribeNodegroupOutput {
	return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{
		NodegroupName: aws.String(name),
		Status:        status,
		ScalingConfig: &ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(desired)},
	}}
}

// requireNoBlock fails when r, alone or in the aggregate, blocks.
func requireNoBlock(t *testing.T, r HealthResult) {
	t.Helper()
	if r.Status == StatusFail && r.IsBlocking {
		t.Errorf("got a blocking FAIL: %+v", r)
	}
	if d := aggregateResults([]HealthResult{r}).Decision; d == DecisionBlock {
		t.Errorf("decision = %s, want no block", d)
	}
}

// Every DescribeNodegroup throttled past the retries used to read as
// "No nodes found in cluster", a blocking FAIL.
func TestCheckNodeHealth_ThrottledDescribesDoNotBlock(t *testing.T) {
	api := listedNodegroups([]string{"ng-a", "ng-b"}, func(string) (*eks.DescribeNodegroupOutput, error) {
		return nil, mocks.Throttling()
	})
	r := (&HealthChecker{eksClient: api}).CheckNodeHealth(context.Background(), "prod")
	requireNoBlock(t, r)
	if r.Status != StatusWarn || !strings.Contains(r.Message, "Could not describe 2 of 2 nodegroups") {
		t.Errorf("status = %s, message = %q; want a WARN that names the unread nodegroups", r.Status, r.Message)
	}
	if len(r.failures) != 2 {
		t.Errorf("failures = %d, want 2 (one per nodegroup)", len(r.failures))
	}
}

func TestCheckNodeHealth_ThrottledListDoesNotBlock(t *testing.T) {
	api := &mocks.EKSAPI{
		ListNodegroupsFn: func(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return nil, mocks.Throttling()
		},
	}
	r := (&HealthChecker{eksClient: api}).CheckNodeHealth(context.Background(), "prod")
	requireNoBlock(t, r)
	if r.Status != StatusWarn || len(r.failures) != 1 {
		t.Errorf("got %+v with %d failures, want a WARN with 1 failure", r, len(r.failures))
	}
}

// A permanent error is not a throttle: the check still fails and blocks.
func TestCheckNodeHealth_DeniedDescribesStillBlock(t *testing.T) {
	api := listedNodegroups([]string{"ng-a"}, func(string) (*eks.DescribeNodegroupOutput, error) {
		return nil, mocks.AccessDenied()
	})
	r := (&HealthChecker{eksClient: api}).CheckNodeHealth(context.Background(), "prod")
	if r.Status != StatusFail || !r.IsBlocking {
		t.Errorf("got %+v, want a blocking FAIL", r)
	}
}

// A cluster without managed nodegroups (Fargate, Karpenter, self-managed)
// and without Kubernetes access has nothing to estimate from.
func TestCheckNodeHealth_NoManagedNodegroupsWithoutKubeIsSkipped(t *testing.T) {
	api := mocks.NewEKSAPI().WithCluster("prod", "1.32").Build()
	r := (&HealthChecker{eksClient: api}).CheckNodeHealth(context.Background(), "prod")
	requireNoBlock(t, r)
	if !r.Skipped {
		t.Errorf("got %+v, want a skipped check", r)
	}
	if d := aggregateResults([]HealthResult{r}).Decision; d != DecisionProceed {
		t.Errorf("decision = %s, want Proceed", d)
	}
}

// With Kubernetes access the nodes of a cluster without managed nodegroups
// are counted.
func TestCheckNodeHealth_NoManagedNodegroupsWithKubeCountsNodes(t *testing.T) {
	api := mocks.NewEKSAPI().WithCluster("prod", "1.32").Build()
	fargate := readyNode("fargate-ip-10-0-0-1")
	fargate.Labels = map[string]string{"eks.amazonaws.com/compute-type": "fargate"}
	r := (&HealthChecker{eksClient: api, k8sClient: fakek8s.NewSimpleClientset(fargate)}).CheckNodeHealth(context.Background(), "prod")
	if r.Status != StatusPass || r.Skipped || r.Message != "1/1 nodes ready" {
		t.Errorf("got %+v, want Pass 1/1 nodes ready", r)
	}
}

// With Kubernetes access and no nodes at all, the check still fails.
func TestCheckNodeHealth_NoNodesWithKubeFails(t *testing.T) {
	api := mocks.NewEKSAPI().WithCluster("prod", "1.32").Build()
	r := (&HealthChecker{eksClient: api, k8sClient: fakek8s.NewSimpleClientset()}).CheckNodeHealth(context.Background(), "prod")
	if r.Status != StatusFail || !r.IsBlocking || r.Message != "No nodes found in cluster" {
		t.Errorf("got %+v, want a blocking FAIL", r)
	}
}

// 3 ACTIVE and 3 UPDATING nodegroups used to pass as "3/6 nodes ready" with
// a score of 50: the UPDATING capacity counted in the total but never as ready.
func TestCheckNodeHealth_UpdatingNodegroupsAreNotHalfReady(t *testing.T) {
	names := []string{"ng-a1", "ng-a2", "ng-a3", "ng-u1", "ng-u2", "ng-u3"}
	api := listedNodegroups(names, func(name string) (*eks.DescribeNodegroupOutput, error) {
		if strings.HasPrefix(name, "ng-u") {
			return nodegroupWith(name, ekstypes.NodegroupStatusUpdating, 1), nil
		}
		return nodegroupWith(name, ekstypes.NodegroupStatusActive, 1), nil
	})
	r := (&HealthChecker{eksClient: api}).CheckNodeHealth(context.Background(), "prod")
	if r.Status != StatusPass || r.Message != "3/3 nodes ready (estimated)" || r.Score != maxEstimatedNodeHealthScore {
		t.Errorf("status = %s, message = %q, score = %d; want Pass, 3/3 nodes ready (estimated), %d",
			r.Status, r.Message, r.Score, maxEstimatedNodeHealthScore)
	}
	if !hasDetail(r.Details, "Nodegroups scaling/updating (not a failure)") {
		t.Errorf("details = %v, want the in-progress nodegroups listed", r.Details)
	}
}

func TestCheckNodeHealth_AllNodegroupsUpdatingWarns(t *testing.T) {
	api := listedNodegroups([]string{"ng-u1", "ng-u2"}, func(name string) (*eks.DescribeNodegroupOutput, error) {
		return nodegroupWith(name, ekstypes.NodegroupStatusUpdating, 2), nil
	})
	r := (&HealthChecker{eksClient: api}).CheckNodeHealth(context.Background(), "prod")
	requireNoBlock(t, r)
	if r.Status != StatusWarn || !strings.Contains(r.Message, "Nodegroups still scaling") {
		t.Errorf("status = %s, message = %q; want a still-scaling WARN", r.Status, r.Message)
	}
}

// Every skipped check has the same shape, and none blocks. Some used to be
// Warn with score 70 and others Pass with score 0.
func TestSkippedChecksAreConsistent(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	ctx := context.Background()
	results := []HealthResult{
		hc.CheckCriticalWorkloads(ctx),
		hc.CheckPodDisruptionBudgets(ctx),
		hc.CheckNodeUtilization(ctx, "prod"),
		hc.CheckControlPlaneMetrics(ctx, "prod"),
		hc.CheckServiceQuotas(ctx, "prod"),
		evaluateControlPlane(controlPlaneMetrics{}),
		evaluateQuota(10, 0),
		checkControlPlaneMetrics(ctx, &sequenceMetricsAPI{errs: []error{mocks.AccessDenied()}}, "prod"),
	}
	for _, r := range results {
		if !r.Skipped || r.Status != StatusPass || r.Score != 0 || r.IsBlocking {
			t.Errorf("%s: got status=%s score=%d skipped=%v blocking=%v, want a skipped Pass with score 0 that does not block",
				r.Name, r.Status, r.Score, r.Skipped, r.IsBlocking)
		}
	}
	if s := aggregateResults(results); s.Decision != DecisionProceed || len(s.Warnings) != 0 {
		t.Errorf("decision = %s, warnings = %v; want Proceed and none", s.Decision, s.Warnings)
	}
}
