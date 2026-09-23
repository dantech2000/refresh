package nodegroup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/common"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
)

// scalePollInterval is how often waitForScaleCompletion re-checks nodegroup
// status while waiting for a scaling operation to finish.
const scalePollInterval = 5 * time.Second

// ErrScaleHealthBlocked marks a scale the pre-scaling health check blocked
// before any change.
var ErrScaleHealthBlocked = errors.New("pre-scaling health check blocked operation")

// ErrScaleVerifyFailed marks a scale that was applied but whose
// post-scaling health check found blocking issues.
var ErrScaleVerifyFailed = errors.New("post-scaling health check found blocking issues")

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
			return fmt.Errorf("%w: %v", ErrScaleHealthBlocked, summary.Errors)
		}
		if summary.Decision == health.DecisionWarn {
			s.logger.Warn("pre-scaling health warnings", "warnings", summary.Warnings)
		}
	}

	// A scaling config change does not honor PDBs: EKS terminates the
	// surplus nodes without waiting for evictions. So with --check-pdbs a
	// scale-down that could take a PDB below its budget is refused unless
	// Force is set.
	if options.CheckPDBs && !options.Force {
		check, err := s.CheckScaleDownPDBs(ctx, clusterName, nodegroupName, desired)
		if err != nil {
			return err
		}
		if check.Refused() {
			return &ScaleDownBlockedError{Cluster: clusterName, Nodegroup: nodegroupName, Check: *check}
		}
	}

	input := &eks.UpdateNodegroupConfigInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(nodegroupName),
		// Pin the idempotency token so WithRetry re-issues the SAME request
		// instead of submitting a fresh update per attempt.
		ClientRequestToken: aws.String(common.IdempotencyToken()),
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
			return fmt.Errorf("%w: %v", ErrScaleVerifyFailed, summary.Errors)
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
	ticker := time.NewTicker(scalePollInterval)
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
			if ng == nil {
				continue
			}
			if ng.Status == ekstypes.NodegroupStatusActive {
				if desired == nil || (ng.ScalingConfig != nil && ng.ScalingConfig.DesiredSize != nil && *ng.ScalingConfig.DesiredSize == *desired) {
					return nil
				}
			}
		}
	}
}

// ScaleDownPDBCheck is the result of CheckScaleDownPDBs.
type ScaleDownPDBCheck struct {
	// CurrentDesired is the nodegroup's desired size before the change.
	CurrentDesired int32
	// RequestedDesired is the requested desired size, or CurrentDesired when
	// --desired is not set.
	RequestedDesired int32
	// ScaleDown is true when RequestedDesired < CurrentDesired. The other
	// fields are only filled in for a scale-down.
	ScaleDown bool
	// Blockers are the PDBs whose covered pods on the nodegroup's nodes could
	// lose more pods than the PDB allows, when the removed nodes are the ones
	// that hold the most of them (see health.HealthChecker.ScaleDownBlockers).
	// When Scoped is false they are every PDB in the cluster that allows 0
	// disruptions.
	Blockers []health.PDBInfo
	// Scoped is true when Blockers is narrowed to the nodegroup's nodes.
	Scoped bool
	// Note explains a short-circuit in the check, if any.
	Note string
}

// Refused reports whether the check refuses the scale.
func (c ScaleDownPDBCheck) Refused() bool { return c.ScaleDown && len(c.Blockers) > 0 }

// ScaleDownBlockedError is returned by Scale with CheckPDBs when a scale-down
// could terminate more of a PDB's pods than the PDB allows.
type ScaleDownBlockedError struct {
	Cluster   string
	Nodegroup string
	Check     ScaleDownPDBCheck
}

func (e *ScaleDownBlockedError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "refusing to scale %s/%s down from %d to %d: ",
		e.Cluster, e.Nodegroup, e.Check.CurrentDesired, e.Check.RequestedDesired)
	if e.Check.Scoped {
		fmt.Fprintf(&b, "%d PodDisruptionBudget(s) could lose more pods on this nodegroup's nodes than they allow", len(e.Check.Blockers))
	} else {
		fmt.Fprintf(&b, "%d PodDisruptionBudget(s) allow 0 disruptions (could not scope to this nodegroup's nodes, so every such PDB in the cluster counts)", len(e.Check.Blockers))
	}
	b.WriteString(":")
	for _, p := range e.Check.Blockers {
		b.WriteString("\n  - ")
		b.WriteString(p.DrainBlockerSummary())
	}
	b.WriteString("\nA scaling change does not wait for PDBs: EKS terminates the removed nodes and their pods go down with them.")
	b.WriteString("\nScale up the workload or relax the PDB first, or re-run with --force to scale down anyway.")
	return b.String()
}

// CheckScaleDownPDBs reports whether scaling nodegroupName to desired is a
// scale-down and, if so, which PDBs it could violate. The PDB scan is scoped
// to the nodegroup's nodes and assumes the worst case: the removed nodes are
// the ones that hold the most of a PDB's pods (see
// health.HealthChecker.ScaleDownBlockers).
// A nil desired is never a scale-down. It returns an error when the check
// can't be done (no health checker or Kubernetes client, or a failed API
// call): the caller asked for PDB validation, so "couldn't check" must not
// read as "no blockers".
func (s *ServiceImpl) CheckScaleDownPDBs(ctx context.Context, clusterName, nodegroupName string, desired *int32) (*ScaleDownPDBCheck, error) {
	if desired == nil {
		return &ScaleDownPDBCheck{}, nil
	}
	desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
		return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(nodegroupName),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("PDB validation: %w", awsinternal.FormatAWSError(err, fmt.Sprintf("describing nodegroup %s/%s", clusterName, nodegroupName)))
	}
	if desc == nil || desc.Nodegroup == nil || desc.Nodegroup.ScalingConfig == nil || desc.Nodegroup.ScalingConfig.DesiredSize == nil {
		return nil, fmt.Errorf("PDB validation: nodegroup %s/%s has no scaling config", clusterName, nodegroupName)
	}
	check := &ScaleDownPDBCheck{
		CurrentDesired:   *desc.Nodegroup.ScalingConfig.DesiredSize,
		RequestedDesired: *desired,
	}
	check.ScaleDown = check.RequestedDesired < check.CurrentDesired
	if !check.ScaleDown {
		return check, nil
	}
	if s.healthChecker == nil {
		return nil, errors.New("PDB validation: no health checker configured")
	}
	report, err := s.healthChecker.ScaleDownBlockers(ctx, clusterName, nodegroupName, check.CurrentDesired-check.RequestedDesired)
	if err != nil {
		return nil, fmt.Errorf("PDB validation for %s/%s: %w (fix cluster access with --kubeconfig/--kube-context, or use --force to skip the PDB gate)", clusterName, nodegroupName, err)
	}
	check.Blockers = report.Blockers
	check.Scoped = report.Scoped
	check.Note = report.Note
	return check, nil
}
