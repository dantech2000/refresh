package nodegroup

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8s "k8s.io/client-go/kubernetes/fake"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/mocks"
)

func readinessNode(name, ng string, ready corev1.ConditionStatus) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"eks.amazonaws.com/nodegroup": ng},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}},
		},
	}
}

// REF-130: with a cluster-connected health checker, List reports a measured
// Ready count — a NotReady node yields ready < desired, never desired/desired.
func TestList_MeasuredReadiness(t *testing.T) {
	ng := stubNodegroup("workers", ekstypes.NodegroupStatusActive) // DesiredSize 2
	mock := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.29"),
		ListNodegroupsFn: func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return &eks.ListNodegroupsOutput{Nodegroups: []string{"workers"}}, nil
		},
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: ng}, nil
		},
	}
	k8s := fakek8s.NewSimpleClientset(
		readinessNode("w1", "workers", corev1.ConditionTrue),
		readinessNode("w2", "workers", corev1.ConditionFalse), // NotReady
	)
	svc := &ServiceImpl{
		eksClient:     mock,
		logger:        silentLogger(),
		healthChecker: health.NewChecker(nil, k8s, nil, nil),
	}

	summaries, err := svc.List(context.Background(), "my-cluster", ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := summaries[0]
	if !s.ReadyKnown {
		t.Fatal("ReadyKnown = false, want true (health checker wired)")
	}
	if s.ReadyNodes != 1 {
		t.Errorf("ReadyNodes = %d, want 1 (one of two nodes Ready)", s.ReadyNodes)
	}
	if s.DesiredSize != 2 {
		t.Errorf("DesiredSize = %d, want 2", s.DesiredSize)
	}
}

// REF-130: without a health checker, readiness is honestly unknown — ReadyNodes
// is not synthesized from desired even for an ACTIVE nodegroup.
func TestList_ReadinessUnknownWithoutHealthChecker(t *testing.T) {
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
	s := summaries[0]
	if s.ReadyKnown {
		t.Error("ReadyKnown = true, want false (no health checker)")
	}
	if s.ReadyNodes != 0 {
		t.Errorf("ReadyNodes = %d, want 0 (unmeasured, not synthesized from desired)", s.ReadyNodes)
	}
}
