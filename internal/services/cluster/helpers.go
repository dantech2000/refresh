package cluster

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/services/common"
)

// getVpcCidr retrieves the CIDR block for a VPC
func (s *ServiceImpl) getVpcCidr(ctx context.Context, vpcID string) (string, error) {
	output, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*ec2.DescribeVpcsOutput, error) {
		return s.ec2Client.DescribeVpcs(rc, &ec2.DescribeVpcsInput{
			VpcIds: []string{vpcID},
		})
	})
	if err != nil {
		return "", awsinternal.FormatAWSError(err, fmt.Sprintf("describing VPC %s", vpcID))
	}

	if len(output.Vpcs) == 0 {
		return "", fmt.Errorf("VPC %s not found", vpcID)
	}

	return aws.ToString(output.Vpcs[0].CidrBlock), nil
}

// itemResult is one item of a per-item fan-out: the value, or why it is
// missing. A zero itemResult means the item never ran (the context ended).
type itemResult[T any] struct {
	value   *T
	failure string
	ran     bool
}

// collectItems splits fan-out results into the values and one warning per
// missing item, in input order.
func collectItems[T any](ctx context.Context, kind string, names []string, results []itemResult[T]) (values []T, warnings []string) {
	for i, r := range results {
		switch {
		case !r.ran:
			reason := "sweep stopped early"
			if cause := context.Cause(ctx); cause != nil {
				reason = cause.Error()
			}
			warnings = append(warnings, fmt.Sprintf("%s %s: not evaluated: %s", kind, names[i], reason))
		case r.failure != "":
			warnings = append(warnings, fmt.Sprintf("%s %s: %s", kind, names[i], r.failure))
		case r.value != nil:
			values = append(values, *r.value)
		}
	}
	return values, warnings
}

// getClusterAddons retrieves add-on information for a cluster. warnings
// names each add-on that could not be described.
func (s *ServiceImpl) getClusterAddons(ctx context.Context, clusterName string) (addons []AddonInfo, warnings []string, err error) {
	addonNames, err := awsinternal.ListAllPages(ctx, fmt.Sprintf("listing add-ons for cluster %s", clusterName),
		func(rc context.Context, token *string) (*eks.ListAddonsOutput, error) {
			return s.eksClient.ListAddons(rc, &eks.ListAddonsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   token,
			})
		},
		func(out *eks.ListAddonsOutput) ([]string, *string) { return out.Addons, out.NextToken },
	)
	if err != nil {
		return nil, nil, err
	}

	results := common.ForEachParallel(ctx, addonNames, common.DefaultItemConcurrency,
		func(fctx context.Context, addonName string) itemResult[AddonInfo] {
			describeOutput, err := common.WithRetry(fctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonOutput, error) {
				return s.eksClient.DescribeAddon(rc, &eks.DescribeAddonInput{
					ClusterName: aws.String(clusterName),
					AddonName:   aws.String(addonName),
				})
			})
			if err != nil {
				s.logger.Debug("failed to describe add-on", "cluster", clusterName, "addon", addonName, "error", err)
				return itemResult[AddonInfo]{ran: true, failure: awserr.Summary(err)}
			}
			if describeOutput == nil || describeOutput.Addon == nil {
				s.logger.Debug("empty add-on describe response", "cluster", clusterName, "addon", addonName)
				return itemResult[AddonInfo]{ran: true, failure: "empty DescribeAddon response"}
			}

			addon := describeOutput.Addon
			health := "Unknown"

			// Determine health status based on add-on status
			switch addon.Status {
			case ekstypes.AddonStatusActive:
				health = "Healthy"
			case ekstypes.AddonStatusDegraded:
				health = "Issues"
			case ekstypes.AddonStatusCreateFailed, ekstypes.AddonStatusDeleteFailed, ekstypes.AddonStatusUpdateFailed:
				health = "Failed"
			case ekstypes.AddonStatusCreating, ekstypes.AddonStatusDeleting, ekstypes.AddonStatusUpdating:
				health = "Updating"
			}

			return itemResult[AddonInfo]{ran: true, value: &AddonInfo{
				Name:    aws.ToString(addon.AddonName),
				Version: aws.ToString(addon.AddonVersion),
				Status:  string(addon.Status),
				Health:  health,
			}}
		})

	addons, warnings = collectItems(ctx, "add-on", addonNames, results)
	return addons, warnings, nil
}

// getClusterNodegroups retrieves nodegroup information for a cluster.
// warnings names each nodegroup that could not be described.
func (s *ServiceImpl) getClusterNodegroups(ctx context.Context, clusterName string) (nodegroups []NodegroupSummary, warnings []string, err error) {
	nodegroupNames, err := awsinternal.ListAllPages(ctx, fmt.Sprintf("listing nodegroups for cluster %s", clusterName),
		func(rc context.Context, token *string) (*eks.ListNodegroupsOutput, error) {
			return s.eksClient.ListNodegroups(rc, &eks.ListNodegroupsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   token,
			})
		},
		func(out *eks.ListNodegroupsOutput) ([]string, *string) { return out.Nodegroups, out.NextToken },
	)
	if err != nil {
		return nil, nil, err
	}

	// Measured Kubernetes Ready counts per nodegroup, fetched once when a
	// cluster-connected health checker is wired (--check-readiness). Otherwise
	// readiness is honestly unknown rather than the old "ready = desired when
	// ACTIVE" tautology. (REF-130)
	readyByNG, haveReady := s.nodegroupReadyCounts(ctx)

	results := common.ForEachParallel(ctx, nodegroupNames, common.DefaultItemConcurrency,
		func(fctx context.Context, nodegroupName string) itemResult[NodegroupSummary] {
			describeOutput, err := common.WithRetry(fctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
				return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
					ClusterName:   aws.String(clusterName),
					NodegroupName: aws.String(nodegroupName),
				})
			})
			if err != nil {
				s.logger.Debug("failed to describe nodegroup", "cluster", clusterName, "nodegroup", nodegroupName, "error", err)
				return itemResult[NodegroupSummary]{ran: true, failure: awserr.Summary(err)}
			}
			if describeOutput == nil || describeOutput.Nodegroup == nil {
				s.logger.Debug("empty nodegroup describe response", "cluster", clusterName, "nodegroup", nodegroupName)
				return itemResult[NodegroupSummary]{ran: true, failure: "empty DescribeNodegroup response"}
			}

			ng := describeOutput.Nodegroup
			var desiredSize int32
			if ng.ScalingConfig != nil {
				desiredSize = aws.ToInt32(ng.ScalingConfig.DesiredSize)
			}
			readyNodes := int32(0)
			readyKnown := false
			if haveReady {
				readyNodes = readyByNG[nodegroupName]
				readyKnown = true
			}

			instanceTypes := "Unknown"
			if len(ng.InstanceTypes) > 0 {
				instanceTypes = ng.InstanceTypes[0]
			}

			return itemResult[NodegroupSummary]{ran: true, value: &NodegroupSummary{
				Name:         aws.ToString(ng.NodegroupName),
				Status:       string(ng.Status),
				InstanceType: instanceTypes,
				DesiredSize:  desiredSize,
				ReadyNodes:   readyNodes,
				ReadyKnown:   readyKnown,
			}}
		})

	nodegroups, warnings = collectItems(ctx, "nodegroup", nodegroupNames, results)
	return nodegroups, warnings, nil
}

// nodegroupReadyCounts returns measured Kubernetes Ready=True counts per
// nodegroup, or (nil, false) when no cluster-connected health checker is wired.
// (REF-130)
func (s *ServiceImpl) nodegroupReadyCounts(ctx context.Context) (map[string]int32, bool) {
	if s.healthChecker == nil {
		return nil, false
	}
	return s.healthChecker.NodegroupReadyCounts(ctx)
}

// shouldSkipCluster applies filters to determine if a cluster should be skipped.
// Only the "name" filter is supported at the list stage; "status"/"version" need
// the per-cluster summary and are applied afterwards by filterSummaries.
func (s *ServiceImpl) shouldSkipCluster(clusterName string, filters map[string]string) bool {
	if pattern, ok := filters["name"]; ok && !strings.Contains(clusterName, pattern) {
		return true
	}
	return false
}

// filterSummaries applies the "status"/"version" filters that can only be
// evaluated once each cluster's summary (and thus its Status/Version) has been
// fetched. Matching is case-insensitive and exact. The "name" filter is already
// applied at the list stage by shouldSkipCluster. (REF-1)
func filterSummaries(summaries []ClusterSummary, filters map[string]string) []ClusterSummary {
	status, hasStatus := filters["status"]
	version, hasVersion := filters["version"]
	if !hasStatus && !hasVersion {
		return summaries
	}
	out := make([]ClusterSummary, 0, len(summaries))
	for _, s := range summaries {
		if hasStatus && !strings.EqualFold(s.Status, status) {
			continue
		}
		if hasVersion && !strings.EqualFold(s.Version, version) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// getClusterSummary creates a summary for a single cluster. On describe
// failure it returns a minimal "UNKNOWN" summary so callers can still render a
// complete list -- never returns an error.
func (s *ServiceImpl) getClusterSummary(ctx context.Context, clusterName string, options ListOptions) *ClusterSummary {
	output, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
	})
	if err != nil || output.Cluster == nil {
		if err == nil {
			err = fmt.Errorf("empty DescribeCluster response")
		}
		s.logger.Debug("failed to describe cluster, returning minimal summary", "cluster", clusterName, "error", err)
		return &ClusterSummary{
			Name:     clusterName,
			Status:   "UNKNOWN",
			Region:   s.awsConfig.Region,
			Warnings: []string{"could not describe cluster: " + awserr.Summary(err)},
		}
	}

	cluster := output.Cluster
	summary := &ClusterSummary{
		Name:      aws.ToString(cluster.Name),
		Status:    string(cluster.Status),
		Version:   aws.ToString(cluster.Version),
		Region:    s.awsConfig.Region,
		CreatedAt: aws.ToTime(cluster.CreatedAt),
		Tags:      cluster.Tags,
	}

	if nodegroups, warnings, err := s.getClusterNodegroups(ctx, clusterName); err != nil {
		s.logger.Debug("failed to get nodegroups for summary", "cluster", clusterName, "error", err)
		summary.Warnings = append(summary.Warnings, "could not list nodegroups (node counts unknown): "+awserr.Summary(err))
	} else {
		summary.Warnings = append(summary.Warnings, warnings...)
		// Ready is meaningful only when every nodegroup's readiness was measured;
		// if any is unknown, the aggregate Ready is not a real count. (REF-130)
		var totalReady, totalDesired int32
		readyKnown := len(nodegroups) > 0
		for _, ng := range nodegroups {
			totalReady += ng.ReadyNodes
			totalDesired += ng.DesiredSize
			if !ng.ReadyKnown {
				readyKnown = false
			}
		}
		summary.NodeCount = NodeCountInfo{Ready: totalReady, Total: totalDesired, ReadyKnown: readyKnown}
	}

	if options.ShowHealth && s.healthChecker != nil {
		healthSummary := s.healthChecker.RunAllChecks(ctx, clusterName)
		summary.Health = &healthSummary
	}

	return summary
}
