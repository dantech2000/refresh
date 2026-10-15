package health

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// The node thresholds. Below half of the settled nodes Ready, too little
// capacity is left to take the pods of the nodes that a roll drains. A
// NotReady node created within the grace is still joining: a scale-up
// returns before the kubelets of its nodes report Ready.
const (
	minReadyNodePercent = 50
	nodeJoinGrace       = 10 * time.Minute
)

// labelNodegroup is the label that EKS sets on the nodes of a managed
// nodegroup.
const labelNodegroup = "eks.amazonaws.com/nodegroup"

// Nodes checks that the nodes of the cluster are Ready, and that no
// managed nodegroup is degraded. With Kubernetes access it counts the
// nodes; without it, it estimates from the desired size of the ACTIVE
// nodegroups. A target nodegroup that is not ACTIVE fails the check in
// either mode.
func Nodes() Check {
	return Check{Name: "Nodes", Blocking: true, FailClosed: true, Run: runNodes}
}

func runNodes(_ context.Context, in *Input) Result {
	snap := in.Snapshot
	if snap.Have.Nodegroups != fleet.Collected {
		return Result{Outcome: Unreadable, Message: "the nodegroups could not all be read"}
	}
	var (
		problems, inProgress []string
		targetFail           bool
		total, ready         int
	)
	for _, n := range snap.Nodegroups {
		target := slices.Contains(in.Targets, n.Name)
		switch n.Status {
		case "ACTIVE":
			total += int(n.Desired)
			ready += int(n.Desired)
		case "CREATING", "UPDATING":
			inProgress = append(inProgress, n.Name+" ("+n.Status+")")
			targetFail = targetFail || target
		default:
			total += int(n.Desired)
			problems = append(problems, n.Name+" ("+n.Status+")")
			targetFail = targetFail || target
		}
	}
	if in.Kube == nil {
		return estimateNodes(total, ready, problems, inProgress, targetFail)
	}
	list, err := in.reads.Nodes()
	if err != nil {
		f := kubeFailure(snap.Cluster.Name, "listing nodes", err)
		return Result{Outcome: Unreadable, Message: "could not list the nodes: " + f.Error, Failures: []apidoc.Failure{f}}
	}
	var joining, notReady []string
	ready = 0
	for i := range list.Items {
		node := &list.Items[i]
		switch {
		case nodeReady(node):
			ready++
		case nodeJoining(node, in.Now):
			joining = append(joining, node.Name)
		default:
			notReady = append(notReady, node.Name)
		}
	}
	return countNodes(len(list.Items), ready, joining, notReady, problems, inProgress, targetFail)
}

// countNodes is the rule for counts from the Kubernetes API.
func countNodes(total, ready int, joining, notReady, problems, inProgress []string, targetFail bool) Result {
	findings := slices.Sorted(slices.Values(slices.Concat(notReady, joining, problems)))
	r := Result{Findings: findings}
	if len(inProgress) > 0 {
		r.Details = append(r.Details, "nodegroups mid-change (not a finding): "+strings.Join(inProgress, ", "))
	}
	settled := total - len(joining)
	issues := func() string { return "; issues: " + strings.Join(findings, ", ") }
	switch {
	case targetFail:
		r.Outcome, r.Message = Fail, "a target nodegroup is not ACTIVE"+issues()
	case total == 0 && len(findings) == 0 && len(inProgress) > 0:
		// The nodes are still coming up: not a failure.
		r.Outcome, r.Message = Warn, "no nodes yet; nodegroups still scaling: "+strings.Join(inProgress, ", ")
	case total == 0:
		r.Outcome, r.Message = Fail, "no nodes found in the cluster"
	case len(findings) == 0:
		r.Outcome, r.Message = Pass, fmt.Sprintf("%d/%d nodes ready", ready, total)
	case ready == 0 && settled > 0:
		r.Outcome, r.Message = Fail, "no ready nodes"+issues()
	case ready == 0:
		r.Outcome, r.Message = Warn, fmt.Sprintf("no ready nodes yet, %d joining", len(joining))+issues()
	case ready*100 < settled*minReadyNodePercent:
		r.Outcome, r.Message = Fail, fmt.Sprintf("%d/%d nodes ready, below the %d%% minimum", ready, total, minReadyNodePercent)+issues()
	default:
		r.Outcome, r.Message = Warn, fmt.Sprintf("%d/%d nodes ready", ready, total)+issues()
	}
	return r
}

// estimateNodes is the rule for the estimate from the nodegroups. It never
// applies the minimum: a desired size says nothing about the nodes.
func estimateNodes(total, ready int, problems, inProgress []string, targetFail bool) Result {
	r := Result{Findings: slices.Sorted(slices.Values(problems)), Details: []string{"estimated from the desired size of the nodegroups (no Kubernetes access)"}}
	if len(inProgress) > 0 {
		r.Details = append(r.Details, "nodegroups mid-change (not a finding): "+strings.Join(inProgress, ", "))
	}
	switch {
	case targetFail:
		r.Outcome, r.Message = Fail, "a target nodegroup is not ACTIVE: "+strings.Join(slices.Concat(problems, inProgress), ", ")
	case total == 0 && len(inProgress) == 0 && len(problems) == 0:
		r.Outcome, r.Message = Skipped, "no managed nodegroup capacity; node readiness needs Kubernetes access"
	case total == 0 && len(problems) == 0:
		r.Outcome, r.Message = Warn, "every nodegroup is mid-change: "+strings.Join(inProgress, ", ")
	case len(problems) == 0:
		r.Outcome, r.Message = Pass, fmt.Sprintf("%d/%d nodes ready (estimated)", ready, total)
	case ready == 0 && len(inProgress) > 0:
		// A nodegroup that is still coming up may bring the ready nodes.
		r.Outcome, r.Message = Warn, "no ready nodes yet (estimated); issues: "+strings.Join(problems, ", ")
	case ready == 0:
		r.Outcome, r.Message = Fail, "no ready nodes (estimated); issues: "+strings.Join(problems, ", ")
	default:
		r.Outcome, r.Message = Warn, fmt.Sprintf("%d/%d nodes ready (estimated); issues: %s", ready, total, strings.Join(problems, ", "))
	}
	return r
}

// nodeReady reports whether the Ready condition of a node is True. False
// and Unknown both count as not ready.
func nodeReady(node *corev1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// nodeJoining reports whether a NotReady node is still joining: created
// within the grace, and its Ready condition missing or set by a kubelet
// that is starting. A kubelet that stopped posting is broken, not joining.
func nodeJoining(node *corev1.Node, now time.Time) bool {
	if node.CreationTimestamp.IsZero() || now.Sub(node.CreationTimestamp.Time) > nodeJoinGrace {
		return false
	}
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Reason == "KubeletNotReady" || c.Reason == "NodeStatusNeverUpdated"
		}
	}
	return true
}

func joinNames(names []string) string { return strings.Join(names, ", ") }
