// Package factory constructs AWS-backed services used by CLI command packages.
package factory

import (
	"log/slog"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/services/nodegroup"
)

// defaultLogLevel is the level used by NewDefaultLogger when a command does not
// supply its own logger. It defaults to warn (quiet) and is overridden once,
// from main's Before hook, by the global --log-level / --verbose flags (REF-37).
var defaultLogLevel = slog.LevelWarn

// SetDefaultLogLevel sets the level NewDefaultLogger uses for the shared logger.
// Call it once during startup; commands run single-threaded after that.
func SetDefaultLogLevel(level slog.Level) { defaultLogLevel = level }

// ParseLogLevel maps a --log-level string to an slog.Level. Unknown values
// fall back to warn (the default quiet level). (REF-37)
func ParseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning", "":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}

// NewDefaultLogger returns logger unchanged if non-nil; otherwise returns a
// stderr text logger at the configured default level (see SetDefaultLogLevel).
// This is the single logger-construction path for the CLI.
func NewDefaultLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: defaultLogLevel}))
}

// newHealthChecker builds a health checker with the AWS-backed clients always
// wired — including Service Quotas, which needs no cluster access — plus the
// optional Kubernetes and metrics-server clients. Centralizing construction
// here keeps every entry point (describe, scale, upgrade) consistent so a check
// isn't silently skipped just because one command forgot to wire its client.
func newHealthChecker(awsCfg aws.Config, k8sClient kubernetes.Interface, metricsClient health.NodeMetricsLister) *health.HealthChecker {
	hc := health.NewChecker(
		eks.NewFromConfig(awsCfg),
		k8sClient,
		cloudwatch.NewFromConfig(awsCfg),
		autoscaling.NewFromConfig(awsCfg),
	)
	hc.SetServiceQuotas(servicequotas.NewFromConfig(awsCfg))
	if metricsClient != nil {
		hc.SetNodeMetrics(metricsClient)
	}
	return hc
}

// NewClusterService initializes a cluster service with optional health checking.
func NewClusterService(awsCfg aws.Config, withHealth bool, logger *slog.Logger) *cluster.ServiceImpl {
	logger = NewDefaultLogger(logger)
	var hc *health.HealthChecker
	if withHealth {
		hc = newHealthChecker(awsCfg, nil, nil)
	}
	return cluster.NewService(awsCfg, hc, logger)
}

// NewNodegroupService initializes a nodegroup service with optional health checking.
func NewNodegroupService(awsCfg aws.Config, withHealth bool, logger *slog.Logger) *nodegroup.ServiceImpl {
	logger = NewDefaultLogger(logger)
	var hc *health.HealthChecker
	if withHealth {
		hc = newHealthChecker(awsCfg, nil, nil)
	}
	return nodegroup.NewService(awsCfg, hc, logger)
}

// NewAddonService initializes an add-on service through the shared logger path,
// matching the cluster/nodegroup constructors. (REF-39)
func NewAddonService(awsCfg aws.Config, logger *slog.Logger) *addons.ServiceImpl {
	return addons.NewService(eks.NewFromConfig(awsCfg), NewDefaultLogger(logger))
}

// NewClusterServiceWithHealth initializes a cluster service whose health checker
// is wired to the given Kubernetes client (which may be nil, in which case
// kube-dependent signals degrade gracefully). Use this when a command has
// resolved a --kubeconfig so measured node readiness runs against the right
// cluster. (REF-130)
func NewClusterServiceWithHealth(awsCfg aws.Config, k8sClient kubernetes.Interface, metricsClient health.NodeMetricsLister, logger *slog.Logger) *cluster.ServiceImpl {
	logger = NewDefaultLogger(logger)
	return cluster.NewService(awsCfg, newHealthChecker(awsCfg, k8sClient, metricsClient), logger)
}

// NewNodegroupServiceWithHealth initializes a nodegroup service whose health
// checker is wired to the given Kubernetes client (which may be nil, in which
// case kube-dependent checks degrade gracefully). Use this when a command has
// resolved a --kubeconfig so workload/PDB checks run against the right cluster.
func NewNodegroupServiceWithHealth(awsCfg aws.Config, k8sClient kubernetes.Interface, logger *slog.Logger) *nodegroup.ServiceImpl {
	logger = NewDefaultLogger(logger)
	return nodegroup.NewService(awsCfg, newHealthChecker(awsCfg, k8sClient, nil), logger)
}
