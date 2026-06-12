package upgrade

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/services/common"
)

// UpgradeControlPlane moves the cluster's control plane to targetVersion and
// waits until the update finishes and the cluster is ACTIVE again.
//
// Idempotent: if the control plane already runs targetVersion (or newer) it
// returns immediately; if an update is already in flight it attaches and
// watches instead of failing. On context cancellation (Ctrl+C) it returns
// ctx.Err() — the upgrade keeps running server-side and a rerun re-attaches.
func (s *Service) UpgradeControlPlane(ctx context.Context, clusterName, targetVersion string, progress ProgressFunc) error {
	progress = ensureProgress(progress)

	cluster, err := s.describeCluster(ctx, clusterName)
	if err != nil {
		return err
	}

	if versionAtLeast(aws.ToString(cluster.Version), targetVersion) {
		progress("control plane already at %s, skipping", aws.ToString(cluster.Version))
		return nil
	}

	if cluster.Status == ekstypes.ClusterStatusUpdating {
		// Another update (possibly a previous run of this orchestrator) is in
		// flight; attach and watch rather than fail.
		progress("cluster is already UPDATING; attaching to the in-flight update")
		if err := s.waitForClusterActive(ctx, clusterName, progress); err != nil {
			return err
		}
		cluster, err = s.describeCluster(ctx, clusterName)
		if err != nil {
			return err
		}
		if versionAtLeast(aws.ToString(cluster.Version), targetVersion) {
			progress("control plane reached %s", aws.ToString(cluster.Version))
			return nil
		}
		// The in-flight update was something else (e.g. config change);
		// fall through and start the version upgrade.
	}

	input := &eks.UpdateClusterVersionInput{
		Name:    aws.String(clusterName),
		Version: aws.String(targetVersion),
		// Pin the idempotency token so WithRetry re-issues the SAME request
		// instead of submitting a fresh update per attempt.
		ClientRequestToken: aws.String(common.IdempotencyToken()),
	}
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.UpdateClusterVersionOutput, error) {
		return s.eksClient.UpdateClusterVersion(rc, input)
	})
	if err != nil {
		return awsinternal.FormatAWSError(err, fmt.Sprintf("upgrading control plane of %s to %s", clusterName, targetVersion))
	}

	updateID := ""
	if out.Update != nil {
		updateID = aws.ToString(out.Update.Id)
	}
	progress("control plane upgrade to %s started (update %s); this typically takes ~10 minutes", targetVersion, updateID)

	if updateID != "" {
		if err := s.waitForUpdate(ctx, &eks.DescribeUpdateInput{
			Name:     aws.String(clusterName),
			UpdateId: aws.String(updateID),
		}, fmt.Sprintf("control plane upgrade to %s", targetVersion), progress); err != nil {
			return err
		}
	}

	// Gate: the cluster itself must be ACTIVE at the new version before the
	// addon phase may start.
	if err := s.waitForClusterActive(ctx, clusterName, progress); err != nil {
		return err
	}
	cluster, err = s.describeCluster(ctx, clusterName)
	if err != nil {
		return err
	}
	if !versionAtLeast(aws.ToString(cluster.Version), targetVersion) {
		return fmt.Errorf("control plane reports version %s after the upgrade to %s finished", aws.ToString(cluster.Version), targetVersion)
	}
	progress("control plane is ACTIVE at %s", aws.ToString(cluster.Version))
	return nil
}

// waitForClusterActive polls the cluster until its status is ACTIVE.
func (s *Service) waitForClusterActive(ctx context.Context, clusterName string, progress ProgressFunc) error {
	interval := s.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		cluster, err := s.describeCluster(ctx, clusterName)
		if err != nil {
			return err
		}
		switch cluster.Status {
		case ekstypes.ClusterStatusActive:
			return nil
		case ekstypes.ClusterStatusFailed:
			return fmt.Errorf("cluster %s entered FAILED status", clusterName)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
