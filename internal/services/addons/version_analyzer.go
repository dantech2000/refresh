package addons

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
)

// GetAvailableVersions returns available versions for an addon, newest first.
// Pass k8sVersion to restrict results to versions compatible with that
// Kubernetes version. All pages are fetched and the result is sorted by
// version (the API does not document an ordering guarantee).
func (s *ServiceImpl) GetAvailableVersions(ctx context.Context, addonName string, k8sVersion string) ([]AddonVersionInfo, error) {
	newInput := func(token *string) *eks.DescribeAddonVersionsInput {
		input := &eks.DescribeAddonVersionsInput{
			AddonName: aws.String(addonName),
			NextToken: token,
		}
		if k8sVersion != "" {
			input.KubernetesVersion = aws.String(k8sVersion)
		}
		return input
	}

	addonInfos, err := awsinternal.ListAllPages(ctx, fmt.Sprintf("describing versions for addon %s", addonName),
		func(rc context.Context, token *string) (*eks.DescribeAddonVersionsOutput, error) {
			return s.eksClient.DescribeAddonVersions(rc, newInput(token))
		},
		func(out *eks.DescribeAddonVersionsOutput) ([]ekstypes.AddonInfo, *string) { return out.Addons, out.NextToken },
	)
	if err != nil {
		return nil, fmt.Errorf("describing addon versions: %w", err)
	}

	var versions []AddonVersionInfo
	for _, info := range addonInfos {
		for _, v := range info.AddonVersions {
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
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("no versions found for addon %s", addonName)
	}

	// Newest first: callers treat versions[0] as "latest".
	sort.SliceStable(versions, func(i, j int) bool {
		return compareAddonVersions(versions[i].Version, versions[j].Version) > 0
	})

	return versions, nil
}

// compareAddonVersions compares EKS addon version strings such as
// "v1.18.1-eksbuild.3", returning >0 when a is newer than b. Numeric segments
// are compared numerically; non-numeric segments lexically.
func compareAddonVersions(a, b string) int {
	segs := func(v string) []string {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		return strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
	}
	as, bs := segs(a), segs(b)
	for i := 0; i < len(as) && i < len(bs); i++ {
		ai, aerr := strconv.Atoi(as[i])
		bi, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil:
			if ai != bi {
				return ai - bi
			}
		case aerr == nil:
			return 1 // numeric beats non-numeric ("1" > "eksbuild")
		case berr == nil:
			return -1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return len(as) - len(bs)
}

// waitForAddonUpdate polls until an addon update completes.
func (s *ServiceImpl) waitForAddonUpdate(ctx context.Context, clusterName, addonName string) error {
	ticker := time.NewTicker(5 * time.Second)
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
			case ekstypes.AddonStatusDegraded, ekstypes.AddonStatusCreateFailed,
				ekstypes.AddonStatusUpdateFailed, ekstypes.AddonStatusDeleteFailed:
				return fmt.Errorf("addon update failed: status %s", desc.Addon.Status)
			}
		}
	}
}

func mapAddonHealth(status ekstypes.AddonStatus) string {
	switch status {
	case ekstypes.AddonStatusActive:
		return "PASS"
	case ekstypes.AddonStatusDegraded, ekstypes.AddonStatusCreateFailed,
		ekstypes.AddonStatusUpdateFailed, ekstypes.AddonStatusDeleteFailed:
		return "FAIL"
	case ekstypes.AddonStatusCreating, ekstypes.AddonStatusDeleting, ekstypes.AddonStatusUpdating:
		return "IN_PROGRESS"
	default:
		return "UNKNOWN"
	}
}
