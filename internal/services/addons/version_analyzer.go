package addons

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/dantech2000/refresh/internal/services/common"
)

// GetAvailableVersions returns available versions for an addon.
func (s *ServiceImpl) GetAvailableVersions(ctx context.Context, addonName string, k8sVersion string) ([]AddonVersionInfo, error) {
	input := &eks.DescribeAddonVersionsInput{
		AddonName: aws.String(addonName),
	}
	if k8sVersion != "" {
		input.KubernetesVersion = aws.String(k8sVersion)
	}

	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonVersionsOutput, error) {
		return s.eksClient.DescribeAddonVersions(rc, input)
	})
	if err != nil {
		return nil, fmt.Errorf("describing addon versions: %w", err)
	}

	if len(out.Addons) == 0 || len(out.Addons[0].AddonVersions) == 0 {
		return nil, fmt.Errorf("no versions found for addon %s", addonName)
	}

	versions := make([]AddonVersionInfo, 0, len(out.Addons[0].AddonVersions))
	for _, v := range out.Addons[0].AddonVersions {
		var compatibilities []string
		for _, c := range v.Compatibilities {
			if c.ClusterVersion != nil {
				compatibilities = append(compatibilities, *c.ClusterVersion)
			}
		}
		versions = append(versions, AddonVersionInfo{
			Version:           aws.ToString(v.AddonVersion),
			Compatibilities:   compatibilities,
			Architecture:      append([]string{}, v.Architecture...),
			RequiresIAMPolicy: v.RequiresIamPermissions,
		})
	}

	return versions, nil
}

// waitForAddonUpdate polls until an addon update completes.
func (s *ServiceImpl) waitForAddonUpdate(ctx context.Context, clusterName, addonName string) error {
	ticker := time.NewTicker(addonUpdatePollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			desc, err := s.eksClient.DescribeAddon(ctx, &eks.DescribeAddonInput{
				ClusterName: aws.String(clusterName),
				AddonName:   aws.String(addonName),
			})
			if err != nil {
				continue
			}
			switch desc.Addon.Status {
			case ekstypes.AddonStatusActive:
				return nil
			case ekstypes.AddonStatusDegraded, ekstypes.AddonStatusCreateFailed:
				return fmt.Errorf("addon update failed: status %s", desc.Addon.Status)
			}
		}
	}
}

func mapAddonHealth(status ekstypes.AddonStatus) string {
	switch status {
	case ekstypes.AddonStatusActive:
		return "PASS"
	case ekstypes.AddonStatusDegraded, ekstypes.AddonStatusCreateFailed, ekstypes.AddonStatusDeleteFailed:
		return "FAIL"
	case ekstypes.AddonStatusCreating, ekstypes.AddonStatusDeleting, ekstypes.AddonStatusUpdating:
		return "IN_PROGRESS"
	default:
		return "UNKNOWN"
	}
}

func countVersionsBehind(current string, versions []AddonVersionInfo) int {
	for i, v := range versions {
		if v.Version == current {
			return i
		}
	}
	return len(versions)
}
