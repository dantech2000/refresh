package addons

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/dantech2000/refresh/internal/services/common"
)

// preUpdateHealthCheck verifies the addon is in a stable state before updating.
// It blocks updates if the addon is actively being created or updated; it allows
// updates on DEGRADED state so users can remediate a broken addon.
func (s *ServiceImpl) preUpdateHealthCheck(ctx context.Context, clusterName, addonName string) error {
	desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonOutput, error) {
		return s.eksClient.DescribeAddon(rc, &eks.DescribeAddonInput{
			ClusterName: aws.String(clusterName),
			AddonName:   aws.String(addonName),
		})
	})
	if err != nil {
		return fmt.Errorf("pre-update health check: %w", err)
	}

	switch desc.Addon.Status {
	case ekstypes.AddonStatusActive:
		return nil
	case ekstypes.AddonStatusCreating, ekstypes.AddonStatusUpdating:
		return fmt.Errorf("pre-update health check failed: addon %s is currently %s — wait for it to reach ACTIVE before updating", addonName, desc.Addon.Status)
	default:
		// DEGRADED / CREATE_FAILED / DELETE_FAILED: allow update so users can fix a broken addon
		s.logger.Warn("pre-update health check: addon is not ACTIVE, proceeding anyway",
			"addon", addonName, "status", desc.Addon.Status)
		return nil
	}
}

// postUpdateHealthCheck verifies the addon reached a healthy state after an update
// completes. It is called only when the caller waited for the update to finish.
func (s *ServiceImpl) postUpdateHealthCheck(ctx context.Context, clusterName, addonName string) error {
	desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonOutput, error) {
		return s.eksClient.DescribeAddon(rc, &eks.DescribeAddonInput{
			ClusterName: aws.String(clusterName),
			AddonName:   aws.String(addonName),
		})
	})
	if err != nil {
		return fmt.Errorf("post-update health check: %w", err)
	}

	addon := desc.Addon
	if addon.Status != ekstypes.AddonStatusActive {
		return fmt.Errorf("post-update health check failed: addon %s ended in status %s (expected ACTIVE)",
			addonName, addon.Status)
	}

	if addon.Health != nil && len(addon.Health.Issues) > 0 {
		msgs := make([]string, 0, len(addon.Health.Issues))
		for _, issue := range addon.Health.Issues {
			msgs = append(msgs, fmt.Sprintf("%s: %s", issue.Code, aws.ToString(issue.Message)))
		}
		return fmt.Errorf("post-update health check: addon %s is ACTIVE but has %d health issue(s): %s",
			addonName, len(msgs), strings.Join(msgs, "; "))
	}

	s.logger.Info("post-update health check passed", "addon", addonName, "status", addon.Status)
	return nil
}

// validateVersionCompatibility confirms the target addon version is compatible with
// the cluster's Kubernetes version. Returns nil if compatibility cannot be determined
// (e.g. API error) so a network hiccup doesn't block legitimate updates.
func (s *ServiceImpl) validateVersionCompatibility(ctx context.Context, clusterName, addonName, targetVersion string) error {
	clusterDesc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{
			Name: aws.String(clusterName),
		})
	})
	if err != nil {
		s.logger.Warn("compatibility check: could not describe cluster, skipping", "error", err)
		return nil
	}
	k8sVersion := aws.ToString(clusterDesc.Cluster.Version)

	versions, err := s.GetAvailableVersions(ctx, addonName, k8sVersion)
	if err != nil {
		s.logger.Warn("compatibility check: could not retrieve addon versions, skipping",
			"addon", addonName, "k8sVersion", k8sVersion, "error", err)
		return nil
	}

	for _, v := range versions {
		if v.Version == targetVersion {
			return nil
		}
	}

	return fmt.Errorf("addon %s version %s is not compatible with Kubernetes %s — run 'refresh addon describe %s --show-versions' to see supported versions",
		addonName, targetVersion, k8sVersion, addonName)
}
