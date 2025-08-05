package health

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"k8s.io/client-go/kubernetes"
)

// HealthStatus represents the status of a health check
type HealthStatus string

const (
	StatusPass HealthStatus = "PASS"
	StatusWarn HealthStatus = "WARN"
	StatusFail HealthStatus = "FAIL"
)

// Decision represents the overall decision for proceeding with update
type Decision string

const (
	DecisionProceed Decision = "PROCEED"
	DecisionWarn    Decision = "WARN"
	DecisionBlock   Decision = "BLOCK"
)

// HealthResult represents the result of a single health check
type HealthResult struct {
	Name       string
	Status     HealthStatus
	Score      int // 0-100
	Message    string
	Details    []string
	IsBlocking bool
}

// HealthSummary represents the overall health check results
type HealthSummary struct {
	Results      []HealthResult
	OverallScore int
	Decision     Decision
	Warnings     []string
	Errors       []string
}

// HealthChecker performs various health checks on the EKS cluster
type HealthChecker struct {
	eksClient *eks.Client
	k8sClient kubernetes.Interface
	cwClient  *cloudwatch.Client
	asgClient *autoscaling.Client
}

// NewChecker creates a new health checker instance
func NewChecker(eksClient *eks.Client, k8sClient kubernetes.Interface, cwClient *cloudwatch.Client, asgClient *autoscaling.Client) *HealthChecker {
	return &HealthChecker{
		eksClient: eksClient,
		k8sClient: k8sClient,
		cwClient:  cwClient,
		asgClient: asgClient,
	}
}

// RunAllChecks executes all health checks and returns a summary
func (hc *HealthChecker) RunAllChecks(ctx context.Context, clusterName string) HealthSummary {
	var results []HealthResult
	var warnings []string
	var errors []string

	// Run individual health checks
	nodeHealth := hc.CheckNodeHealth(ctx, clusterName)
	results = append(results, nodeHealth)

	capacity := hc.CheckClusterCapacity(ctx, clusterName)
	results = append(results, capacity)

	workloads := hc.CheckCriticalWorkloads(ctx)
	results = append(results, workloads)

	pdbs := hc.CheckPodDisruptionBudgets(ctx)
	results = append(results, pdbs)

	balance := hc.CheckResourceBalance(ctx, clusterName)
	results = append(results, balance)

	// Calculate overall score and decision
	totalScore := 0
	hasBlocking := false
	hasWarnings := false

	for _, result := range results {
		totalScore += result.Score

		if result.Status == StatusFail && result.IsBlocking {
			hasBlocking = true
			errors = append(errors, result.Message)
		} else if result.Status == StatusWarn {
			hasWarnings = true
			warnings = append(warnings, result.Message)
		} else if result.Status == StatusFail {
			errors = append(errors, result.Message)
		}
	}

	overallScore := totalScore / len(results)
	decision := DecisionProceed

	if hasBlocking {
		decision = DecisionBlock
	} else if hasWarnings || len(errors) > 0 {
		decision = DecisionWarn
	}

	return HealthSummary{
		Results:      results,
		OverallScore: overallScore,
		Decision:     decision,
		Warnings:     warnings,
		Errors:       errors,
	}
}
