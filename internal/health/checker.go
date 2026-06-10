package health

import (
	"context"
	"sync"

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
	Name       string       `json:"name"`
	Status     HealthStatus `json:"status"`
	Score      int          `json:"score"` // 0-100
	Message    string       `json:"message"`
	Details    []string     `json:"details,omitempty"`
	IsBlocking bool         `json:"isBlocking"`
}

// HealthSummary represents the overall health check results
type HealthSummary struct {
	Results      []HealthResult `json:"results"`
	OverallScore int            `json:"overallScore"`
	Decision     Decision       `json:"decision"`
	Warnings     []string       `json:"warnings,omitempty"`
	Errors       []string       `json:"errors,omitempty"`
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

// RunAllChecks executes all health checks and returns a summary. The checks
// are independent, so they run concurrently; capacity and balance share one
// instance-discovery + CloudWatch fetch via a lazy snapshot.
func (hc *HealthChecker) RunAllChecks(ctx context.Context, clusterName string) HealthSummary {
	var warnings []string
	var errors []string

	snap := hc.newCPUSnapshot(clusterName)
	checks := []func() HealthResult{
		func() HealthResult { return hc.CheckNodeHealth(ctx, clusterName) },
		func() HealthResult { return hc.checkClusterCapacityWith(ctx, snap) },
		func() HealthResult { return hc.CheckCriticalWorkloads(ctx) },
		func() HealthResult { return hc.CheckPodDisruptionBudgets(ctx) },
		func() HealthResult { return hc.checkResourceBalanceWith(ctx, snap) },
	}

	results := make([]HealthResult, len(checks))
	var wg sync.WaitGroup
	for i, check := range checks {
		wg.Add(1)
		go func(i int, check func() HealthResult) {
			defer wg.Done()
			results[i] = check()
		}(i, check)
	}
	wg.Wait()

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
