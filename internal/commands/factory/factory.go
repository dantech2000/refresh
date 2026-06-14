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

// NewClusterService initializes a cluster service with optional health checking.
func NewClusterService(awsCfg aws.Config, withHealth bool, logger *slog.Logger) *cluster.ServiceImpl {
	logger = NewDefaultLogger(logger)
	var hc *health.HealthChecker
	if withHealth {
		eksClient := eks.NewFromConfig(awsCfg)
		cwClient := cloudwatch.NewFromConfig(awsCfg)
		asgClient := autoscaling.NewFromConfig(awsCfg)
		hc = health.NewChecker(eksClient, nil, cwClient, asgClient)
	}
	return cluster.NewService(awsCfg, hc, logger)
}

// NewNodegroupService initializes a nodegroup service with optional health checking.
func NewNodegroupService(awsCfg aws.Config, withHealth bool, logger *slog.Logger) *nodegroup.ServiceImpl {
	logger = NewDefaultLogger(logger)
	var hc *health.HealthChecker
	if withHealth {
		eksClient := eks.NewFromConfig(awsCfg)
		cwClient := cloudwatch.NewFromConfig(awsCfg)
		asgClient := autoscaling.NewFromConfig(awsCfg)
		hc = health.NewChecker(eksClient, nil, cwClient, asgClient)
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
func NewClusterServiceWithHealth(awsCfg aws.Config, k8sClient kubernetes.Interface, logger *slog.Logger) *cluster.ServiceImpl {
	logger = NewDefaultLogger(logger)
	eksClient := eks.NewFromConfig(awsCfg)
	cwClient := cloudwatch.NewFromConfig(awsCfg)
	asgClient := autoscaling.NewFromConfig(awsCfg)
	hc := health.NewChecker(eksClient, k8sClient, cwClient, asgClient)
	return cluster.NewService(awsCfg, hc, logger)
}

// NewNodegroupServiceWithHealth initializes a nodegroup service whose health
// checker is wired to the given Kubernetes client (which may be nil, in which
// case kube-dependent checks degrade gracefully). Use this when a command has
// resolved a --kubeconfig so workload/PDB checks run against the right cluster.
func NewNodegroupServiceWithHealth(awsCfg aws.Config, k8sClient kubernetes.Interface, logger *slog.Logger) *nodegroup.ServiceImpl {
	logger = NewDefaultLogger(logger)
	eksClient := eks.NewFromConfig(awsCfg)
	cwClient := cloudwatch.NewFromConfig(awsCfg)
	asgClient := autoscaling.NewFromConfig(awsCfg)
	hc := health.NewChecker(eksClient, k8sClient, cwClient, asgClient)
	return nodegroup.NewService(awsCfg, hc, logger)
}
