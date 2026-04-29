package workloads

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func deployment(ns, name string, labels map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
			},
		},
	}
}

func pdb(ns, name string, selector map[string]string) *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: selector},
		},
	}
}

func TestAnalyzePDBCoverageMatchesPDBSelectorsToDeployments(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		deployment("apps", "api", map[string]string{"app": "api"}),
		deployment("apps", "worker", map[string]string{"app": "worker"}),
		deployment("kube-system", "coredns", map[string]string{"k8s-app": "kube-dns"}),
		pdb("apps", "api-pdb", map[string]string{"app": "api"}),
		pdb("kube-system", "coredns-pdb", map[string]string{"k8s-app": "kube-dns"}),
	)

	result, err := AnalyzePDBCoverage(context.Background(), client, PDBCoverageOptions{})
	if err != nil {
		t.Fatalf("AnalyzePDBCoverage: %v", err)
	}
	if result.Summary.TotalDeployments != 2 {
		t.Fatalf("TotalDeployments = %d, want 2", result.Summary.TotalDeployments)
	}
	if result.Summary.WithPDB != 1 || result.Summary.WithoutPDB != 1 {
		t.Fatalf("summary = %+v", result.Summary)
	}

	byName := map[string]PDBCoverageRow{}
	for _, row := range result.Deployments {
		byName[row.Deployment] = row
	}
	if !byName["api"].HasPDB || byName["api"].PDBs[0] != "api-pdb" {
		t.Fatalf("api row = %+v", byName["api"])
	}
	if byName["worker"].HasPDB || byName["worker"].Status != "MISSING" {
		t.Fatalf("worker row = %+v", byName["worker"])
	}
	if _, ok := byName["coredns"]; ok {
		t.Fatal("system namespace deployment should be excluded by default")
	}
}

func TestAnalyzePDBCoverageNamespaceAndSystemOptions(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		deployment("apps", "api", map[string]string{"app": "api"}),
		deployment("kube-system", "coredns", map[string]string{"k8s-app": "kube-dns"}),
		pdb("kube-system", "coredns-pdb", map[string]string{"k8s-app": "kube-dns"}),
	)

	result, err := AnalyzePDBCoverage(context.Background(), client, PDBCoverageOptions{Namespace: "kube-system", IncludeSystem: true})
	if err != nil {
		t.Fatalf("AnalyzePDBCoverage namespace: %v", err)
	}
	if len(result.Deployments) != 1 || result.Deployments[0].Deployment != "coredns" || !result.Deployments[0].HasPDB {
		t.Fatalf("namespace rows = %+v", result.Deployments)
	}
}

func TestAnalyzePDBCoverageRequiresClient(t *testing.T) {
	if _, err := AnalyzePDBCoverage(context.Background(), nil, PDBCoverageOptions{}); err == nil {
		t.Fatal("expected nil client error")
	}
}
