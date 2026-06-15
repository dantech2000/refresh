package upgrade

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/services/common"
)

// NodegroupGate is a pre-flight check run before each nodegroup roll. A nil
// gate falls back to the built-in check (nodegroup ACTIVE with no reported
// health issues).
type NodegroupGate func(ctx context.Context, nodegroupName string) error

// RollObserver renders a live view of a single nodegroup roll. It is supplied
// by the command (view) layer and invoked by the nodegroup phase AFTER a roll
// starts and BEFORE the authoritative DescribeUpdate wait — so rendering never
// happens in the service itself. It must be best-effort and bounded (it must
// not block the roll or affect its result); a nil observer means text progress
// only.
type RollObserver func(ctx context.Context, nodegroupName string)

// NodegroupRollOptions tunes the nodegroup phase.
type NodegroupRollOptions struct {
	// SkipPatterns are substring patterns for nodegroups to leave alone.
	SkipPatterns []string
	// Force terminates pods that can't be drained due to PDBs (passed
	// through to UpdateNodegroupVersion).
	Force bool
	// Gate overrides the built-in pre-flight health gate.
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

	gate := opts.Gate
	if gate == nil {
		gate = s.defaultNodegroupGate(clusterName)
	}

	for _, ng := range nodegroups {
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

		if err := gate(ctx, ng.Name); err != nil {
			return fmt.Errorf("pre-flight gate failed for nodegroup %s (remaining nodegroups not attempted): %w", ng.Name, err)
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

	// Live per-node panel (view layer, best-effort) while the roll proceeds; the
	// DescribeUpdate wait below stays authoritative for the result.
	if observer != nil {
		observer(ctx, nodegroupName)
	}

	if updateID != "" {
		if err := s.waitForUpdate(ctx, &eks.DescribeUpdateInput{
			Name:          aws.String(clusterName),
			NodegroupName: aws.String(nodegroupName),
			UpdateId:      aws.String(updateID),
		}, fmt.Sprintf("nodegroup %s roll to %s", nodegroupName, targetVersion), progress); err != nil {
			return err
		}
	}
	progress("nodegroup %s is at %s", nodegroupName, targetVersion)
	return nil
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
