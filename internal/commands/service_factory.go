package commands

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

// newDefaultLogger returns a default logger if none is provided.
func newDefaultLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// newClusterService centralizes cluster service initialization with optional health.
func newClusterService(awsCfg aws.Config, withHealth bool, logger *slog.Logger) *cluster.ServiceImpl {
	logger = newDefaultLogger(logger)
	var hc *health.HealthChecker
	if withHealth {
		eksClient := eks.NewFromConfig(awsCfg)
		cwClient := cloudwatch.NewFromConfig(awsCfg)
		asgClient := autoscaling.NewFromConfig(awsCfg)
		hc = health.NewChecker(eksClient, nil, cwClient, asgClient)
	}
	return cluster.NewService(awsCfg, hc, logger)
}

// newNodegroupService centralizes nodegroup service initialization with optional health.
func newNodegroupService(awsCfg aws.Config, withHealth bool, logger *slog.Logger) *nodegroup.ServiceImpl {
	logger = newDefaultLogger(logger)
	var hc *health.HealthChecker
	if withHealth {
		eksClient := eks.NewFromConfig(awsCfg)
		cwClient := cloudwatch.NewFromConfig(awsCfg)
		asgClient := autoscaling.NewFromConfig(awsCfg)
		hc = health.NewChecker(eksClient, nil, cwClient, asgClient)
	}
	return nodegroup.NewService(awsCfg, hc, logger)
}
