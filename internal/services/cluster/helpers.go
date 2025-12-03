package cluster

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// List add-ons with pagination
	var addonNames []string
	var nextToken *string
	for {
		listOutput, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.ListAddonsOutput, error) {
			return s.eksClient.ListAddons(rc, &eks.ListAddonsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   nextToken,
			})
		})
		if err != nil {
			return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("listing add-ons for cluster %s", clusterName))
		}
		addonNames = append(addonNames, listOutput.Addons...)
		if listOutput.NextToken == nil {
			break
		}
		nextToken = listOutput.NextToken
	}

	var addons []AddonInfo

	for _, addonName := range addonNames {
		describeOutput, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonOutput, error) {
			return s.eksClient.DescribeAddon(rc, &eks.DescribeAddonInput{
				ClusterName: aws.String(clusterName),
				AddonName:   aws.String(addonName),
			})
		})
		if err != nil {
			s.logger.Warn("failed to describe add-on", "cluster", clusterName, "addon", addonName, "error", err)
			continue
		}

		addon := describeOutput.Addon
		health := "Unknown"

		// Determine health status based on add-on status
		switch addon.Status {
		case ekstypes.AddonStatusActive:
			health = "Healthy"
		case ekstypes.AddonStatusDegraded:
			health = "Issues"
		case ekstypes.AddonStatusCreateFailed, ekstypes.AddonStatusDeleteFailed:
			health = "Failed"
		case ekstypes.AddonStatusCreating, ekstypes.AddonStatusDeleting, ekstypes.AddonStatusUpdating:
			health = "Updating"
		}

		addons = append(addons, AddonInfo{
			Name:    aws.ToString(addon.AddonName),
			Version: aws.ToString(addon.AddonVersion),
			Status:  string(addon.Status),
			Health:  health,
		})
	}

	return addons, nil
}

// getClusterNodegroups retrieves nodegroup information for a cluster
func (s *ServiceImpl) getClusterNodegroups(ctx context.Context, clusterName string) ([]NodegroupSummary, error) {
	// List nodegroups with pagination
	var nodegroupNames []string
	var nextToken *string
	for {
		listOutput, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.ListNodegroupsOutput, error) {
			return s.eksClient.ListNodegroups(rc, &eks.ListNodegroupsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   nextToken,
			})
		})
		if err != nil {
			return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("listing nodegroups for cluster %s", clusterName))
		}
		nodegroupNames = append(nodegroupNames, listOutput.Nodegroups...)
		if listOutput.NextToken == nil {
			break
		}
		nextToken = listOutput.NextToken
	}

	var nodegroups []NodegroupSummary

	for _, nodegroupName := range nodegroupNames {
		describeOutput, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
			return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
				ClusterName:   aws.String(clusterName),
				NodegroupName: aws.String(nodegroupName),
			})
		})
		if err != nil {
			s.logger.Warn("failed to describe nodegroup", "cluster", clusterName, "nodegroup", nodegroupName, "error", err)
			continue
		}

		ng := describeOutput.Nodegroup
		readyNodes := int32(0)

		// Calculate ready nodes based on scaling config and status
		if ng.ScalingConfig != nil && ng.Status == ekstypes.NodegroupStatusActive {
			readyNodes = aws.ToInt32(ng.ScalingConfig.DesiredSize)
		}

		instanceTypes := "Unknown"
		if len(ng.InstanceTypes) > 0 {
			instanceTypes = string(ng.InstanceTypes[0])
		}

		nodegroups = append(nodegroups, NodegroupSummary{
			Name:         aws.ToString(ng.NodegroupName),
			Status:       string(ng.Status),
			InstanceType: instanceTypes,
			DesiredSize:  aws.ToInt32(ng.ScalingConfig.DesiredSize),
			ReadyNodes:   readyNodes,
		})
	}

	return nodegroups, nil
}

// shouldSkipCluster applies filters to determine if a cluster should be skipped
func (s *ServiceImpl) shouldSkipCluster(clusterName string, filters map[string]string) bool {
	if len(filters) == 0 {
		return false
	}

	for key, value := range filters {
		switch key {
		case "name":
			if !strings.Contains(clusterName, value) {
				return true
			}
		case "status":
			// We'd need to get cluster details to filter by status
			// For now, skip this filter to avoid extra API calls
			continue
		}
	}

	return false
}

// getClusterSummary creates a summary for a single cluster
func (s *ServiceImpl) getClusterSummary(ctx context.Context, clusterName string, options ListOptions) (*ClusterSummary, error) {
	// Get basic cluster information
	output, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
	})
	if err != nil {
		// Fallback: return minimal summary so list output remains complete even if a describe call fails
		s.logger.Warn("failed to describe cluster, returning minimal summary", "cluster", clusterName, "error", err)
		return &ClusterSummary{
			Name:      clusterName,
			Status:    "UNKNOWN",
			Version:   "",
			Region:    s.awsConfig.Region,
			CreatedAt: time.Time{},
		}, nil
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

	// Get node count information
	nodegroups, err := s.getClusterNodegroups(ctx, clusterName)
	if err != nil {
		s.logger.Warn("failed to get nodegroups for summary", "cluster", clusterName, "error", err)
	} else {
		var totalReady, totalDesired int32
		for _, ng := range nodegroups {
			totalReady += ng.ReadyNodes
			totalDesired += ng.DesiredSize
		}
		summary.NodeCount = NodeCountInfo{Ready: totalReady, Total: totalDesired}

		// If a Kubernetes client is available, compute actual ready nodes
		if k8sClient, kerr := health.GetKubernetesClient(); kerr == nil && k8sClient != nil {
			if nodeList, lerr := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); lerr == nil {
				var readyCount int32
				for _, node := range nodeList.Items {
					for _, cond := range node.Status.Conditions {
						if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
							readyCount++
							break
						}
					}
				}
				// Use actual ready node count but keep desired as total
				summary.NodeCount.Ready = readyCount
			} else {
				s.logger.Debug("failed to list k8s nodes for readiness", "cluster", clusterName, "error", lerr)
			}
		}
	}

	// Add health information if requested
	if options.ShowHealth && s.healthChecker != nil {
		healthSummary := s.healthChecker.RunAllChecks(ctx, clusterName)
		summary.Health = &healthSummary
	}

	return summary, nil
}

// analyzeDifferences compares clusters and identifies differences
func (s *ServiceImpl) analyzeDifferences(clusters []ClusterDetails, options CompareOptions) []Difference {
	var differences []Difference

	if len(clusters) < 2 {
		return differences
	}

	// Compare Kubernetes versions
	if s.shouldIncludeField("versions", options.Include) {
		versions := make(map[string][]string)
		for _, cluster := range clusters {
			version := cluster.Version
			if _, exists := versions[version]; !exists {
				versions[version] = []string{}
			}
			versions[version] = append(versions[version], cluster.Name)
		}

		if len(versions) > 1 {
			var values []ValuePair
			for _, cluster := range clusters {
				values = append(values, ValuePair{
					ClusterName: cluster.Name,
					Value:       cluster.Version,
				})
			}

			differences = append(differences, Difference{
				Field:       "version",
				Description: "Kubernetes version differs between clusters",
				Values:      values,
				Severity:    "warning",
			})
		}
	}

	// Compare networking configuration
	if s.shouldIncludeField("networking", options.Include) {
		// Check VPC IDs
		vpcIds := make(map[string][]string)
		for _, cluster := range clusters {
			vpcId := cluster.Networking.VpcId
			if _, exists := vpcIds[vpcId]; !exists {
				vpcIds[vpcId] = []string{}
			}
			vpcIds[vpcId] = append(vpcIds[vpcId], cluster.Name)
		}

		if len(vpcIds) > 1 {
			var values []ValuePair
			for _, cluster := range clusters {
				values = append(values, ValuePair{
					ClusterName: cluster.Name,
					Value:       cluster.Networking.VpcId,
				})
			}

			differences = append(differences, Difference{
				Field:       "networking.vpcId",
				Description: "VPC configuration differs between clusters",
				Values:      values,
				Severity:    "info",
			})
		}

		// Check endpoint access
		for _, cluster := range clusters {
			if !cluster.Networking.EndpointAccess.PrivateAccess && cluster.Networking.EndpointAccess.PublicAccess {
				differences = append(differences, Difference{
					Field:       "networking.endpointAccess",
					Description: fmt.Sprintf("Cluster %s has public-only endpoint access", cluster.Name),
					Values: []ValuePair{
						{
							ClusterName: cluster.Name,
							Value: map[string]bool{
								"private": cluster.Networking.EndpointAccess.PrivateAccess,
								"public":  cluster.Networking.EndpointAccess.PublicAccess,
							},
						},
					},
					Severity: "warning",
				})
			}
		}
	}

	// Compare security configuration
	if s.shouldIncludeField("security", options.Include) {
		// Check encryption
		for _, cluster := range clusters {
			if !cluster.Security.EncryptionEnabled {
				differences = append(differences, Difference{
					Field:       "security.encryption",
					Description: fmt.Sprintf("Cluster %s does not have encryption enabled", cluster.Name),
					Values: []ValuePair{
						{
							ClusterName: cluster.Name,
							Value:       cluster.Security.EncryptionEnabled,
						},
					},
					Severity: "critical",
				})
			}
		}

		// Check logging
		for _, cluster := range clusters {
			if len(cluster.Security.LoggingEnabled) == 0 {
				differences = append(differences, Difference{
					Field:       "security.logging",
					Description: fmt.Sprintf("Cluster %s has no logging enabled", cluster.Name),
					Values: []ValuePair{
						{
							ClusterName: cluster.Name,
							Value:       cluster.Security.LoggingEnabled,
						},
					},
					Severity: "warning",
				})
			}
		}
	}

	// Compare add-ons
	if s.shouldIncludeField("addons", options.Include) {
		addonsByCluster := make(map[string]map[string]string)
		allAddons := make(map[string]bool)

		for _, cluster := range clusters {
			addonsByCluster[cluster.Name] = make(map[string]string)
			for _, addon := range cluster.Addons {
				addonsByCluster[cluster.Name][addon.Name] = addon.Version
				allAddons[addon.Name] = true
			}
		}

		// Check for missing add-ons
		for addonName := range allAddons {
			missingClusters := []string{}
			versionDiffs := make(map[string][]string)

			for _, cluster := range clusters {
				if version, exists := addonsByCluster[cluster.Name][addonName]; exists {
					if _, versionExists := versionDiffs[version]; !versionExists {
						versionDiffs[version] = []string{}
					}
					versionDiffs[version] = append(versionDiffs[version], cluster.Name)
				} else {
					missingClusters = append(missingClusters, cluster.Name)
				}
			}

			if len(missingClusters) > 0 {
				var values []ValuePair
				for _, cluster := range clusters {
					version := "missing"
					if v, exists := addonsByCluster[cluster.Name][addonName]; exists {
						version = v
					}
					values = append(values, ValuePair{
						ClusterName: cluster.Name,
						Value:       version,
					})
				}

				differences = append(differences, Difference{
					Field:       fmt.Sprintf("addons.%s", addonName),
					Description: fmt.Sprintf("Add-on %s is missing from some clusters: %s", addonName, strings.Join(missingClusters, ", ")),
					Values:      values,
					Severity:    "warning",
				})
			} else if len(versionDiffs) > 1 {
				var values []ValuePair
				for _, cluster := range clusters {
					values = append(values, ValuePair{
						ClusterName: cluster.Name,
						Value:       addonsByCluster[cluster.Name][addonName],
					})
				}

				differences = append(differences, Difference{
					Field:       fmt.Sprintf("addons.%s.version", addonName),
					Description: fmt.Sprintf("Add-on %s has different versions across clusters", addonName),
					Values:      values,
					Severity:    "info",
				})
			}
		}
	}

	return differences
}

// shouldIncludeField checks if a field should be included in comparison
func (s *ServiceImpl) shouldIncludeField(field string, include []string) bool {
	if len(include) == 0 {
		return true // Include all fields if none specified
	}

	for _, included := range include {
		if included == field {
			return true
		}
	}

	return false
}
