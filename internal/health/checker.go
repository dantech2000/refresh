// Package health runs the pre-flight cluster health checks (nodes, capacity,
// live utilization, control plane, quotas, workloads, PDBs, resource balance)
// that gate nodegroup updates and scales and cluster upgrades.
package health

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/diag"
)

// HealthStatus represents the status of a health check
type HealthStatus string

const (
	StatusPass HealthStatus = "Pass"
	StatusWarn HealthStatus = "Warn"
	StatusFail HealthStatus = "Fail"
)

// EnumValues lists every HealthStatus.
func (HealthStatus) EnumValues() []string {
	return []string{string(StatusPass), string(StatusWarn), string(StatusFail)}
}

// Decision represents the overall decision for proceeding with update
type Decision string

const (
	DecisionProceed Decision = "Proceed"
	DecisionWarn    Decision = "Warn"
	DecisionBlock   Decision = "Block"
)

// EnumValues lists every Decision.
func (Decision) EnumValues() []string {
	return []string{string(DecisionProceed), string(DecisionWarn), string(DecisionBlock)}
}

// HealthResult represents the result of a single health check
type HealthResult struct {
	Name       string       `json:"name" yaml:"name"`
	Status     HealthStatus `json:"status" yaml:"status"`
	Score      int          `json:"score" yaml:"score"` // 0-100
	Message    string       `json:"message" yaml:"message"`
	Details    []string     `json:"details,omitempty" yaml:"details,omitempty"`
	IsBlocking bool         `json:"isBlocking" yaml:"isBlocking"`
	// Skipped marks a check that could not be evaluated (e.g. no Kubernetes
	// client) rather than measured. Skipped checks are excluded from the
	// OverallScore so a missing prerequisite doesn't silently drag the score.
	Skipped bool `json:"skipped,omitempty" yaml:"skipped,omitempty"`
	// failures are the reads this check could not make (see
	// HealthSummary.Failures).
	failures []diag.Failure
}

// HealthSummary represents the overall health check results
type HealthSummary struct {
	Results      []HealthResult `json:"results" yaml:"results"`
	OverallScore int            `json:"overallScore" yaml:"overallScore"`
	Decision     Decision       `json:"decision" yaml:"decision"`
	Warnings     []string       `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	Errors       []string       `json:"errors,omitempty" yaml:"errors,omitempty"`
	// Failures are the EKS and Kubernetes reads the node and PDB checks could
	// not make. The check that needed the data warns or fails instead of
	// passing, so the Decision already accounts for them. [] when empty.
	Failures diag.List `json:"failures" yaml:"failures"`
}

// DocumentKind is HealthSummary.
func (HealthSummary) DocumentKind() apidoc.Kind { return apidoc.KindHealthSummary }

// HealthChecker performs various health checks on the EKS cluster
type HealthChecker struct {
	eksClient   nodegroupAPI
	k8sClient   kubernetes.Interface
	cwClient    *cloudwatch.Client
	asgClient   asgAPI
	nodeMetrics NodeMetricsLister // optional; enables the live utilization check
	sqClient    serviceQuotaAPI   // optional; enables the vCPU quota headroom check
	// optional; scopes the PDB drain-blocker check to these managed nodegroups
	targetNodegroups []string
	// reads the target nodegroups' desired size when none of their nodes are
	// found; nil when there is no EKS client
	ngDescriber nodegroupDescriber
}

// NewChecker creates a new health checker instance
func NewChecker(eksClient *eks.Client, k8sClient kubernetes.Interface, cwClient *cloudwatch.Client, asgClient *autoscaling.Client) *HealthChecker {
	hc := &HealthChecker{
		k8sClient: k8sClient,
		cwClient:  cwClient,
	}
	// Assign only non-nil pointers so the interface fields stay nil (a typed
	// nil pointer in an interface would defeat the nil checks).
	if eksClient != nil {
		hc.eksClient = eksClient
		hc.ngDescriber = eksClient
	}
	if asgClient != nil {
		hc.asgClient = asgClient
	}
	return hc
}

// NewCheckerForConfig builds the fully wired checker every command uses: EKS,
// CloudWatch, Auto Scaling and Service Quotas clients from awsCfg, plus the
// optional Kubernetes client and node-metrics lister (either may be nil).
func NewCheckerForConfig(awsCfg aws.Config, k8sClient kubernetes.Interface, metrics NodeMetricsLister) *HealthChecker {
	hc := NewChecker(
		eks.NewFromConfig(awsCfg),
		k8sClient,
		cloudwatch.NewFromConfig(awsCfg),
		autoscaling.NewFromConfig(awsCfg),
	)
	hc.SetServiceQuotas(servicequotas.NewFromConfig(awsCfg))
	if metrics != nil {
		hc.SetNodeMetrics(metrics)
	}
	return hc
}

// nodegroupAPI is the slice of the EKS API the node and capacity checks use.
type nodegroupAPI interface {
	ListNodegroups(ctx context.Context, in *eks.ListNodegroupsInput, optFns ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroup(ctx context.Context, in *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
}

// asgAPI is the slice of the Auto Scaling API the capacity check uses.
type asgAPI interface {
	DescribeAutoScalingGroups(ctx context.Context, in *autoscaling.DescribeAutoScalingGroupsInput, optFns ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error)
}

// RunAllChecks executes all health checks and returns a summary. The checks
// are independent, so they run concurrently; capacity and balance share one
// instance-discovery + CloudWatch fetch via a lazy snapshot.
func (hc *HealthChecker) RunAllChecks(ctx context.Context, clusterName string) HealthSummary {
	snap := hc.newCPUSnapshot(clusterName)
	checks := []func() HealthResult{
		func() HealthResult { return hc.CheckNodeHealth(ctx, clusterName) },
		func() HealthResult { return hc.checkClusterCapacityWith(ctx, snap) },
		func() HealthResult { return hc.CheckNodeUtilization(ctx, clusterName) },
		func() HealthResult { return hc.CheckControlPlaneMetrics(ctx, clusterName) },
		func() HealthResult { return hc.CheckServiceQuotas(ctx, clusterName) },
		func() HealthResult { return hc.CheckCriticalWorkloads(ctx) },
		func() HealthResult { return hc.checkPodDisruptionBudgets(ctx, clusterName) },
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

	return aggregateResults(results)
}

// aggregateResults folds the individual check results into a HealthSummary:
// the OverallScore is the mean of the *measured* checks (skipped checks are
// excluded so a missing prerequisite doesn't penalize the score), and the
// Decision is driven solely by blocking/warning flags.
func aggregateResults(results []HealthResult) HealthSummary {
	var warnings, errors []string
	var failures diag.List
	totalScore := 0
	measuredCount := 0
	hasBlocking := false
	hasWarnings := false

	for _, result := range results {
		failures = append(failures, result.failures...)
		// A skipped check could not be evaluated (missing prerequisite, e.g. no
		// Kubernetes/metrics client). It contributes neither to the score nor to
		// the verdict — otherwise a missing prerequisite would wrongly force a
		// WARN decision on an otherwise-healthy cluster. (REF-146)
		if result.Skipped {
			continue
		}
		totalScore += result.Score
		measuredCount++

		switch {
		case result.Status == StatusFail && result.IsBlocking:
			hasBlocking = true
			errors = append(errors, result.Message)
		case result.Status == StatusWarn:
			hasWarnings = true
			warnings = append(warnings, result.Message)
		case result.Status == StatusFail:
			errors = append(errors, result.Message)
			hasWarnings = true
		}
	}

	overallScore := 0
	if measuredCount > 0 {
		overallScore = totalScore / measuredCount
	}

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
		Failures:     failures,
	}
}
