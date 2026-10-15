package health

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// systemNamespaces are left out of the coverage count. "default" is not
// one of them: real workloads run there.
var systemNamespaces = map[string]bool{"kube-system": true, "kube-public": true, "kube-node-lease": true}

// pdb is one PodDisruptionBudget as the checks read it.
type pdb struct {
	namespace, name string
	allowed         int32
	healthy         int32
	expected        int32
	// unsynced is set when the disruption controller has not observed the
	// spec, or reports SyncFailed. The counts are then not to be trusted,
	// and the eviction API refuses the pods of the budget.
	unsynced bool
	// selector is nil when the spec has no selector, which matches no
	// pod. An empty selector matches every pod of the namespace.
	selector    labels.Selector
	alwaysAllow bool
	healthyOK   bool // the budget is met: currentHealthy >= desiredHealthy > 0
}

func newPDB(in policyv1.PodDisruptionBudget) pdb {
	p := pdb{
		namespace: in.Namespace, name: in.Name,
		allowed: in.Status.DisruptionsAllowed, healthy: in.Status.CurrentHealthy, expected: in.Status.ExpectedPods,
		unsynced:  in.Status.ObservedGeneration < in.Generation,
		healthyOK: in.Status.DesiredHealthy > 0 && in.Status.CurrentHealthy >= in.Status.DesiredHealthy,
	}
	if c := meta.FindStatusCondition(in.Status.Conditions, policyv1.DisruptionAllowedCondition); c != nil && c.Reason == policyv1.SyncFailedReason {
		p.unsynced = true
	}
	if policy := in.Spec.UnhealthyPodEvictionPolicy; policy != nil && *policy == policyv1.AlwaysAllow {
		p.alwaysAllow = true
	}
	if in.Spec.Selector != nil {
		// A selector that does not parse matches no pod, as in the
		// eviction API.
		p.selector, _ = metav1.LabelSelectorAsSelector(in.Spec.Selector)
	}
	return p
}

func (p pdb) id() string { return p.namespace + "/" + p.name }

// atRisk reports whether the budget allows no disruption while it may
// cover pods. expected 0 means "no pod" only when the status is synced.
func (p pdb) atRisk() bool {
	return p.allowed <= 0 && (p.expected > 0 || p.unsynced)
}

// allowedNow is the number of pods the budget lets go: 0 when it is not
// synced, since the eviction API then refuses its pods.
func (p pdb) allowedNow() int32 {
	if p.unsynced {
		return 0
	}
	return max(p.allowed, 0)
}

func (p pdb) summary() string {
	if p.unsynced {
		return p.id() + " (status not synced, 0 disruptions allowed; evictions are refused)"
	}
	return fmt.Sprintf("%s (%d/%d pods healthy, 0 disruptions allowed)", p.id(), p.healthy, p.expected)
}

// matches reports whether the selector of p selects the pod.
func (p pdb) matches(pod *corev1.Pod) bool {
	return p.selector != nil && p.selector.Matches(labels.Set(pod.Labels))
}

// gates reports whether the eviction API would consult p for the pod: the
// pod is one that eviction looks at, and either it is Ready, or the
// budget holds a not-Ready pod under IfHealthyBudget while it is not met.
func (p pdb) gates(pod *corev1.Pod) bool {
	switch {
	case !p.matches(pod) || evictionIgnores(pod):
		return false
	case podReady(pod):
		return true
	case p.alwaysAllow:
		return false
	}
	return !p.healthyOK
}

// evictionIgnores reports whether the eviction API deletes the pod
// without a look at any budget: a pod that is done, Pending, or already
// going.
func evictionIgnores(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return true
	}
	switch pod.Status.Phase {
	case corev1.PodSucceeded, corev1.PodFailed, corev1.PodPending:
		return true
	}
	return false
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// DrainBlockers checks what would stop EKS draining the nodes of the
// target nodegroups: a budget that allows no disruption while it covers a
// pod there, and a pod that more than one budget selects, which the
// eviction API refuses before it looks at any budget. Without targets it
// reports every such budget and pod in the cluster. --force rolls past it.
func DrainBlockers() Check {
	return Check{Name: "DrainBlockers", Blocking: true, FailClosed: true, Forceable: true, Needs: Needs{Kube: true}, Run: runDrainBlockers}
}

func runDrainBlockers(_ context.Context, in *Input) Result {
	cluster := in.Snapshot.Cluster.Name
	if len(in.Targets) > 0 {
		together(func() { _, _ = in.reads.PDBs() }, func() { _, _ = in.reads.Nodes() })
	}
	list, err := in.reads.PDBs()
	if err != nil {
		f := kubeFailure(cluster, "listing PodDisruptionBudgets", err)
		return Result{Outcome: Unreadable, Message: "could not list the PodDisruptionBudgets: " + f.Error, Failures: []apidoc.Failure{f}}
	}
	budgets := make([]pdb, len(list.Items))
	for i, p := range list.Items {
		budgets[i] = newPDB(p)
	}
	targets, scoped, r := targetNodes(in)
	if r != nil {
		return *r
	}
	return findDrainBlockers(in, budgets, targets, scoped, true)
}

// targetNodes returns the nodes of the target nodegroups, and whether the
// check is scoped to them. With targets whose nodes cannot be found the
// check falls back to the whole cluster, unless the targets have nothing
// to drain, which is a Pass. A failed node list is a result of its own.
func targetNodes(in *Input) (nodes map[string]bool, scoped bool, done *Result) {
	if len(in.Targets) == 0 {
		return nil, false, nil
	}
	list, err := in.reads.Nodes()
	if err != nil {
		f := kubeFailure(in.Snapshot.Cluster.Name, "listing nodes", err)
		return nil, false, &Result{Outcome: Unreadable, Message: "could not list the nodes: " + f.Error, Failures: []apidoc.Failure{f}}
	}
	nodes = map[string]bool{}
	for i := range list.Items {
		if slices.Contains(in.Targets, list.Items[i].Labels[labelNodegroup]) {
			nodes[list.Items[i].Name] = true
		}
	}
	if len(nodes) > 0 {
		return nodes, true, nil
	}
	if desired, known := targetsDesired(in.Snapshot, in.Targets); known && desired == 0 {
		return nil, true, &Result{Outcome: Pass, Message: "the target nodegroups have no nodes; nothing to drain"}
	}
	return nil, false, nil
}

// targetsDesired sums the desired size of the targets from the snapshot.
// It is not known when a target was not read.
func targetsDesired(snap *fleet.Snapshot, targets []string) (int32, bool) {
	var total int32
	for _, name := range targets {
		i := slices.IndexFunc(snap.Nodegroups, func(n fleet.Nodegroup) bool { return n.Name == name })
		if i < 0 || snap.Nodegroups[i].State != fleet.Collected {
			return 0, false
		}
		total += snap.Nodegroups[i].Desired
	}
	return total, true
}

// findDrainBlockers is the rule over the budgets. Scoped, it reads the
// pods of each namespace that has an at-risk budget, to see whether the
// budget gates a pod on a target node; unscoped, an at-risk budget counts
// without a pod read, as the old check counted it. With withMulti it also
// names the pods that more than one budget selects, which needs the pods
// of each namespace with more than one budget; a scale-down evicts
// nothing and leaves that rule out.
func findDrainBlockers(in *Input, budgets []pdb, targets map[string]bool, scoped, withMulti bool) Result {
	byNamespace := groupByNamespace(budgets)
	var (
		blockers []string
		multi    []string
		failures []apidoc.Failure
		unread   []string
		reads    []func()
	)
	needsPods := func(ns string) bool {
		return (scoped && slices.ContainsFunc(byNamespace[ns], pdb.atRisk)) || (withMulti && len(byNamespace[ns]) > 1)
	}
	namespaces := slices.Sorted(maps.Keys(byNamespace))
	// The pods of every namespace that needs them, in one round trip.
	for _, ns := range namespaces {
		if needsPods(ns) {
			reads = append(reads, func() { _, _ = in.reads.Pods(ns) })
		}
	}
	together(reads...)
	for _, ns := range namespaces {
		list := byNamespace[ns]
		var pods *corev1.PodList
		if needsPods(ns) {
			var err error
			if pods, err = in.reads.Pods(ns); err != nil {
				failures = append(failures, kubeFailure(in.Snapshot.Cluster.Name, "listing pods in "+ns, err))
				unread = append(unread, ns)
				pods = nil
			}
		}
		for _, p := range list {
			switch {
			case !p.atRisk():
			case !scoped:
				blockers = append(blockers, p.summary())
			case pods != nil && slices.ContainsFunc(pods.Items, func(pod corev1.Pod) bool { return targets[pod.Spec.NodeName] && p.gates(&pod) }):
				blockers = append(blockers, p.summary())
			}
		}
		if withMulti && len(list) > 1 && pods != nil {
			multi = append(multi, multiPDBPods(list, pods.Items, targets, scoped)...)
		}
	}
	return drainBlockersResult(blockers, multi, unread, failures, scoped)
}

// multiPDBPods names the pods of a namespace that more than one of its
// budgets selects, on the target nodes when scoped. A pod that eviction
// ignores does not count; its readiness does not matter.
func multiPDBPods(list []pdb, pods []corev1.Pod, targets map[string]bool, scoped bool) []string {
	var out []string
	for i := range pods {
		pod := &pods[i]
		if (scoped && !targets[pod.Spec.NodeName]) || evictionIgnores(pod) {
			continue
		}
		var matched []string
		for _, p := range list {
			if p.matches(pod) {
				matched = append(matched, p.name)
			}
		}
		if len(matched) > 1 {
			out = append(out, fmt.Sprintf("pod %s/%s is covered by %d PDBs (%s); the eviction API refuses such pods", pod.Namespace, pod.Name, len(matched), strings.Join(matched, ", ")))
		}
	}
	return out
}

// drainBlockersResult words the outcome of the drain-blocker check.
func drainBlockersResult(blockers, multi, unread []string, failures []apidoc.Failure, scoped bool) Result {
	r := Result{Failures: failures}
	var blocking []string
	for _, b := range blockers {
		r.Details = append(r.Details, "drain blocker: "+b)
		name, _, _ := strings.Cut(b, " (")
		r.Findings = append(r.Findings, name)
		blocking = append(blocking, name)
	}
	slices.Sort(blocking)
	for _, m := range multi {
		r.Details = append(r.Details, "drain blocker: "+m)
		name, _, _ := strings.Cut(strings.TrimPrefix(m, "pod "), " is covered")
		r.Findings = append(r.Findings, name)
	}
	slices.Sort(r.Findings)
	switch {
	case len(blockers)+len(multi) > 0:
		var parts []string
		if len(blockers) > 0 {
			if len(blocking) == 1 {
				parts = append(parts, "PDB "+blocking[0]+" allows 0 disruptions")
			} else {
				parts = append(parts, "PDBs "+strings.Join(blocking, ", ")+" allow 0 disruptions")
			}
		}
		if len(multi) > 0 {
			parts = append(parts, plural(len(multi), "pod")+" covered by more than one PDB")
		}
		r.Outcome = Fail
		r.Message = strings.Join(parts, " and ")
		if scoped {
			r.Message += " on the target nodegroups; the roll would stall on eviction"
		} else {
			r.Message += "; a drain would stall on eviction"
		}
		r.Details = append(r.Details, "let the workloads recover, relax the PDBs, or narrow their selectors so each pod matches one PDB")
	case len(unread) > 0:
		r.Outcome, r.Message = Unreadable, "could not list the pods of "+strings.Join(unread, ", ")
	default:
		r.Outcome, r.Message = Pass, "no PodDisruptionBudget blocks a drain"
	}
	if len(unread) > 0 && r.Outcome == Fail {
		r.Message += "; not read: " + strings.Join(unread, ", ")
	}
	return r
}

// ScaleDownBlockers is the drain-blocker check for a scale-down of
// nodegroup by remove nodes. A scale-down terminates nodes without an
// eviction, and the Auto Scaling group picks which: a budget counts when
// the removed nodes that hold the most of its covered pods hold more than
// it allows to be disrupted.
func ScaleDownBlockers(nodegroup string, remove int32) Check {
	return Check{Name: "DrainBlockers", Blocking: true, FailClosed: true, Forceable: true, Needs: Needs{Kube: true},
		Run: func(_ context.Context, in *Input) Result { return runScaleDown(in, nodegroup, remove) }}
}

func runScaleDown(in *Input, nodegroup string, remove int32) Result {
	if remove <= 0 {
		return Result{Outcome: Pass, Message: "a scale-up removes no node"}
	}
	cluster := in.Snapshot.Cluster.Name
	list, err := in.reads.PDBs()
	if err != nil {
		f := kubeFailure(cluster, "listing PodDisruptionBudgets", err)
		return Result{Outcome: Unreadable, Message: "could not list the PodDisruptionBudgets: " + f.Error, Failures: []apidoc.Failure{f}}
	}
	scoped := *in
	scoped.Targets = []string{nodegroup}
	nodes, isScoped, done := targetNodes(&scoped)
	if done != nil {
		return *done
	}
	budgets := make([]pdb, len(list.Items))
	for i, p := range list.Items {
		budgets[i] = newPDB(p)
	}
	if !isScoped {
		// Without the nodes of the nodegroup the rule of a roll applies:
		// every budget that allows nothing counts. A scale-down evicts no
		// pod, so a pod with several budgets is not a blocker.
		return findDrainBlockers(&scoped, budgets, nil, false, false)
	}
	// The pods of every namespace with a budget, in one round trip.
	var reads []func()
	for _, ns := range slices.Sorted(maps.Keys(groupByNamespace(budgets))) {
		reads = append(reads, func() { _, _ = in.reads.Pods(ns) })
	}
	together(reads...)
	r := Result{}
	for _, p := range budgets {
		pods, err := in.reads.Pods(p.namespace)
		if err != nil {
			f := kubeFailure(cluster, "listing pods in "+p.namespace, err)
			return Result{Outcome: Unreadable, Message: "could not list the pods of " + p.namespace + ": " + f.Error, Failures: []apidoc.Failure{f}}
		}
		perNode := map[string]int32{}
		for i := range pods.Items {
			pod := &pods.Items[i]
			if nodes[pod.Spec.NodeName] && p.gates(pod) {
				perNode[pod.Spec.NodeName]++
			}
		}
		if loss := worstCaseLoss(perNode, int(remove)); loss > p.allowedNow() {
			r.Findings = append(r.Findings, p.id())
			r.Details = append(r.Details, fmt.Sprintf("drain blocker: %s (%d/%d pods healthy, %d allowed; covered pods run on %d of the nodes, so removing %d can take down %d)",
				p.id(), p.healthy, p.expected, p.allowedNow(), len(perNode), remove, loss))
		}
	}
	if len(r.Findings) == 0 {
		r.Outcome, r.Message = Pass, fmt.Sprintf("no PodDisruptionBudget stops removing %s from %s", plural(int(remove), "node"), nodegroup)
		return r
	}
	r.Outcome = Fail
	budgetsWord := "PDB " + r.Findings[0] + " allows"
	if len(r.Findings) > 1 {
		budgetsWord = "PDBs " + strings.Join(r.Findings, ", ") + " allow"
	}
	r.Message = fmt.Sprintf("removing %s from %s could evict more pods than %s", plural(int(remove), "node"), nodegroup, budgetsWord)
	return r
}

// groupByNamespace returns the budgets by namespace.
func groupByNamespace(budgets []pdb) map[string][]pdb {
	out := map[string][]pdb{}
	for _, p := range budgets {
		out[p.namespace] = append(out[p.namespace], p)
	}
	return out
}

// worstCaseLoss returns the number of pods on the k nodes of perNode that
// hold the most pods.
func worstCaseLoss(perNode map[string]int32, k int) int32 {
	counts := slices.Sorted(func(yield func(int32) bool) {
		for _, c := range perNode {
			if !yield(c) {
				return
			}
		}
	})
	slices.Reverse(counts)
	var loss int32
	for _, c := range counts[:min(k, len(counts))] {
		loss += c
	}
	return loss
}

// PDBCoverage checks that every deployment outside the system namespaces
// has a PodDisruptionBudget. It is advisory.
func PDBCoverage() Check {
	return Check{Name: "PDBCoverage", Needs: Needs{Kube: true}, Run: runPDBCoverage}
}

func runPDBCoverage(_ context.Context, in *Input) Result {
	cluster := in.Snapshot.Cluster.Name
	together(func() { _, _ = in.reads.PDBs() }, func() { _, _ = in.reads.Deployments() })
	budgets, err := in.reads.PDBs()
	if err != nil {
		f := kubeFailure(cluster, "listing PodDisruptionBudgets", err)
		return Result{Outcome: Unreadable, Message: "could not list the PodDisruptionBudgets: " + f.Error, Failures: []apidoc.Failure{f}}
	}
	deployments, err := in.reads.Deployments()
	if err != nil {
		f := kubeFailure(cluster, "listing deployments", err)
		return Result{Outcome: Unreadable, Message: "could not list the deployments: " + f.Error, Failures: []apidoc.Failure{f}}
	}
	selectors := map[string][]labels.Selector{}
	for _, p := range budgets.Items {
		if p.Spec.Selector == nil {
			continue
		}
		if sel, err := metav1.LabelSelectorAsSelector(p.Spec.Selector); err == nil {
			selectors[p.Namespace] = append(selectors[p.Namespace], sel)
		}
	}
	total := 0
	var uncovered []string
	for _, d := range deployments.Items {
		if systemNamespaces[d.Namespace] {
			continue
		}
		total++
		podLabels := labels.Set(d.Spec.Template.Labels)
		if !slices.ContainsFunc(selectors[d.Namespace], func(sel labels.Selector) bool { return sel.Matches(podLabels) }) {
			uncovered = append(uncovered, d.Namespace+"/"+d.Name)
		}
	}
	slices.Sort(uncovered)
	switch {
	case total == 0:
		return Result{Outcome: Pass, Message: "no user deployments"}
	case len(uncovered) == 0:
		return Result{Outcome: Pass, Message: fmt.Sprintf("every deployment has a PodDisruptionBudget (%d)", total)}
	}
	shown := uncovered
	if len(shown) > 5 {
		shown = shown[:5]
	}
	return Result{
		Outcome: Warn, Findings: uncovered,
		Message: fmt.Sprintf("%d of %s without a PodDisruptionBudget: %s", len(uncovered), plural(total, "deployment"), strings.Join(shown, ", ")),
		Details: []string{"a deployment without a budget can lose every pod at once when its nodes drain"},
	}
}
