package health

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// CheckPodDisruptionBudgets validates PDB configuration for user workloads
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

	// Get all deployments in user namespaces (excluding system namespaces)
	systemNamespaces := map[string]bool{
		"kube-system":     true,
		"kube-public":     true,
		"kube-node-lease": true,
		"default":         true,
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

	// Calculate score and status
	if totalDeployments == 0 {
		result.Status = StatusPass
		result.Score = 100
		result.Message = "No user deployments found"
		return result
	}

	// For PDBs, we're more lenient - it's a warning, not a failure
	pdbCoveragePercentage := (protectedDeployments * 100) / totalDeployments
	result.Score = pdbCoveragePercentage

	if len(unprotectedDeployments) == 0 {
		result.Status = StatusPass
		result.Message = fmt.Sprintf("All %d deployments have PDB protection", totalDeployments)
	} else if pdbCoveragePercentage >= 50 {
		result.Status = StatusWarn
		result.Message = fmt.Sprintf("%d deployments missing PDBs", len(unprotectedDeployments))
		if len(unprotectedDeployments) <= 5 {
			result.Details = append(result.Details, fmt.Sprintf("Unprotected: %v", unprotectedDeployments))
		} else {
			result.Details = append(result.Details, fmt.Sprintf("Unprotected: %v... (+%d more)", unprotectedDeployments[:5], len(unprotectedDeployments)-5))
		}
	} else {
		result.Status = StatusWarn // Still warning, not fail
		result.Message = fmt.Sprintf("%d/%d deployments missing PDBs", len(unprotectedDeployments), totalDeployments)
		result.Details = append(result.Details, "Consider creating PDBs for critical workloads")
	}

	return result
}
