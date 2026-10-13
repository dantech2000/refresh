package health

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet"
)

// Baseline is what verification compares against, taken before a roll.
type Baseline struct {
	// Nodes is the set of node names in the cluster.
	Nodes map[string]struct{}
	// Pending is the set of "namespace/name" of the Pending pods, less the
	// pods pinned to a node that is not in Nodes: a DaemonSet pod made for
	// a node that a roll just removed, which the garbage collector deletes.
	Pending map[string]struct{}
	// Failures are the lists that failed. The checks that need the
	// baseline are then Unreadable: the client was there and did not
	// answer.
	Failures []apidoc.Failure
	// Taken is false when there was no Kubernetes client.
	Taken bool
}

// TakeBaseline lists the nodes, with no selector, and the Pending pods, in
// one wave. With a nil client it returns a baseline that is not taken.
func TakeBaseline(ctx context.Context, kube kubernetes.Interface, cluster string) Baseline {
	if kube == nil {
		return Baseline{}
	}
	b := Baseline{Taken: true, Nodes: map[string]struct{}{}, Pending: map[string]struct{}{}}
	var (
		nodes    *corev1.NodeList
		pods     *corev1.PodList
		nodesErr error
		podsErr  error
		done     = make(chan struct{})
	)
	go func() {
		defer close(done)
		nodes, nodesErr = kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	}()
	pods, podsErr = kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "status.phase=Pending"})
	<-done
	if nodesErr != nil {
		b.Failures = append(b.Failures, kubeFailure(cluster, "listing nodes before the roll", nodesErr))
	}
	if podsErr != nil {
		b.Failures = append(b.Failures, kubeFailure(cluster, "listing Pending pods before the roll", podsErr))
	}
	if nodesErr != nil || podsErr != nil {
		return b
	}
	for i := range nodes.Items {
		b.Nodes[nodes.Items[i].Name] = struct{}{}
	}
	for i := range pods.Items {
		if key, ok := pendingKey(&pods.Items[i], b.Nodes); ok {
			b.Pending[key] = struct{}{}
		}
	}
	return b
}

// pendingKey returns the key of a Pending pod, or false for a pod pinned
// to a node that is not in nodes, which is not stuck.
func pendingKey(pod *corev1.Pod, nodes map[string]struct{}) (string, bool) {
	if name := pinnedNode(pod); name != "" {
		if _, ok := nodes[name]; !ok {
			return "", false
		}
	}
	return pod.Namespace + "/" + pod.Name, true
}

// pinnedNode is the node a pod is bound to, or, for a DaemonSet pod not
// yet bound, the node that its affinity requires by name.
func pinnedNode(pod *corev1.Pod) string {
	if pod.Spec.NodeName != "" {
		return pod.Spec.NodeName
	}
	a := pod.Spec.Affinity
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

// WaitOptions bound the wait of the NodesReady check.
type WaitOptions struct {
	// Interval is how often to list the nodes again: --poll-interval.
	Interval time.Duration
	// Deadline is the remaining --wait-timeout. Zero means none.
	Deadline time.Time
	// After and Now come from the factory, so a test drives the wait with
	// a handshake. Nil means the real clock.
	After func(time.Duration) <-chan time.Time
	Now   func() time.Time
}

// nodesReadyBound is the most time NodesReady waits for a new node to be
// Ready after the update ended. EKS marks an update successful only after
// the new nodes joined and were Ready, so a NotReady new node after it is
// one that lost readiness; the bound absorbs a kubelet that is late by
// seconds and no more.
const nodesReadyBound = 2 * time.Minute

// Verify returns the checks that run after a roll of nodegroups:
// NodegroupActive, NodesReady, and NewPendingPods. The input's snapshot
// must be read after the wait, with the rolled nodegroups.
func Verify(nodegroups []string, base Baseline, wait WaitOptions) []Check {
	return []Check{
		{Name: "NodegroupActive", Blocking: true, FailClosed: true, Run: func(_ context.Context, in *Input) Result {
			return runNodegroupActive(in, nodegroups)
		}},
		{Name: "NodesReady", Blocking: true, FailClosed: true, Needs: Needs{Kube: true}, Run: func(ctx context.Context, in *Input) Result {
			return runNodesReady(ctx, in, nodegroups, base, wait)
		}},
		{Name: "NewPendingPods", Blocking: true, FailClosed: true, Needs: Needs{Kube: true}, Run: func(_ context.Context, in *Input) Result {
			return runNewPendingPods(in, base)
		}},
	}
}

// VerifyExit maps the results of Verify to the exit code of the
// verification: 5 when a check failed, else 4 when one could not read,
// else 0.
func VerifyExit(results []Result) int {
	code := exitcode.OK
	for _, r := range results {
		switch r.Outcome {
		case Fail:
			return exitcode.VerifyFailed
		case Unreadable:
			code = exitcode.Incomplete
		}
	}
	return code
}

func runNodegroupActive(in *Input, nodegroups []string) Result {
	var issues, unread, active []string
	for _, name := range nodegroups {
		i := slices.IndexFunc(in.Snapshot.Nodegroups, func(n fleet.Nodegroup) bool { return n.Name == name })
		switch {
		case i < 0 || in.Snapshot.Nodegroups[i].State != fleet.Collected:
			unread = append(unread, name)
		case in.Snapshot.Nodegroups[i].Status != "ACTIVE":
			issues = append(issues, fmt.Sprintf("%s is %s", name, in.Snapshot.Nodegroups[i].Status))
		default:
			active = append(active, name)
		}
	}
	r := Result{Findings: slices.Sorted(slices.Values(unread))}
	for _, issue := range issues {
		name, _, _ := strings.Cut(issue, " is ")
		r.Findings = append(r.Findings, name)
	}
	slices.Sort(r.Findings)
	switch {
	case len(issues) > 0:
		r.Outcome, r.Message = Fail, strings.Join(issues, ", ")+" (expected ACTIVE)"
		if len(unread) > 0 {
			r.Message += "; not read: " + strings.Join(unread, ", ")
		}
	case len(unread) > 0:
		r.Outcome, r.Message = Unreadable, "not read after the roll: "+strings.Join(unread, ", ")
	default:
		r.Outcome, r.Message = Pass, strings.Join(active, ", ")+" ACTIVE"
	}
	return r
}

// runNodesReady lists the nodes and keeps the new ones of the rolled
// nodegroups: those that carry a nodegroup's label and are not in the
// baseline. A new node that is not Ready is listed again every interval
// until it is, or until the bound passes.
func runNodesReady(ctx context.Context, in *Input, nodegroups []string, base Baseline, wait WaitOptions) Result {
	if !base.Taken {
		return Result{Outcome: Skipped, Message: "no baseline of the nodes before the roll"}
	}
	if len(base.Failures) > 0 {
		return Result{Outcome: Unreadable, Message: "the nodes could not be listed before the roll", Failures: base.Failures}
	}
	now, after := wait.Now, wait.After
	if now == nil {
		now = time.Now
	}
	if after == nil {
		after = time.After
	}
	bound := now().Add(nodesReadyBound)
	if !wait.Deadline.IsZero() && wait.Deadline.Before(bound) {
		bound = wait.Deadline
	}
	list, err := in.reads.Nodes()
	if err != nil {
		f := kubeFailure(in.Snapshot.Cluster.Name, "listing nodes after the roll", err)
		return Result{Outcome: Unreadable, Message: "could not list the nodes after the roll: " + f.Error, Failures: []apidoc.Failure{f}}
	}
	for {
		newNodes, notReady := newNodeState(list, nodegroups, base)
		if len(notReady) == 0 {
			return Result{Outcome: Pass, Message: fmt.Sprintf("%s ready", plural(newNodes, "new node"))}
		}
		if wait.Interval <= 0 || !now().Add(wait.Interval).Before(bound) {
			return Result{
				Outcome: Fail, Findings: notReady,
				Message: fmt.Sprintf("%d of %s not ready after the roll: %s", len(notReady), plural(newNodes, "new node"), strings.Join(notReady, ", ")),
			}
		}
		select {
		case <-after(wait.Interval):
		case <-ctx.Done():
			f := fleet.FailureFromError(apidoc.FailureCluster, in.Snapshot.Cluster.Name, ctx.Err())
			return Result{Outcome: Unreadable, Message: "the wait for the new nodes ended: " + f.Error, Failures: []apidoc.Failure{f}}
		}
		list, err = in.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			f := kubeFailure(in.Snapshot.Cluster.Name, "listing nodes after the roll", err)
			return Result{Outcome: Unreadable, Message: "could not list the nodes after the roll: " + f.Error, Failures: []apidoc.Failure{f}}
		}
	}
}

// newNodeState counts the new nodes of the nodegroups in list, and names
// the ones that are not Ready.
func newNodeState(list *corev1.NodeList, nodegroups []string, base Baseline) (newNodes int, notReady []string) {
	for i := range list.Items {
		node := &list.Items[i]
		if _, old := base.Nodes[node.Name]; old || !slices.Contains(nodegroups, node.Labels[labelNodegroup]) {
			continue
		}
		newNodes++
		if !nodeReady(node) {
			notReady = append(notReady, node.Name)
		}
	}
	sort.Strings(notReady)
	return newNodes, notReady
}

func runNewPendingPods(in *Input, base Baseline) Result {
	if !base.Taken {
		return Result{Outcome: Skipped, Message: "no baseline of the Pending pods before the roll"}
	}
	if len(base.Failures) > 0 {
		return Result{Outcome: Unreadable, Message: "the Pending pods could not be listed before the roll", Failures: base.Failures}
	}
	cluster := in.Snapshot.Cluster.Name
	// Both lists in one round trip.
	var (
		pods    *corev1.PodList
		podsErr error
	)
	together(
		func() { _, _ = in.reads.Nodes() },
		func() {
			pods, podsErr = in.Kube.CoreV1().Pods("").List(in.reads.ctx, metav1.ListOptions{FieldSelector: "status.phase=Pending"})
		},
	)
	nodes, err := in.reads.Nodes()
	if err != nil {
		f := kubeFailure(cluster, "listing nodes after the roll", err)
		return Result{Outcome: Unreadable, Message: "could not list the nodes after the roll: " + f.Error, Failures: []apidoc.Failure{f}}
	}
	if err := podsErr; err != nil {
		f := kubeFailure(cluster, "listing Pending pods after the roll", err)
		return Result{Outcome: Unreadable, Message: "could not list the Pending pods after the roll: " + f.Error, Failures: []apidoc.Failure{f}}
	}
	live := make(map[string]struct{}, len(nodes.Items))
	for i := range nodes.Items {
		live[nodes.Items[i].Name] = struct{}{}
	}
	var stuck []string
	for i := range pods.Items {
		key, ok := pendingKey(&pods.Items[i], live)
		if _, before := base.Pending[key]; ok && !before {
			stuck = append(stuck, key)
		}
	}
	sort.Strings(stuck)
	if len(stuck) == 0 {
		return Result{Outcome: Pass, Message: "no new Pending pods"}
	}
	shown := stuck
	if len(shown) > 5 {
		shown = shown[:5]
	}
	return Result{Outcome: Fail, Findings: stuck, Message: fmt.Sprintf("%s newly Pending after the roll: %s", plural(len(stuck), "pod"), strings.Join(shown, ", "))}
}
