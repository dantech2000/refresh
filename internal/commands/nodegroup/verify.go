package nodegroup

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/diag"
)

// pendingPodSet is a set of "namespace/name" for pods in the Pending phase,
// used to tell pods that were already pending before a roll from ones the roll
// left stuck.
type pendingPodSet map[string]struct{}

// snapshotPendingPods captures the set of currently-Pending pods. ok is false
// when no kube client is available or the List call fails; callers must then
// treat the pod check as skipped, not as "nothing pending".
func snapshotPendingPods(ctx context.Context, k8sClient kubernetes.Interface) (set pendingPodSet, ok bool) {
	if k8sClient == nil {
		return nil, false
	}
	pods, err := k8sClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "status.phase=Pending"})
	if err != nil {
		return nil, false
	}
	set = pendingPodSet{}
	for _, p := range pods.Items {
		set[p.Namespace+"/"+p.Name] = struct{}{}
	}
	return set, true
}

// PostRollVerification is the result of verifying a completed AMI roll.
type PostRollVerification struct {
	Checks []string `json:"checks,omitempty" yaml:"checks,omitempty"`
	Issues []string `json:"issues,omitempty" yaml:"issues,omitempty"`
}

// OK reports whether verification found no problems.
func (v PostRollVerification) OK() bool { return len(v.Issues) == 0 }

// nodegroupDescriber is the slice of the EKS API verification needs (satisfied
// by *eks.Client and the test mock).
type nodegroupDescriber interface {
	DescribeNodegroup(ctx context.Context, in *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
}

// verifyPostRoll confirms each updated nodegroup returned to ACTIVE and, when a
// kube client is available, that no pods became newly stuck Pending relative to
// the pre-roll snapshot. Without kube access it degrades to the AWS-side
// nodegroup-status check (mirroring how pre-flight degrades). prerollOK is the
// ok result of the pre-roll snapshotPendingPods call; if either snapshot
// failed, the pod check is reported as skipped rather than passed or failed.
//
// A nodegroup that can't be described after the roll is not a verification
// issue: its state is unknown. It is returned as a failure (exit 4) instead,
// with no cluster set.
func verifyPostRoll(ctx context.Context, eksClient nodegroupDescriber, k8sClient kubernetes.Interface, clusterName string, nodegroups []string, preroll pendingPodSet, prerollOK bool) (PostRollVerification, []diag.Failure) {
	var v PostRollVerification
	var failures []diag.Failure

	for _, ng := range nodegroups {
		desc, err := eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(ng),
		})
		if err == nil && (desc == nil || desc.Nodegroup == nil) {
			err = errors.New("empty DescribeNodegroup response")
		}
		if err != nil {
			failures = append(failures, diag.FromError(diag.KindNodegroup, ng, diag.OpDescribeNodegroup,
				fmt.Errorf("describing the nodegroup after the roll: %w", err)))
			continue
		}
		if desc.Nodegroup.Status == ekstypes.NodegroupStatusActive {
			v.Checks = append(v.Checks, fmt.Sprintf("nodegroup %s is ACTIVE", ng))
		} else {
			v.Issues = append(v.Issues, fmt.Sprintf("nodegroup %s status is %s (expected ACTIVE)", ng, string(desc.Nodegroup.Status)))
		}
	}

	if k8sClient == nil {
		v.Checks = append(v.Checks, "pod verification skipped (no Kubernetes access)")
		return v, failures
	}

	if !prerollOK {
		v.Checks = append(v.Checks, "pod verification skipped (could not list Pending pods before the roll)")
		return v, failures
	}
	after, ok := snapshotPendingPods(ctx, k8sClient)
	if !ok {
		v.Checks = append(v.Checks, "pod verification skipped (could not list Pending pods after the roll)")
		return v, failures
	}
	var newlyPending []string
	for key := range after {
		if _, existed := preroll[key]; !existed {
			newlyPending = append(newlyPending, key)
		}
	}
	sort.Strings(newlyPending)
	if len(newlyPending) == 0 {
		v.Checks = append(v.Checks, "no new Pending pods")
		return v, failures
	}
	shown := newlyPending
	if len(shown) > 5 {
		shown = shown[:5]
	}
	v.Issues = append(v.Issues, fmt.Sprintf("%d pod(s) newly Pending after roll: %v", len(newlyPending), shown))
	return v, failures
}
