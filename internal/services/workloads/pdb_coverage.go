package workloads

import (
	"context"
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

type PDBCoverageOptions struct {
	Namespace     string `json:"namespace,omitempty"`
	IncludeSystem bool   `json:"includeSystem"`
}

type PDBCoverageRow struct {
	Namespace  string   `json:"namespace" yaml:"namespace"`
	Deployment string   `json:"deployment" yaml:"deployment"`
	HasPDB     bool     `json:"hasPdb" yaml:"hasPdb"`
	PDBs       []string `json:"pdbs" yaml:"pdbs"`
	Status     string   `json:"status" yaml:"status"`
}

type PDBCoverageSummary struct {
	TotalDeployments int `json:"totalDeployments" yaml:"totalDeployments"`
	WithPDB          int `json:"withPdb" yaml:"withPdb"`
	WithoutPDB       int `json:"withoutPdb" yaml:"withoutPdb"`
}

type PDBCoverageResult struct {
	Deployments []PDBCoverageRow     `json:"deployments" yaml:"deployments"`
	Summary     PDBCoverageSummary   `json:"summary" yaml:"summary"`
	Options     PDBCoverageOptions   `json:"options" yaml:"options"`
}

func AnalyzePDBCoverage(ctx context.Context, client kubernetes.Interface, opts PDBCoverageOptions) (PDBCoverageResult, error) {
	if client == nil {
		return PDBCoverageResult{}, fmt.Errorf("kubernetes client is required")
	}

	namespaces, err := coverageNamespaces(ctx, client, opts)
	if err != nil {
		return PDBCoverageResult{}, err
	}

	result := PDBCoverageResult{Options: opts}
	for _, namespace := range namespaces {
		rows, err := analyzeNamespacePDBCoverage(ctx, client, namespace)
		if err != nil {
			return PDBCoverageResult{}, err
		}
		result.Deployments = append(result.Deployments, rows...)
	}

	sort.SliceStable(result.Deployments, func(i, j int) bool {
		if result.Deployments[i].Namespace == result.Deployments[j].Namespace {
			return result.Deployments[i].Deployment < result.Deployments[j].Deployment
		}
		return result.Deployments[i].Namespace < result.Deployments[j].Namespace
	})

	result.Summary.TotalDeployments = len(result.Deployments)
	for _, row := range result.Deployments {
		if row.HasPDB {
			result.Summary.WithPDB++
		} else {
			result.Summary.WithoutPDB++
		}
	}

	return result, nil
}

func coverageNamespaces(ctx context.Context, client kubernetes.Interface, opts PDBCoverageOptions) ([]string, error) {
	if opts.Namespace != "" {
		return []string{opts.Namespace}, nil
	}

	namespaces, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(namespaces.Items))
	for _, namespace := range namespaces.Items {
		if !opts.IncludeSystem && isSystemNamespace(namespace.Name) {
			continue
		}
		out = append(out, namespace.Name)
	}
	sort.Strings(out)
	return out, nil
}

func analyzeNamespacePDBCoverage(ctx context.Context, client kubernetes.Interface, namespace string) ([]PDBCoverageRow, error) {
	deployments, err := client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	pdbs, err := client.PolicyV1().PodDisruptionBudgets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	rows := make([]PDBCoverageRow, 0, len(deployments.Items))
	for _, deployment := range deployments.Items {
		matches := make([]string, 0)
		podLabels := labels.Set(deployment.Spec.Template.Labels)
		for _, pdb := range pdbs.Items {
			if pdb.Spec.Selector == nil {
				continue
			}
			selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
			if err != nil {
				continue
			}
			if selector.Matches(podLabels) {
				matches = append(matches, pdb.Name)
			}
		}
		sort.Strings(matches)

		status := "MISSING"
		if len(matches) > 0 {
			status = "PROTECTED"
		}
		rows = append(rows, PDBCoverageRow{
			Namespace:  namespace,
			Deployment: deployment.Name,
			HasPDB:     len(matches) > 0,
			PDBs:       matches,
			Status:     status,
		})
	}
	return rows, nil
}

func isSystemNamespace(namespace string) bool {
	switch namespace {
	case "kube-system", "kube-public", "kube-node-lease", "default":
		return true
	default:
		return false
	}
}
