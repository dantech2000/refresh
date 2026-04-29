package nodegroup

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/common"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
)

// Scale updates the desired/min/max size for a nodegroup.
func (s *ServiceImpl) Scale(ctx context.Context, clusterName, nodegroupName string, desired, min, max *int32, options ScaleOptions) error {
	s.logger.Info("scaling nodegroup", "cluster", clusterName, "nodegroup", nodegroupName,
		"desired", desired, "min", min, "max", max, "options", options)

	if options.DryRun {
		return nil
	}

	if options.HealthCheck && s.healthChecker != nil {
		summary := s.healthChecker.RunAllChecks(ctx, clusterName)
		if summary.Decision == health.DecisionBlock {
			return fmt.Errorf("pre-scaling health check blocked operation: %v", summary.Errors)
		}
		if summary.Decision == health.DecisionWarn {
			s.logger.Warn("pre-scaling health warnings", "warnings", summary.Warnings)
		}
	}

	if options.CheckPDBs && s.healthChecker != nil && desired != nil {
		desc, err := s.eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(nodegroupName),
		})
		if err == nil && desc.Nodegroup.ScalingConfig != nil && desc.Nodegroup.ScalingConfig.DesiredSize != nil {
			if *desired < *desc.Nodegroup.ScalingConfig.DesiredSize {
				pdb := s.healthChecker.CheckPodDisruptionBudgets(ctx)
				if pdb.Status == health.StatusFail && pdb.IsBlocking {
					return fmt.Errorf("PDB validation failed: %s", pdb.Message)
				}
			}
		}
	}

	input := &eks.UpdateNodegroupConfigInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(nodegroupName),
	}
	if desired != nil || min != nil || max != nil {
		input.ScalingConfig = &ekstypes.NodegroupScalingConfig{
			DesiredSize: desired,
			MinSize:     min,
			MaxSize:     max,
		}
	}

	_, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.UpdateNodegroupConfigOutput, error) {
		return s.eksClient.UpdateNodegroupConfig(rc, input)
	})
	if err != nil {
		return awsinternal.FormatAWSError(err, fmt.Sprintf("updating nodegroup scaling for %s/%s", clusterName, nodegroupName))
	}

	if options.Wait {
		if err := s.waitForScaleCompletion(ctx, clusterName, nodegroupName, desired, options.Timeout); err != nil {
			return err
		}
	}

	if options.HealthCheck && s.healthChecker != nil {
		summary := s.healthChecker.RunAllChecks(ctx, clusterName)
		if summary.Decision == health.DecisionBlock {
			return fmt.Errorf("post-scaling health check blocked operation: %v", summary.Errors)
		}
		if summary.Decision == health.DecisionWarn {
			s.logger.Warn("post-scaling health warnings", "warnings", summary.Warnings)
		}
	}
	return nil
}

func (s *ServiceImpl) waitForScaleCompletion(ctx context.Context, clusterName, nodegroupName string, desired *int32, timeout time.Duration) error {
	waitCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("timed out waiting for nodegroup scaling to complete: %w", waitCtx.Err())
		case <-ticker.C:
			out, err := s.eksClient.DescribeNodegroup(waitCtx, &eks.DescribeNodegroupInput{
				ClusterName:   aws.String(clusterName),
				NodegroupName: aws.String(nodegroupName),
			})
			if err != nil {
				s.logger.Warn("failed to describe nodegroup while waiting", "error", err)
				continue
			}
			ng := out.Nodegroup
			if ng.Status == ekstypes.NodegroupStatusActive {
				if desired == nil || (ng.ScalingConfig != nil && ng.ScalingConfig.DesiredSize != nil && *ng.ScalingConfig.DesiredSize == *desired) {
					return nil
				}
			}
		}
	}
}
