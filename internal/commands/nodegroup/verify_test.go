package nodegroup

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8s "k8s.io/client-go/kubernetes/fake"

	"github.com/dantech2000/refresh/internal/mocks"
)

func pendingPod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
}

func TestVerifyPostRoll_ActiveAndNoNewPending(t *testing.T) {
	eksMock := &mocks.EKSAPI{
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{Status: ekstypes.NodegroupStatusActive}}, nil
		},
	}
	// A pod that was already pending before the roll must not count as an issue.
	k8s := fakek8s.NewSimpleClientset(pendingPod("default", "pre-existing"))
	preroll := snapshotPendingPods(context.Background(), k8s)

	v := verifyPostRoll(context.Background(), eksMock, k8s, "c", []string{"ng-a"}, preroll)
	if !v.OK() {
		t.Errorf("expected OK, got issues: %v", v.Issues)
	}
}

func TestVerifyPostRoll_NewlyPendingPodIsIssue(t *testing.T) {
	eksMock := &mocks.EKSAPI{
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{Status: ekstypes.NodegroupStatusActive}}, nil
		},
	}
	preroll := pendingPodSet{} // nothing pending before
	k8s := fakek8s.NewSimpleClientset(pendingPod("default", "stuck-after-roll"))

	v := verifyPostRoll(context.Background(), eksMock, k8s, "c", []string{"ng-a"}, preroll)
	if v.OK() {
		t.Error("a newly-pending pod should be flagged as an issue")
	}
}

func TestVerifyPostRoll_DegradedNodegroupIsIssue(t *testing.T) {
	eksMock := &mocks.EKSAPI{
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{Status: ekstypes.NodegroupStatusDegraded}}, nil
		},
	}
	// No kube client → AWS-only verification.
	v := verifyPostRoll(context.Background(), eksMock, nil, "c", []string{"ng-a"}, pendingPodSet{})
	if v.OK() {
		t.Error("a DEGRADED nodegroup should be flagged as an issue")
	}
}
