package nodegroup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/dantech2000/refresh/internal/diag"
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
	preroll, ok := snapshotPendingPods(context.Background(), k8s)
	if !ok {
		t.Fatal("snapshot against a reachable fake API should succeed")
	}

	v, _ := verifyPostRoll(context.Background(), eksMock, k8s, "c", []string{"ng-a"}, preroll, true)
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

	v, _ := verifyPostRoll(context.Background(), eksMock, k8s, "c", []string{"ng-a"}, preroll, true)
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
	v, _ := verifyPostRoll(context.Background(), eksMock, nil, "c", []string{"ng-a"}, nil, false)
	if v.OK() {
		t.Error("a DEGRADED nodegroup should be flagged as an issue")
	}
}

// A nodegroup that can't be described after the roll is a failure (its
// state is unknown, exit 4), not a verification issue (exit 5).
func TestVerifyPostRoll_DescribeFailureIsAFailureNotAnIssue(t *testing.T) {
	eksMock := &mocks.EKSAPI{
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return nil, mocks.Throttling()
		},
	}
	v, failures := verifyPostRoll(context.Background(), eksMock, nil, "c", []string{"ng-a"}, nil, false)
	if !v.OK() {
		t.Errorf("issues = %v, want none: an unreadable nodegroup is not a verification issue", v.Issues)
	}
	if len(failures) != 1 {
		t.Fatalf("failures = %+v, want one", failures)
	}
	f := failures[0]
	if f.Kind != diag.KindNodegroup || f.Name != "ng-a" || f.Operation != diag.OpDescribeNodegroup || f.Reason != diag.ReasonThrottled || !f.Retryable {
		t.Errorf("failure = %+v", f)
	}
}

func activeNodegroupEKS() *mocks.EKSAPI {
	return &mocks.EKSAPI{
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{Status: ekstypes.NodegroupStatusActive}}, nil
		},
	}
}

// failPodList makes every pod List on the fake client return an error, as when
// the cluster API is unreachable.
func failPodList(k8s *fakek8s.Clientset) {
	k8s.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
}

func hasCheckContaining(v PostRollVerification, sub string) bool {
	for _, c := range v.Checks {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func TestSnapshotPendingPods_ListErrorIsNotOK(t *testing.T) {
	k8s := fakek8s.NewSimpleClientset(pendingPod("default", "p"))
	failPodList(k8s)
	if _, ok := snapshotPendingPods(context.Background(), k8s); ok {
		t.Error("a failed List must report ok=false, not an empty set")
	}
	if _, ok := snapshotPendingPods(context.Background(), nil); ok {
		t.Error("no kube client must report ok=false")
	}
}

// Post-roll List fails (API unreachable): the pod check must be skipped, not
// reported as "no new Pending pods".
func TestVerifyPostRoll_PostRollListFailureSkipsPodCheck(t *testing.T) {
	k8s := fakek8s.NewSimpleClientset(pendingPod("default", "stuck-after-roll"))
	failPodList(k8s)

	v, _ := verifyPostRoll(context.Background(), activeNodegroupEKS(), k8s, "c", []string{"ng-a"}, pendingPodSet{}, true)
	if !v.OK() {
		t.Errorf("a skipped pod check is not an issue, got: %v", v.Issues)
	}
	if hasCheckContaining(v, "no new Pending pods") {
		t.Errorf("must not claim a pass when the List failed, got checks: %v", v.Checks)
	}
	if !hasCheckContaining(v, "skipped") {
		t.Errorf("expected a skipped pod check, got checks: %v", v.Checks)
	}
}

// Pre-roll List fails: pods that were already Pending must not be reported as
// newly Pending (exit 5); the pod check is skipped instead.
func TestVerifyPostRoll_PreRollSnapshotFailureSkipsPodCheck(t *testing.T) {
	k8s := fakek8s.NewSimpleClientset(pendingPod("default", "pre-existing"))

	v, _ := verifyPostRoll(context.Background(), activeNodegroupEKS(), k8s, "c", []string{"ng-a"}, nil, false)
	if !v.OK() {
		t.Errorf("pre-existing Pending pods must not be flagged when the pre-roll snapshot failed, got: %v", v.Issues)
	}
	if !hasCheckContaining(v, "skipped") {
		t.Errorf("expected a skipped pod check, got checks: %v", v.Checks)
	}
}
