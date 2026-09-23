package health

import (
	"context"
	"fmt"

	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
}

// AtRisk reports whether this PDB currently allows zero voluntary disruptions
// while covering at least one pod, meaning a node drain (scale-down or node
// roll) that evicts one of its pods will be blocked until the workload
// recovers. A PDB that matches no pods (ExpectedPods == 0) blocks nothing.
func (p PDBInfo) AtRisk() bool { return p.DisruptionsAllowed <= 0 && p.ExpectedPods > 0 }

// systemNamespaces are skipped when counting deployments for PDB coverage and
// when listing user PDBs. "default" is deliberately absent: real workloads run
// there and must be counted.
var systemNamespaces = map[string]bool{
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
}

func pdbInfoFrom(pdb policyv1.PodDisruptionBudget) PDBInfo {
	return PDBInfo{
		Namespace:          pdb.Namespace,
		Name:               pdb.Name,
		DisruptionsAllowed: pdb.Status.DisruptionsAllowed,
		CurrentHealthy:     pdb.Status.CurrentHealthy,
		DesiredHealthy:     pdb.Status.DesiredHealthy,
		ExpectedPods:       pdb.Status.ExpectedPods,
	}
}

// ListPodDisruptionBudgets returns a structured snapshot of every PDB in user
// namespaces with its current disruption status. Returns (nil, nil) when no
// Kubernetes client is configured so callers can degrade gracefully. (REF-4)
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
		if systemNamespaces[pdb.Namespace] {
			continue
		}
		out = append(out, pdbInfoFrom(pdb))
	}
	return out, nil
}

// CheckPodDisruptionBudgets validates PDB configuration for user workloads:
// it flags PDBs that would block a node drain right now, and measures how many
// deployments are covered by a PDB at all.
func (hc *HealthChecker) CheckPodDisruptionBudgets(ctx context.Context) HealthResult {
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

	// A PDB that currently allows zero disruptions blocks every eviction of
	// the pods it covers, so a node roll stalls on drain and EKS eventually
	// fails the update with PodEvictionFailure. System namespaces are included
	// on purpose: a stuck kube-system PDB blocks a drain just the same.
	var drainBlockers []string
	for _, pdb := range pdbs.Items {
		if info := pdbInfoFrom(pdb); info.AtRisk() {
			drainBlockers = append(drainBlockers, fmt.Sprintf("%s/%s (%d/%d pods healthy, 0 disruptions allowed)",
				info.Namespace, info.Name, info.CurrentHealthy, info.ExpectedPods))
		}
	}

	namespaces, err := hc.k8sClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		result.Status = StatusWarn
		result.Score = 60
		result.Message = fmt.Sprintf("Failed to list namespaces: %v", err)
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

	// A drain blocker outranks coverage: full coverage is no comfort if a PDB
	// will stop the roll. It stays WARN (non-blocking) so existing pipelines
	// are not hard-stopped; --require-healthy escalates WARN to a hard stop.
	if len(drainBlockers) > 0 {
		result.Status = StatusWarn
		result.Score = min(result.Score, 50)
		result.Message = fmt.Sprintf("%d PDB(s) allow 0 disruptions; node roll will stall on eviction", len(drainBlockers))
		for _, b := range drainBlockers {
			result.Details = append(result.Details, "Drain blocker: "+b)
		}
		result.Details = append(result.Details, "Scale up the workload or relax minAvailable/maxUnavailable before rolling nodes")
	}

	return result
}
