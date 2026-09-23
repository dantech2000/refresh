package health

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/dantech2000/refresh/internal/services/common"
)

// PDBInfo is a structured snapshot of one PodDisruptionBudget's disruption
// status, used by `nodegroup scale --dry-run` to show which PDBs would
// constrain a scale-down. (REF-4)
type PDBInfo struct {
	Namespace          string `json:"namespace" yaml:"namespace"`
	Name               string `json:"name" yaml:"name"`
	DisruptionsAllowed int32  `json:"disruptionsAllowed" yaml:"disruptionsAllowed"`
	CurrentHealthy     int32  `json:"currentHealthy" yaml:"currentHealthy"`
	DesiredHealthy     int32  `json:"desiredHealthy" yaml:"desiredHealthy"`
	ExpectedPods       int32  `json:"expectedPods" yaml:"expectedPods"`
	// StatusNotSynced is set when the disruption controller has not observed
	// the PDB's latest spec, or reports the DisruptionAllowed condition with
	// reason SyncFailed (bare pods, custom resources without a /scale
	// subresource). The counts are then not trustworthy: the controller leaves
	// ExpectedPods at 0, but the eviction API still refuses the PDB's pods.
	StatusNotSynced bool `json:"statusNotSynced,omitempty" yaml:"statusNotSynced,omitempty"`
}

// AtRisk reports whether this PDB currently allows zero voluntary disruptions
// while it may cover pods, meaning a node drain (scale-down or node roll) that
// evicts one of its pods will be blocked until the workload recovers.
// ExpectedPods == 0 means "matches no pods" only when the status is synced; an
// unsynced PDB that allows 0 disruptions is at risk whatever it counts.
func (p PDBInfo) AtRisk() bool {
	return p.DisruptionsAllowed <= 0 && (p.ExpectedPods > 0 || p.StatusNotSynced)
}

// systemNamespaces are skipped when counting deployments for PDB coverage.
// "default" is deliberately absent: real workloads run
// there and must be counted.
var systemNamespaces = map[string]bool{
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
}

// SetTargetNodegroups scopes the PDB drain-blocker check to the managed
// nodegroups about to roll: a zero-disruption PDB is only reported if one of
// its pods runs on a node of these nodegroups. Without it the check is
// cluster-wide and reports every such PDB as one that may block a drain.
func (hc *HealthChecker) SetTargetNodegroups(names []string) { hc.targetNodegroups = names }

func pdbInfoFrom(pdb policyv1.PodDisruptionBudget) PDBInfo {
	return PDBInfo{
		Namespace:          pdb.Namespace,
		Name:               pdb.Name,
		DisruptionsAllowed: pdb.Status.DisruptionsAllowed,
		CurrentHealthy:     pdb.Status.CurrentHealthy,
		DesiredHealthy:     pdb.Status.DesiredHealthy,
		ExpectedPods:       pdb.Status.ExpectedPods,
		StatusNotSynced:    !pdbStatusSynced(pdb),
	}
}

// pdbStatusSynced reports whether the disruption controller has observed the
// PDB's current spec and computed its status without a sync failure.
func pdbStatusSynced(pdb policyv1.PodDisruptionBudget) bool {
	if pdb.Status.ObservedGeneration < pdb.Generation {
		return false
	}
	c := meta.FindStatusCondition(pdb.Status.Conditions, policyv1.DisruptionAllowedCondition)
	return c == nil || c.Reason != policyv1.SyncFailedReason
}

// ListPodDisruptionBudgets returns a structured snapshot of every PDB in the
// cluster with its current disruption status. System namespaces are included:
// a stuck kube-system PDB (e.g. coredns) blocks a drain just like a user one.
// Returns (nil, nil) when no Kubernetes client is configured so callers can
// degrade gracefully. (REF-4)
func (hc *HealthChecker) ListPodDisruptionBudgets(ctx context.Context) ([]PDBInfo, error) {
	if hc.k8sClient == nil {
		return nil, nil
	}
	pdbs, err := hc.k8sClient.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing PodDisruptionBudgets: %w", err)
	}
	out := make([]PDBInfo, 0, len(pdbs.Items))
	for _, pdb := range pdbs.Items {
		out = append(out, pdbInfoFrom(pdb))
	}
	return out, nil
}

// CheckPodDisruptionBudgets validates PDB configuration for user workloads:
// it flags PDBs that would block a node drain right now, and measures how many
// deployments are covered by a PDB at all.
func (hc *HealthChecker) CheckPodDisruptionBudgets(ctx context.Context) HealthResult {
	return hc.checkPodDisruptionBudgets(ctx, "")
}

// checkPodDisruptionBudgets is CheckPodDisruptionBudgets for clusterName.
// The cluster name lets the scoped drain-blocker check read the target
// nodegroups' desired size when none of their nodes are found.
func (hc *HealthChecker) checkPodDisruptionBudgets(ctx context.Context, clusterName string) HealthResult {
	result := HealthResult{
		Name:       "Pod Disruption Budgets",
		IsBlocking: false, // PDBs are warning-level, not blocking
		Details:    []string{},
	}

	if hc.k8sClient == nil {
		result.Status = StatusWarn
		result.Score = 70
		result.Skipped = true // excluded from OverallScore — not measured
		result.Message = "Kubernetes client not available, skipping PDB check"
		result.Details = append(result.Details, "Install kubectl and configure cluster access to enable this check")
		return result
	}

	// Get all PDBs in the cluster
	pdbs, err := hc.k8sClient.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		result.Status = StatusWarn
		result.Score = 60
		result.Message = fmt.Sprintf("Failed to list PDBs: %v", err)
		return result
	}

	blockerPDBs, scoped, note := hc.findDrainBlockers(ctx, clusterName, hc.targetNodegroups, pdbs.Items)
	drainBlockers := make([]string, 0, len(blockerPDBs))
	for _, b := range blockerPDBs {
		drainBlockers = append(drainBlockers, b.DrainBlockerSummary())
	}
	if note != "" {
		result.Details = append(result.Details, note)
	}

	namespaces, err := hc.k8sClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		result.Status = StatusWarn
		result.Score = 60
		result.Message = fmt.Sprintf("Failed to list namespaces: %v", err)
		// applyDrainBlockers replaces Message, so keep the error in Details.
		result.Details = append(result.Details, result.Message)
		applyDrainBlockers(&result, drainBlockers, scoped)
		return result
	}

	totalDeployments := 0
	protectedDeployments := 0
	var unprotectedDeployments []string

	// Group PDB selectors by namespace so each deployment can be matched
	// against the PDBs that could actually cover its pods. Counting PDBs as
	// "protected deployments" (the old behavior) over- or under-counted
	// whenever the two quantities differed.
	pdbSelectorsByNamespace := make(map[string][]labels.Selector)
	for _, pdb := range pdbs.Items {
		sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil {
			continue
		}
		pdbSelectorsByNamespace[pdb.Namespace] = append(pdbSelectorsByNamespace[pdb.Namespace], sel)
	}

	// Check each user namespace
	for _, ns := range namespaces.Items {
		if systemNamespaces[ns.Name] {
			continue
		}

		deployments, err := hc.k8sClient.AppsV1().Deployments(ns.Name).List(ctx, metav1.ListOptions{})
		if err != nil {
			result.Details = append(result.Details, fmt.Sprintf("Failed to list deployments in %s: %v", ns.Name, err))
			continue
		}

		nsDeployments := len(deployments.Items)
		nsProtected := 0
		selectors := pdbSelectorsByNamespace[ns.Name]

		for _, deployment := range deployments.Items {
			podLabels := labels.Set(deployment.Spec.Template.Labels)
			covered := false
			for _, sel := range selectors {
				if sel.Matches(podLabels) {
					covered = true
					break
				}
			}
			if covered {
				nsProtected++
			} else {
				unprotectedDeployments = append(unprotectedDeployments, fmt.Sprintf("%s/%s", ns.Name, deployment.Name))
			}
		}

		totalDeployments += nsDeployments
		protectedDeployments += nsProtected

		if nsDeployments > 0 {
			result.Details = append(result.Details, fmt.Sprintf("%s: %d/%d deployments covered by PDBs", ns.Name, nsProtected, nsDeployments))
		}
	}

	// Calculate score and status from deployment coverage.
	switch {
	case totalDeployments == 0:
		result.Status = StatusPass
		result.Score = 100
		result.Message = "No user deployments found"
	case len(unprotectedDeployments) == 0:
		result.Status = StatusPass
		result.Score = 100
		result.Message = fmt.Sprintf("All %d deployments have PDB protection", totalDeployments)
	default:
		// For PDBs, we're more lenient - it's a warning, not a failure
		pdbCoveragePercentage := (protectedDeployments * 100) / totalDeployments
		result.Score = pdbCoveragePercentage
		result.Status = StatusWarn // Still warning, not fail
		if pdbCoveragePercentage >= 50 {
			result.Message = fmt.Sprintf("%d deployments missing PDBs", len(unprotectedDeployments))
			if len(unprotectedDeployments) <= 5 {
				result.Details = append(result.Details, fmt.Sprintf("Unprotected: %v", unprotectedDeployments))
			} else {
				result.Details = append(result.Details, fmt.Sprintf("Unprotected: %v... (+%d more)", unprotectedDeployments[:5], len(unprotectedDeployments)-5))
			}
		} else {
			result.Message = fmt.Sprintf("%d/%d deployments missing PDBs", len(unprotectedDeployments), totalDeployments)
			result.Details = append(result.Details, "Consider creating PDBs for critical workloads")
		}
	}

	applyDrainBlockers(&result, drainBlockers, scoped)
	return result
}

// findDrainBlockers returns the PDBs that allow zero disruptions while
// covering pods. Such a PDB blocks every eviction of its pods, so a node roll
// stalls on drain and EKS eventually fails the update with PodEvictionFailure.
// System namespaces are included on purpose: a stuck kube-system PDB blocks a
// drain just the same.
//
// When targets is non-empty, a PDB only counts if it gates the eviction of at
// least one pod on a node of those nodegroups, and scoped is true. Otherwise
// every at-risk PDB is reported and scoped is false. That fallback also applies
// when the node list fails, or when no node carries a target nodegroup label:
// the check fails open instead of passing on nothing. The one exception is
// targets whose total desired size is 0 (read with DescribeNodegroup for
// clusterName): they have nothing to drain, so no PDB blocks them, and note
// says so.
func (hc *HealthChecker) findDrainBlockers(ctx context.Context, clusterName string, targets []string, pdbs []policyv1.PodDisruptionBudget) (blockers []PDBInfo, scoped bool, note string) {
	var targetNodes map[string]bool
	if len(targets) > 0 {
		nodes, err := hc.k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s in (%s)", nodeLabelNodegroup, strings.Join(targets, ",")),
		})
		switch {
		case err == nil && len(nodes.Items) > 0:
			scoped = true
			targetNodes = make(map[string]bool, len(nodes.Items))
			for _, n := range nodes.Items {
				targetNodes[n.Name] = true
			}
		case err == nil:
			if desired, derr := hc.targetsDesiredSize(ctx, clusterName, targets); derr == nil && desired == 0 {
				return nil, true, "Target nodegroup(s) have no nodes; nothing to drain"
			}
		}
	}

	podsByNamespace := make(map[string][]corev1.Pod)
	for _, pdb := range pdbs {
		info := pdbInfoFrom(pdb)
		if !info.AtRisk() {
			continue
		}
		if scoped && !hc.pdbCoversTargetNode(ctx, pdb, targetNodes, podsByNamespace) {
			continue
		}
		blockers = append(blockers, info)
	}
	return blockers, scoped, ""
}

// DrainBlockerSummary describes p as a drain blocker, e.g.
// "ns/name (1/1 pods healthy, 0 disruptions allowed)".
func (p PDBInfo) DrainBlockerSummary() string {
	if p.StatusNotSynced {
		return fmt.Sprintf("%s/%s (PDB status not synced, 0 disruptions allowed; evictions are refused)", p.Namespace, p.Name)
	}
	return fmt.Sprintf("%s/%s (%d/%d pods healthy, 0 disruptions allowed)", p.Namespace, p.Name, p.CurrentHealthy, p.ExpectedPods)
}

// ErrNoKubeClient is returned by DrainBlockers when no Kubernetes client is
// configured, so PDBs can't be read.
var ErrNoKubeClient = errors.New("no Kubernetes client configured")

// DrainBlockerReport is the result of DrainBlockers.
type DrainBlockerReport struct {
	// Blockers are the PDBs that allow 0 disruptions and would refuse an
	// eviction from the nodes being drained.
	Blockers []PDBInfo
	// Scoped is true when Blockers was narrowed to pods on the target
	// nodegroups' nodes. False means the check fell back to every at-risk
	// PDB in the cluster.
	Scoped bool
	// Note explains a short-circuit, such as targets with nothing to drain.
	Note string
}

// DrainBlockers lists the PDBs that would block draining the nodes of the
// given managed nodegroups, with the same scoping rules as the pre-flight PDB
// check (see findDrainBlockers). Unlike SetTargetNodegroups it leaves the
// checker unchanged. It returns ErrNoKubeClient without a Kubernetes client
// and an error when the PDBs can't be listed: a caller that gates on the
// result must not treat "couldn't check" as "no blockers".
func (hc *HealthChecker) DrainBlockers(ctx context.Context, clusterName string, nodegroups []string) (DrainBlockerReport, error) {
	if hc.k8sClient == nil {
		return DrainBlockerReport{}, ErrNoKubeClient
	}
	pdbs, err := hc.k8sClient.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return DrainBlockerReport{}, fmt.Errorf("listing PodDisruptionBudgets: %w", err)
	}
	blockers, scoped, note := hc.findDrainBlockers(ctx, clusterName, nodegroups, pdbs.Items)
	return DrainBlockerReport{Blockers: blockers, Scoped: scoped, Note: note}, nil
}

// nodegroupDescriber is the slice of the EKS API that reads a nodegroup's
// scaling config.
type nodegroupDescriber interface {
	DescribeNodegroup(ctx context.Context, params *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
}

// targetsDesiredSize sums the desired size of the target nodegroups. It
// returns an error when the size can't be known (no EKS client or cluster
// name, a failed call, or a nodegroup without a scaling config), so callers
// can fail open.
func (hc *HealthChecker) targetsDesiredSize(ctx context.Context, clusterName string, targets []string) (int32, error) {
	if hc.ngDescriber == nil || clusterName == "" {
		return 0, fmt.Errorf("no EKS client or cluster name to describe the target nodegroups")
	}
	var total int32
	for _, ng := range targets {
		out, err := common.WithRetry(ctx, common.DefaultRetryConfig,
			func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
				return hc.ngDescriber.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
					ClusterName:   aws.String(clusterName),
					NodegroupName: aws.String(ng),
				})
			})
		if err != nil {
			return 0, fmt.Errorf("describing nodegroup %s: %w", ng, err)
		}
		if out == nil || out.Nodegroup == nil || out.Nodegroup.ScalingConfig == nil {
			return 0, fmt.Errorf("nodegroup %s has no scaling config", ng)
		}
		total += aws.ToInt32(out.Nodegroup.ScalingConfig.DesiredSize)
	}
	return total, nil
}

// pdbCoversTargetNode reports whether pdb gates the eviction of any pod on one
// of targetNodes. Pods are listed once per namespace and cached in
// podsByNamespace. If the pods can't be listed it returns true, so a blocker
// is never hidden by a transient API error.
func (hc *HealthChecker) pdbCoversTargetNode(ctx context.Context, pdb policyv1.PodDisruptionBudget, targetNodes map[string]bool, podsByNamespace map[string][]corev1.Pod) bool {
	sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
	if err != nil {
		return true
	}
	pods, ok := podsByNamespace[pdb.Namespace]
	if !ok {
		list, err := hc.k8sClient.CoreV1().Pods(pdb.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return true
		}
		pods = list.Items
		podsByNamespace[pdb.Namespace] = pods
	}
	for _, p := range pods {
		if targetNodes[p.Spec.NodeName] && sel.Matches(labels.Set(p.Labels)) && evictionGatedByPDB(p, pdb) {
			return true
		}
	}
	return false
}

// evictionGatedByPDB mirrors the eviction API's rules for when pdb can refuse
// the eviction of pod. The API ignores PDBs for pods that are Succeeded,
// Failed, Pending or already being deleted. It also lets a not-Ready pod go
// when unhealthyPodEvictionPolicy is AlwaysAllow, or, under the default
// IfHealthyBudget policy, while the budget is met (currentHealthy >=
// desiredHealthy > 0).
func evictionGatedByPDB(pod corev1.Pod, pdb policyv1.PodDisruptionBudget) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}
	switch pod.Status.Phase {
	case corev1.PodSucceeded, corev1.PodFailed, corev1.PodPending:
		return false
	}
	if podReady(pod) {
		return true
	}
	if p := pdb.Spec.UnhealthyPodEvictionPolicy; p != nil && *p == policyv1.AlwaysAllow {
		return false
	}
	st := pdb.Status
	return st.DesiredHealthy <= 0 || st.CurrentHealthy < st.DesiredHealthy
}

// podReady reports whether pod has the Ready condition set to True.
func podReady(pod corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// applyDrainBlockers folds drain blockers into result. A blocker outranks
// coverage: full coverage is no comfort if a PDB will stop the roll. It stays
// WARN (non-blocking) so existing pipelines are not hard-stopped;
// --require-healthy escalates WARN to a hard stop.
func applyDrainBlockers(result *HealthResult, blockers []string, scoped bool) {
	if len(blockers) == 0 {
		return
	}
	result.Status = StatusWarn
	result.Score = min(result.Score, 50)
	if scoped {
		result.Message = fmt.Sprintf("%d PDB(s) allow 0 disruptions on the target nodegroup(s); node roll will stall on eviction", len(blockers))
	} else {
		result.Message = fmt.Sprintf("%d PDB(s) allow 0 disruptions and may block a drain", len(blockers))
	}
	for _, b := range blockers {
		result.Details = append(result.Details, "Drain blocker: "+b)
	}
	result.Details = append(result.Details, "Scale up the workload or relax minAvailable/maxUnavailable before rolling nodes")
}
