package upgrade

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/services/common"
)

// NodegroupGate is a pre-flight check run before each nodegroup roll, after
// the built-in check (nodegroup ACTIVE with no reported health issues)
// passes. An error stops the phase before the roll starts.
type NodegroupGate func(ctx context.Context, nodegroupName string) error

// RollObserver renders a live view of a single nodegroup roll. It is supplied
// by the command (view) layer and run by the nodegroup phase concurrently with
// the authoritative DescribeUpdate wait once a roll starts — so rendering never
// happens in the service itself. Its ctx is cancelled as soon as the update
// reaches a terminal state (or the wait otherwise ends), and it must return
// promptly then; it never affects the result. A nil observer means text
// progress only.
type RollObserver func(ctx context.Context, nodegroupName string)

// NodegroupRollOptions tunes the nodegroup phase.
type NodegroupRollOptions struct {
	// SkipPatterns are substring patterns for nodegroups to leave alone.
	SkipPatterns []string
	// Only, when non-empty, limits the phase to these nodegroup names (the
	// plan's pending steps). Other nodegroups are left alone even if they lag
	// the target, e.g. in a catch-up hop that pre-rolls only the nodegroups
	// the next control-plane step would push beyond the kubelet skew.
	Only []string
	// Force terminates pods that can't be drained due to PDBs (passed
	// through to UpdateNodegroupVersion).
	Force bool
	// Gate is an extra pre-flight gate run after the built-in one (e.g. the
	// command layer's PDB drain-blocker and cluster health checks).
	Gate NodegroupGate
	// Observer, when set, renders a live per-node roll view during each roll.
	Observer RollObserver
}

// UpgradeNodegroups rolls every managed nodegroup to targetVersion, serially
// and in listing order, with a pre-flight gate before each roll. It is the
// same UpdateNodegroupVersion machinery as the AMI refresh — a version roll
// IS an AMI refresh with Version set.
//
// Already-current nodegroups are skipped (idempotent rerun); custom-AMI
// nodegroups are surfaced as manual actions, never mutated. A gate failure
// halts the remaining nodegroups so the operator can intervene.
func (s *Service) UpgradeNodegroups(ctx context.Context, clusterName, targetVersion string, opts NodegroupRollOptions, progress ProgressFunc) error {
	progress = ensureProgress(progress)

	nodegroups, err := s.listNodegroupStates(ctx, clusterName)
	if err != nil {
		return err
	}

	builtin := s.defaultNodegroupGate(clusterName)

	for _, ng := range nodegroups {
		if len(opts.Only) > 0 && !slices.Contains(opts.Only, ng.Name) {
			continue
		}
		switch {
		case versionAtLeast(ng.Version, targetVersion):
			progress("nodegroup %s already at %s, skipping", ng.Name, ng.Version)
			continue
		case matchesAny(ng.Name, opts.SkipPatterns):
			progress("nodegroup %s: skipped via --skip-nodegroup", ng.Name)
			continue
		case ng.CustomAMI:
			progress("nodegroup %s: MANUAL — custom AMI; build and roll a %s-compatible AMI yourself", ng.Name, targetVersion)
			continue
		}

		// Resume support: a rerun after Ctrl+C mid-roll finds the nodegroup
		// still UPDATING at its old version. Attach and wait for the roll to
		// settle (like the control-plane and addon phases do) instead of
		// failing the ACTIVE gate, then re-read the version.
		if ng.Status == ekstypes.NodegroupStatusUpdating {
			progress("nodegroup %s is UPDATING (in-flight roll from a previous run); attaching and waiting for it to settle", ng.Name)
			version, err := s.waitForNodegroupSettled(ctx, clusterName, ng.Name, progress)
			if err != nil {
				return fmt.Errorf("nodegroup %s: waiting for in-flight update to finish: %w", ng.Name, err)
			}
			if versionAtLeast(version, targetVersion) {
				progress("nodegroup %s reached %s", ng.Name, version)
				continue
			}
		}

		if err := builtin(ctx, ng.Name); err != nil {
			return fmt.Errorf("pre-flight gate failed for nodegroup %s (remaining nodegroups not attempted): %w", ng.Name, err)
		}
		if opts.Gate != nil {
			if err := opts.Gate(ctx, ng.Name); err != nil {
				return fmt.Errorf("pre-flight gate failed for nodegroup %s (remaining nodegroups not attempted): %w", ng.Name, err)
			}
		}

		if err := s.rollNodegroup(ctx, clusterName, ng.Name, targetVersion, opts.Force, opts.Observer, progress); err != nil {
			return err
		}
	}
	return nil
}

// rollNodegroup starts and watches a single nodegroup version roll.
func (s *Service) rollNodegroup(ctx context.Context, clusterName, nodegroupName, targetVersion string, force bool, observer RollObserver, progress ProgressFunc) error {
	input := &eks.UpdateNodegroupVersionInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(nodegroupName),
		Version:       aws.String(targetVersion),
		Force:         force,
		// Pin the idempotency token so WithRetry re-issues the SAME request
		// instead of submitting a fresh update per attempt.
		ClientRequestToken: aws.String(common.IdempotencyToken()),
	}
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.UpdateNodegroupVersionOutput, error) {
		return s.eksClient.UpdateNodegroupVersion(rc, input)
	})
	if err != nil {
		return awsinternal.FormatAWSError(err, fmt.Sprintf("rolling nodegroup %s to %s", nodegroupName, targetVersion))
	}

	updateID := ""
	if out.Update != nil {
		updateID = aws.ToString(out.Update.Id)
	}
	progress("nodegroup %s roll to %s started (update %s)", nodegroupName, targetVersion, updateID)

	// Live per-node panel (view layer, best-effort) runs alongside the
	// DescribeUpdate wait, which stays authoritative for the result: once EKS
	// reports a terminal status (including FAILED), the panel is cancelled and
	// joined, so a roll that never converges can't hold the wait hostage.
	// While the panel owns the terminal, the wait's progress lines are held
	// back so they never draw over it. They are flushed as soon as the
	// observer returns — when the wait ends, or earlier if the panel has
	// nothing to show or the roll already looks complete — and later lines
	// pass straight through. Without an observer, nothing is held.
	var observe func(context.Context)
	waitProgress := progress
	var held *heldProgress
	if observer != nil {
		held = &heldProgress{out: progress}
		waitProgress = held.add
		observe = func(octx context.Context) {
			defer held.release()
			observer(octx, nodegroupName)
		}
	}
	err = common.RunAlongside(ctx, observe, func(wctx context.Context) error {
		if updateID == "" {
			return nil
		}
		return s.waitForUpdate(wctx, &eks.DescribeUpdateInput{
			Name:          aws.String(clusterName),
			NodegroupName: aws.String(nodegroupName),
			UpdateId:      aws.String(updateID),
		}, fmt.Sprintf("nodegroup %s roll to %s", nodegroupName, targetVersion), waitProgress)
	})
	if err != nil {
		return err
	}
	progress("nodegroup %s is at %s", nodegroupName, targetVersion)
	return nil
}

// heldProgress buffers progress lines until release, then flushes them to out
// in order and passes later lines straight through. add and release may run
// on different goroutines; the lock keeps flushed and new lines in order.
type heldProgress struct {
	mu       sync.Mutex
	out      ProgressFunc
	released bool
	lines    []heldLine
}

type heldLine struct {
	format string
	args   []any
}

func (h *heldProgress) add(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		h.out(format, args...)
		return
	}
	h.lines = append(h.lines, heldLine{format: format, args: args})
}

func (h *heldProgress) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.released = true
	for _, l := range h.lines {
		h.out(l.format, l.args...)
	}
	h.lines = nil
}

// defaultNodegroupGate verifies the nodegroup is ACTIVE and reports no
// health issues before a roll starts.
func (s *Service) defaultNodegroupGate(clusterName string) NodegroupGate {
	return func(ctx context.Context, nodegroupName string) error {
		out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
			return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
				ClusterName:   aws.String(clusterName),
				NodegroupName: aws.String(nodegroupName),
			})
		})
		if err != nil {
			return awsinternal.FormatAWSError(err, fmt.Sprintf("checking nodegroup %s", nodegroupName))
		}
		ng := out.Nodegroup
		if ng == nil {
			return fmt.Errorf("nodegroup %s not found", nodegroupName)
		}
		if ng.Status != ekstypes.NodegroupStatusActive {
			return fmt.Errorf("nodegroup %s is %s, not ACTIVE", nodegroupName, ng.Status)
		}
		if ng.Health != nil && len(ng.Health.Issues) > 0 {
			issue := ng.Health.Issues[0]
			return fmt.Errorf("nodegroup %s has %d health issue(s), first: %s: %s",
				nodegroupName, len(ng.Health.Issues), issue.Code, aws.ToString(issue.Message))
		}
		return nil
	}
}

// waitForNodegroupSettled polls the nodegroup until it leaves the UPDATING /
// CREATING states, honoring ctx, and returns its Kubernetes version at that
// point. Whether the settled state is fit for a roll (ACTIVE, no health
// issues) is left to the pre-flight gate. Like waitForUpdate, it reports
// transient describe failures via progress and keeps polling; only ctx and
// permanent API errors end the wait early.
func (s *Service) waitForNodegroupSettled(ctx context.Context, clusterName, nodegroupName string, progress ProgressFunc) (string, error) {
	interval := s.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
			return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
				ClusterName:   aws.String(clusterName),
				NodegroupName: aws.String(nodegroupName),
			})
		})
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if isPermanentAPIError(err) {
				return "", awsinternal.FormatAWSError(err, fmt.Sprintf("checking nodegroup %s", nodegroupName))
			}
			progress("warning: checking nodegroup %s: %v", nodegroupName, err)
		case out.Nodegroup == nil:
			return "", fmt.Errorf("nodegroup %s not found", nodegroupName)
		case out.Nodegroup.Status != ekstypes.NodegroupStatusUpdating && out.Nodegroup.Status != ekstypes.NodegroupStatusCreating:
			return aws.ToString(out.Nodegroup.Version), nil
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
