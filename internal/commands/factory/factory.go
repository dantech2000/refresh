// Package factory constructs AWS-backed services used by CLI command packages.
package factory

import (
	"log/slog"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/services/nodegroup"
)

// NewDefaultLogger returns logger unchanged if non-nil; otherwise returns a
// stderr warn-level text logger.
func NewDefaultLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
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
