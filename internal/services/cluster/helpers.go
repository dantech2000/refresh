package cluster

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/services/common"
)

// getVpcCidr retrieves the CIDR block for a VPC
func (s *ServiceImpl) getVpcCidr(ctx context.Context, vpcId string) (string, error) {
	output, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*ec2.DescribeVpcsOutput, error) {
		return s.ec2Client.DescribeVpcs(rc, &ec2.DescribeVpcsInput{
			VpcIds: []string{vpcId},
		})
	})
	if err != nil {
		return "", awsinternal.FormatAWSError(err, fmt.Sprintf("describing VPC %s", vpcId))
	}

	if len(output.Vpcs) == 0 {
		return "", fmt.Errorf("VPC %s not found", vpcId)
	}

	return aws.ToString(output.Vpcs[0].CidrBlock), nil
}

// getClusterAddons retrieves add-on information for a cluster
func (s *ServiceImpl) getClusterAddons(ctx context.Context, clusterName string) ([]AddonInfo, error) {
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
		return nil, err
	}

	results := common.ForEachParallel(ctx, addonNames, common.DefaultItemConcurrency,
		func(fctx context.Context, addonName string) *AddonInfo {
			describeOutput, err := common.WithRetry(fctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonOutput, error) {
				return s.eksClient.DescribeAddon(rc, &eks.DescribeAddonInput{
					ClusterName: aws.String(clusterName),
					AddonName:   aws.String(addonName),
				})
			})
			if err != nil {
				s.logger.Warn("failed to describe add-on", "cluster", clusterName, "addon", addonName, "error", err)
				return nil
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

			return &AddonInfo{
				Name:    aws.ToString(addon.AddonName),
				Version: aws.ToString(addon.AddonVersion),
				Status:  string(addon.Status),
				Health:  health,
			}
		})

	var addons []AddonInfo
	for _, r := range results {
		if r != nil {
			addons = append(addons, *r)
		}
	}

	return addons, nil
}

// getClusterNodegroups retrieves nodegroup information for a cluster
func (s *ServiceImpl) getClusterNodegroups(ctx context.Context, clusterName string) ([]NodegroupSummary, error) {
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
		return nil, err
	}

	results := common.ForEachParallel(ctx, nodegroupNames, common.DefaultItemConcurrency,
		func(fctx context.Context, nodegroupName string) *NodegroupSummary {
			describeOutput, err := common.WithRetry(fctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
				return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
					ClusterName:   aws.String(clusterName),
					NodegroupName: aws.String(nodegroupName),
				})
			})
			if err != nil {
				s.logger.Warn("failed to describe nodegroup", "cluster", clusterName, "nodegroup", nodegroupName, "error", err)
				return nil
			}

			ng := describeOutput.Nodegroup
			var desiredSize int32
			if ng.ScalingConfig != nil {
				desiredSize = aws.ToInt32(ng.ScalingConfig.DesiredSize)
			}
			readyNodes := int32(0)
			if ng.Status == ekstypes.NodegroupStatusActive {
				readyNodes = desiredSize
			}

			instanceTypes := "Unknown"
			if len(ng.InstanceTypes) > 0 {
				instanceTypes = string(ng.InstanceTypes[0])
			}

			return &NodegroupSummary{
				Name:         aws.ToString(ng.NodegroupName),
				Status:       string(ng.Status),
				InstanceType: instanceTypes,
				DesiredSize:  desiredSize,
				ReadyNodes:   readyNodes,
			}
		})

	var nodegroups []NodegroupSummary
	for _, r := range results {
		if r != nil {
			nodegroups = append(nodegroups, *r)
		}
	}

	return nodegroups, nil
}

// shouldSkipCluster applies filters to determine if a cluster should be skipped.
// Only the "name" filter is supported at the list stage; other filter keys are
// applied later by callers that have already fetched cluster details.
func (s *ServiceImpl) shouldSkipCluster(clusterName string, filters map[string]string) bool {
	if pattern, ok := filters["name"]; ok && !strings.Contains(clusterName, pattern) {
		return true
	}
	return false
}

// getClusterSummary creates a summary for a single cluster. On describe
// failure it returns a minimal "UNKNOWN" summary so callers can still render a
// complete list -- never returns an error.
func (s *ServiceImpl) getClusterSummary(ctx context.Context, clusterName string, options ListOptions) *ClusterSummary {
	output, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
	})
	if err != nil {
		s.logger.Warn("failed to describe cluster, returning minimal summary", "cluster", clusterName, "error", err)
		return &ClusterSummary{
			Name:   clusterName,
			Status: "UNKNOWN",
			Region: s.awsConfig.Region,
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

	if nodegroups, err := s.getClusterNodegroups(ctx, clusterName); err != nil {
		s.logger.Warn("failed to get nodegroups for summary", "cluster", clusterName, "error", err)
	} else {
		var totalReady, totalDesired int32
		for _, ng := range nodegroups {
			totalReady += ng.ReadyNodes
			totalDesired += ng.DesiredSize
		}
		summary.NodeCount = NodeCountInfo{Ready: totalReady, Total: totalDesired}
	}

	if options.ShowHealth && s.healthChecker != nil {
		healthSummary := s.healthChecker.RunAllChecks(ctx, clusterName)
		summary.Health = &healthSummary
	}

	return summary
}

// compareEquality emits a Difference iff extract returns more than one
// distinct value across clusters. Each ValuePair carries the cluster's value.
func compareEquality(clusters []ClusterDetails, extract func(ClusterDetails) any, field, desc, sev string) *Difference {
	seen := map[any]bool{}
	values := make([]ValuePair, len(clusters))
	for i, c := range clusters {
		v := extract(c)
		seen[v] = true
		values[i] = ValuePair{ClusterName: c.Name, Value: v}
	}
	if len(seen) <= 1 {
		return nil
	}
	return &Difference{Field: field, Description: desc, Values: values, Severity: sev}
}

// perClusterDiff emits one Difference per cluster matching predicate.
func perClusterDiff(clusters []ClusterDetails, match func(ClusterDetails) (any, bool), field, descFmt, sev string) []Difference {
	var out []Difference
	for _, c := range clusters {
		v, ok := match(c)
		if !ok {
			continue
		}
		out = append(out, Difference{
			Field:       field,
			Description: fmt.Sprintf(descFmt, c.Name),
			Values:      []ValuePair{{ClusterName: c.Name, Value: v}},
			Severity:    sev,
		})
	}
	return out
}

// analyzeDifferences compares clusters and identifies differences
func (s *ServiceImpl) analyzeDifferences(clusters []ClusterDetails, options CompareOptions) []Difference {
	var differences []Difference

	if len(clusters) < 2 {
		return differences
	}

	if s.shouldIncludeField("versions", options.Include) {
		if d := compareEquality(clusters,
			func(c ClusterDetails) any { return c.Version },
			"version", "Kubernetes version differs between clusters", "warning"); d != nil {
			differences = append(differences, *d)
		}
	}

	if s.shouldIncludeField("networking", options.Include) {
		if d := compareEquality(clusters,
			func(c ClusterDetails) any { return c.Networking.VpcId },
			"networking.vpcId", "VPC configuration differs between clusters", "info"); d != nil {
			differences = append(differences, *d)
		}
		differences = append(differences, perClusterDiff(clusters,
			func(c ClusterDetails) (any, bool) {
				ea := c.Networking.EndpointAccess
				if !ea.PrivateAccess && ea.PublicAccess {
					return map[string]bool{"private": ea.PrivateAccess, "public": ea.PublicAccess}, true
				}
				return nil, false
			},
			"networking.endpointAccess", "Cluster %s has public-only endpoint access", "warning")...)
	}

	if s.shouldIncludeField("security", options.Include) {
		differences = append(differences, perClusterDiff(clusters,
			func(c ClusterDetails) (any, bool) {
				return c.Security.EncryptionEnabled, !c.Security.EncryptionEnabled
			},
			"security.encryption", "Cluster %s does not have encryption enabled", "critical")...)
		differences = append(differences, perClusterDiff(clusters,
			func(c ClusterDetails) (any, bool) {
				return c.Security.LoggingEnabled, len(c.Security.LoggingEnabled) == 0
			},
			"security.logging", "Cluster %s has no logging enabled", "warning")...)
	}

	if s.shouldIncludeField("addons", options.Include) {
		differences = append(differences, analyzeAddonDifferences(clusters)...)
	}

	return differences
}

// analyzeAddonDifferences emits per-addon Differences for missing addons and
// version drift across the given clusters.
func analyzeAddonDifferences(clusters []ClusterDetails) []Difference {
	addonsByCluster := make(map[string]map[string]string, len(clusters))
	allAddons := make(map[string]bool)
	for _, c := range clusters {
		m := make(map[string]string, len(c.Addons))
		for _, a := range c.Addons {
			m[a.Name] = a.Version
			allAddons[a.Name] = true
		}
		addonsByCluster[c.Name] = m
	}

	var out []Difference
	for addonName := range allAddons {
		var missing []string
		versions := map[string]bool{}
		for _, c := range clusters {
			if v, ok := addonsByCluster[c.Name][addonName]; ok {
				versions[v] = true
			} else {
				missing = append(missing, c.Name)
			}
		}

		switch {
		case len(missing) > 0:
			values := make([]ValuePair, len(clusters))
			for i, c := range clusters {
				v := "missing"
				if got, ok := addonsByCluster[c.Name][addonName]; ok {
					v = got
				}
				values[i] = ValuePair{ClusterName: c.Name, Value: v}
			}
			out = append(out, Difference{
				Field:       fmt.Sprintf("addons.%s", addonName),
				Description: fmt.Sprintf("Add-on %s is missing from some clusters: %s", addonName, strings.Join(missing, ", ")),
				Values:      values,
				Severity:    "warning",
			})
		case len(versions) > 1:
			values := make([]ValuePair, len(clusters))
			for i, c := range clusters {
				values[i] = ValuePair{ClusterName: c.Name, Value: addonsByCluster[c.Name][addonName]}
			}
			out = append(out, Difference{
				Field:       fmt.Sprintf("addons.%s.version", addonName),
				Description: fmt.Sprintf("Add-on %s has different versions across clusters", addonName),
				Values:      values,
				Severity:    "info",
			})
		}
	}
	return out
}

// shouldIncludeField checks if a field should be included in comparison.
// Empty include slice means "include all".
func (s *ServiceImpl) shouldIncludeField(field string, include []string) bool {
	if len(include) == 0 {
		return true
	}
	return slices.Contains(include, field)
}
