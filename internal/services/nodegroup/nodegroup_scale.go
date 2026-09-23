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
	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/common"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
)

// scalePollInterval is how often the --wait loop re-checks the scaling
// update and the nodegroup.
const scalePollInterval = 5 * time.Second

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

	if err := s.checkScaleBounds(ctx, clusterName, nodegroupName, desired, min, max); err != nil {
		return err
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

	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.UpdateNodegroupConfigOutput, error) {
		return s.eksClient.UpdateNodegroupConfig(rc, input)
	})
	if err != nil {
		return awsinternal.FormatAWSError(err, fmt.Sprintf("updating nodegroup scaling for %s/%s", clusterName, nodegroupName))
	}

	if options.Wait {
		var updateID string
		if out != nil && out.Update != nil {
			updateID = aws.ToString(out.Update.Id)
		}
		if err := s.waitForScaleCompletion(ctx, clusterName, nodegroupName, updateID, desired, min, max, options.Timeout); err != nil {
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

// waitForScaleCompletion follows the EKS update updateID until it is
// Successful, Failed, or Cancelled. After Successful it confirms the
// nodegroup reports every requested size and, when desired was requested,
// waits for the nodegroup to be ACTIVE at that size. Transient poll errors
// (throttling, 5xx, network) are polled through; a permanent API error
// (AccessDenied, validation) fails at once. timeout > 0 caps the whole wait.
func (s *ServiceImpl) waitForScaleCompletion(ctx context.Context, clusterName, nodegroupName, updateID string, desired, min, max *int32, timeout time.Duration) error {
	ref := clusterName + "/" + nodegroupName
	if updateID == "" {
		return fmt.Errorf("waiting for nodegroup %s scaling: EKS returned no update ID", ref)
	}
	waitCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	interval := s.scalePollInterval
	if interval <= 0 {
		interval = scalePollInterval
	}

	// Phase 1: the EKS update is the authority on whether the change applied.
	op := fmt.Sprintf("checking scaling update %s of nodegroup %s", updateID, ref)
	var lastErr error
	var lastStatus ekstypes.UpdateStatus
	err := scalePollUntil(waitCtx, interval, func() (bool, error) {
		out, err := common.WithRetry(waitCtx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeUpdateOutput, error) {
			return s.eksClient.DescribeUpdate(rc, &eks.DescribeUpdateInput{
				Name:          aws.String(clusterName),
				NodegroupName: aws.String(nodegroupName),
				UpdateId:      aws.String(updateID),
			})
		})
		if err != nil {
			lastErr = err
			return false, s.scalePollError(waitCtx, err, op)
		}
		if out == nil || out.Update == nil {
			return false, nil
		}
		lastStatus = out.Update.Status
		switch out.Update.Status {
		case ekstypes.UpdateStatusSuccessful:
			return true, nil
		case ekstypes.UpdateStatusFailed, ekstypes.UpdateStatusCancelled:
			return false, fmt.Errorf("nodegroup %s scaling update %s %s%s", ref, updateID, out.Update.Status, scaleUpdateErrorDetails(out.Update.Errors))
		}
		return false, nil
	}, func(ctxErr error) error {
		what := fmt.Sprintf("timed out waiting for nodegroup %s scaling update %s", ref, updateID)
		if lastStatus != "" {
			what += fmt.Sprintf(" (last status %s)", lastStatus)
		}
		return scaleWaitTimeoutError(ctxErr, what, lastErr)
	})
	if err != nil {
		return err
	}

	// Phase 2: EKS says the update succeeded; make sure the nodegroup agrees
	// and, with --desired, wait for it to settle ACTIVE.
	op = fmt.Sprintf("confirming the scaling config of nodegroup %s", ref)
	lastErr = nil
	var lastNGStatus ekstypes.NodegroupStatus
	return scalePollUntil(waitCtx, interval, func() (bool, error) {
		out, err := common.WithRetry(waitCtx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
			return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
				ClusterName:   aws.String(clusterName),
				NodegroupName: aws.String(nodegroupName),
			})
		})
		if err != nil {
			lastErr = err
			return false, s.scalePollError(waitCtx, err, op)
		}
		if out == nil || out.Nodegroup == nil {
			return false, nil
		}
		ng := out.Nodegroup
		lastNGStatus = ng.Status
		if mismatch := scalingConfigMismatch(ng.ScalingConfig, desired, min, max); mismatch != "" {
			return false, fmt.Errorf("nodegroup %s scaling update %s reported Successful, but the nodegroup %s", ref, updateID, mismatch)
		}
		if desired == nil {
			return true, nil
		}
		return ng.Status == ekstypes.NodegroupStatusActive, nil
	}, func(ctxErr error) error {
		what := fmt.Sprintf("timed out waiting for nodegroup %s to settle at the new scaling config", ref)
		if lastNGStatus != "" {
			what += fmt.Sprintf(" (last status %s)", lastNGStatus)
		}
		return scaleWaitTimeoutError(ctxErr, what, lastErr)
	})
}

// scalingConfigMismatch describes how cfg differs from the requested sizes,
// or returns "" when every requested size matches.
func scalingConfigMismatch(cfg *ekstypes.NodegroupScalingConfig, desired, min, max *int32) string {
	if cfg == nil {
		return "reports no scaling config"
	}
	var diffs []string
	check := func(name string, want, got *int32) {
		if want == nil {
			return
		}
		if got == nil {
			diffs = append(diffs, fmt.Sprintf("%s size is unset, not %d", name, *want))
			return
		}
		if *got != *want {
			diffs = append(diffs, fmt.Sprintf("%s size is %d, not %d", name, *got, *want))
		}
	}
	check("desired", desired, cfg.DesiredSize)
	check("min", min, cfg.MinSize)
	check("max", max, cfg.MaxSize)
	if len(diffs) == 0 {
		return ""
	}
	return strings.Join(diffs, ", ")
}

// scalePollUntil calls check at once and then every interval until it
// reports done or returns an error. When ctx ends first, it returns
// onTimeout(ctx.Err()).
func scalePollUntil(ctx context.Context, interval time.Duration, check func() (bool, error), onTimeout func(error) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		done, err := check()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return onTimeout(ctx.Err())
		case <-ticker.C:
		}
	}
}

// scalePollError classifies a failed status poll. It returns nil to keep
// polling on a transient error (throttling, 5xx, network) or when ctx has
// ended (the poll loop then reports the timeout), and a formatted error for
// a permanent API error such as AccessDenied, which polling will not fix.
func (s *ServiceImpl) scalePollError(ctx context.Context, err error, op string) error {
	if !scaleKeepPolling(ctx, err) {
		return awsinternal.FormatAWSError(err, op)
	}
	if ctx.Err() == nil {
		s.logger.Warn("transient error while waiting for scaling; still polling", "op", op, "error", err)
	}
	return nil
}

// scaleKeepPolling reports whether a failed status poll is worth repeating.
func scaleKeepPolling(ctx context.Context, err error) bool {
	return ctx.Err() != nil || common.IsRetryable(err) || awserr.IsNetworkError(err)
}

// scaleWaitTimeoutError reports a wait that ran out of time, with the last
// poll error when there was one.
func scaleWaitTimeoutError(ctxErr error, what string, lastErr error) error {
	if lastErr != nil {
		return fmt.Errorf("%s: %w (last poll error: %s)", what, ctxErr, awserr.Summary(lastErr))
	}
	return fmt.Errorf("%s: %w", what, ctxErr)
}

// scaleUpdateErrorDetails renders an EKS update's error details as
// ": CODE: msg [ids]; ...", or "" when there are none.
func scaleUpdateErrorDetails(details []ekstypes.ErrorDetail) string {
	if len(details) == 0 {
		return ""
	}
	parts := make([]string, 0, len(details))
	for _, d := range details {
		p := string(d.ErrorCode)
		if msg := aws.ToString(d.ErrorMessage); msg != "" {
			if p != "" {
				p += ": "
			}
			p += msg
		}
		if len(d.ResourceIds) > 0 {
			p += " [" + strings.Join(d.ResourceIds, ", ") + "]"
		}
		parts = append(parts, p)
	}
	return ": " + strings.Join(parts, "; ")
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
// A nil desired is never a scale-down: Scale refuses a --min/--max change
// that would need the desired size to move (see checkScaleBounds). It returns
// an error when the check can't be done (no health checker or Kubernetes
// client, or a failed API call): the caller asked for PDB validation, so
// "couldn't check" must not read as "no blockers".
func (s *ServiceImpl) CheckScaleDownPDBs(ctx context.Context, clusterName, nodegroupName string, desired *int32) (*ScaleDownPDBCheck, error) {
	if desired == nil {
		return &ScaleDownPDBCheck{}, nil
	}
	current, err := s.currentDesiredSize(ctx, clusterName, nodegroupName)
	if err != nil {
		return nil, fmt.Errorf("PDB validation: %w", err)
	}
	check := &ScaleDownPDBCheck{
		CurrentDesired:   current,
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

// checkScaleBounds refuses, before any change, a --min/--max change without
// --desired that puts the current desired size outside the new bounds. EKS
// does not document moving the desired size into new bounds, so the user
// must set the new node count explicitly.
func (s *ServiceImpl) checkScaleBounds(ctx context.Context, clusterName, nodegroupName string, desired, min, max *int32) error {
	if desired != nil || (min == nil && max == nil) {
		return nil
	}
	current, err := s.currentDesiredSize(ctx, clusterName, nodegroupName)
	if err != nil {
		return err
	}
	if max != nil && *max < current {
		return fmt.Errorf("--max %d is below the current desired size %d; pass --desired to change the node count", *max, current)
	}
	if min != nil && *min > current {
		return fmt.Errorf("--min %d is above the current desired size %d; pass --desired to change the node count", *min, current)
	}
	return nil
}

// currentDesiredSize reads the nodegroup's current desired size.
func (s *ServiceImpl) currentDesiredSize(ctx context.Context, clusterName, nodegroupName string) (int32, error) {
	desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
		return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(nodegroupName),
		})
	})
	if err != nil {
		return 0, awsinternal.FormatAWSError(err, fmt.Sprintf("describing nodegroup %s/%s", clusterName, nodegroupName))
	}
	if desc == nil || desc.Nodegroup == nil || desc.Nodegroup.ScalingConfig == nil || desc.Nodegroup.ScalingConfig.DesiredSize == nil {
		return 0, fmt.Errorf("nodegroup %s/%s has no scaling config", clusterName, nodegroupName)
	}
	return *desc.Nodegroup.ScalingConfig.DesiredSize, nil
}
