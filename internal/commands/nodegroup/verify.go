package nodegroup

import (
	"context"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// pendingPodSet is a set of "namespace/name" for pods in the Pending phase,
// used to tell pods that were already pending before a roll from ones the roll
// left stuck.
type pendingPodSet map[string]struct{}

// snapshotPendingPods captures the set of currently-Pending pods. Returns an
// empty set when no kube client is available or the list fails (best-effort).
func snapshotPendingPods(ctx context.Context, k8sClient kubernetes.Interface) pendingPodSet {
	set := pendingPodSet{}
	if k8sClient == nil {
		return set
	}
	pods, err := k8sClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "status.phase=Pending"})
	if err != nil {
		return set
	}
	for _, p := range pods.Items {
		set[p.Namespace+"/"+p.Name] = struct{}{}
	}
	return set
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
// nodegroup-status check (mirroring how pre-flight degrades).
func verifyPostRoll(ctx context.Context, eksClient nodegroupDescriber, k8sClient kubernetes.Interface, clusterName string, nodegroups []string, preroll pendingPodSet) PostRollVerification {
	var v PostRollVerification

	for _, ng := range nodegroups {
		desc, err := eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(ng),
		})
		if err != nil {
			v.Issues = append(v.Issues, fmt.Sprintf("%s: could not describe after roll: %v", ng, err))
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
		return v
	}

	after := snapshotPendingPods(ctx, k8sClient)
	var newlyPending []string
	for key := range after {
		if _, existed := preroll[key]; !existed {
			newlyPending = append(newlyPending, key)
		}
	}
	sort.Strings(newlyPending)
	if len(newlyPending) == 0 {
		v.Checks = append(v.Checks, "no new Pending pods")
		return v
	}
	shown := newlyPending
	if len(shown) > 5 {
		shown = shown[:5]
	}
	v.Issues = append(v.Issues, fmt.Sprintf("%d pod(s) newly Pending after roll: %v", len(newlyPending), shown))
	return v
}
