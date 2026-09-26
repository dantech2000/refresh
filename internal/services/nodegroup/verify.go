package nodegroup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/common"
	"github.com/dantech2000/refresh/internal/diag"
)

// PendingPods is a set of "namespace/name" for pods in the Pending phase,
// used to tell pods that were already pending before a roll from ones the roll
// left stuck.
type PendingPods map[string]struct{}

// SnapshotPendingPods captures the set of currently-Pending pods. ok is false
// when no kube client is available or the List call fails; callers must then
// treat the pod check as skipped, not as "nothing pending". `nodegroup update`
// and the TUI's roll take it before a roll starts.
func SnapshotPendingPods(ctx context.Context, k8sClient kubernetes.Interface) (set PendingPods, ok bool) {
	if k8sClient == nil {
		return nil, false
	}
	pods, err := k8sClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "status.phase=Pending"})
	if err != nil {
		return nil, false
	}
	// A pod pinned to a node that no longer exists is not stuck: it is a
	// DaemonSet pod the controller made for a node the roll just removed,
	// and the pod garbage collector deletes it. Seen on a real roll, where
	// aws-node, kube-proxy, and eks-pod-identity-agent pods for the
	// terminated node read as "newly Pending". When the nodes cannot be
	// listed, every Pending pod counts, as before.
	var nodes map[string]bool
	if list, err := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err == nil {
		nodes = make(map[string]bool, len(list.Items))
		for _, n := range list.Items {
			nodes[n.Name] = true
		}
	}
	set = PendingPods{}
	for _, p := range pods.Items {
		if name := pinnedNode(&p); nodes != nil && name != "" && !nodes[name] {
			continue
		}
		set[p.Namespace+"/"+p.Name] = struct{}{}
	}
	return set, true
}

// pinnedNode is the node a pod is bound to (spec.nodeName) or, for a
// DaemonSet pod not yet bound, the node its affinity requires by name.
func pinnedNode(p *corev1.Pod) string {
	if p.Spec.NodeName != "" {
		return p.Spec.NodeName
	}
	a := p.Spec.Affinity
	if a == nil || a.NodeAffinity == nil || a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return ""
	}
	for _, term := range a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		for _, f := range term.MatchFields {
			if f.Key == "metadata.name" && f.Operator == corev1.NodeSelectorOpIn && len(f.Values) == 1 {
				return f.Values[0]
			}
		}
	}
	return ""
}

// PostRollVerification is the result of verifying a completed AMI roll.
type PostRollVerification struct {
	Checks []string `json:"checks,omitempty" yaml:"checks,omitempty"`
	Issues []string `json:"issues,omitempty" yaml:"issues,omitempty"`
	// skipped holds the Checks entries that did not run, so the human view
	// marks them as not measured instead of passed. It is not serialized:
	// the documents keep the check text as it was.
	skipped []string
}

// Skip records a check that did not run: in Checks, as the documents have
// always shown it, and in skipped for the human view.
func (v *PostRollVerification) Skip(msg string) {
	v.Checks = append(v.Checks, msg)
	v.skipped = append(v.skipped, msg)
}

// Skipped reports whether check is a Checks entry that did not run.
func (v PostRollVerification) Skipped(check string) bool {
	return slices.Contains(v.skipped, check)
}

// OK reports whether verification found no problems.
func (v PostRollVerification) OK() bool { return len(v.Issues) == 0 }

// NodegroupDescriber is the slice of the EKS API verification needs (satisfied
// by *eks.Client and the test mock).
type NodegroupDescriber interface {
	DescribeNodegroup(ctx context.Context, in *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
}

// VerifyPostRoll confirms each updated nodegroup returned to ACTIVE and, when a
// kube client is available, that no pods became newly stuck Pending relative to
// the pre-roll snapshot. Without kube access it degrades to the AWS-side
// nodegroup-status check (mirroring how pre-flight degrades). prerollOK is the
// ok result of the pre-roll SnapshotPendingPods call; if either snapshot
// failed, the pod check is reported as skipped rather than passed or failed.
//
// A nodegroup that can't be described after the roll is not a verification
// issue: its state is unknown. It is returned as a failure (exit 4) instead,
// with no cluster set.
func VerifyPostRoll(ctx context.Context, eksClient NodegroupDescriber, k8sClient kubernetes.Interface, clusterName string, nodegroups []string, preroll PendingPods, prerollOK bool) (PostRollVerification, []diag.Failure) {
	var v PostRollVerification
	var failures []diag.Failure

	for _, ng := range nodegroups {
		desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
			return eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
				ClusterName:   aws.String(clusterName),
				NodegroupName: aws.String(ng),
			})
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
		v.Skip("pod verification skipped (no Kubernetes access)")
		return v, failures
	}

	if !prerollOK {
		v.Skip("pod verification skipped (could not list Pending pods before the roll)")
		return v, failures
	}
	after, ok := SnapshotPendingPods(ctx, k8sClient)
	if !ok {
		v.Skip("pod verification skipped (could not list Pending pods after the roll)")
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
