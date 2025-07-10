package aws

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/fatih/color"

	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

func Nodegroups(ctx context.Context, awsCfg aws.Config, clusterName string) ([]refreshTypes.NodegroupInfo, error) {
	eksClient := eks.NewFromConfig(awsCfg)
	ec2Client := ec2.NewFromConfig(awsCfg)
	autoscalingClient := autoscaling.NewFromConfig(awsCfg)
	ssmClient := ssm.NewFromConfig(awsCfg)

	clusterOut, err := eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: aws.String(clusterName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe cluster: %v", err)
	}
	k8sVersion := *clusterOut.Cluster.Version

	ngOut, err := eksClient.ListNodegroups(ctx, &eks.ListNodegroupsInput{
		ClusterName: aws.String(clusterName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list nodegroups: %v", err)
	}

	var nodegroups []refreshTypes.NodegroupInfo
	for _, ngName := range ngOut.Nodegroups {
		ngDesc, err := eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(ngName),
		})
		if err != nil {
			color.Red("Failed to describe nodegroup %s: %v", ngName, err)
			continue
		}
		ng := ngDesc.Nodegroup
		instanceType := "-"
		if len(ng.InstanceTypes) > 0 {
			instanceType = ng.InstanceTypes[0]
		}
		desired := int32(0)
		if ng.ScalingConfig != nil && ng.ScalingConfig.DesiredSize != nil {
			desired = *ng.ScalingConfig.DesiredSize
		}
		currentAmiId := CurrentAmiID(ctx, ng, ec2Client, autoscalingClient)
		if currentAmiId == "" {
			color.Yellow("[WARN] Could not determine AMI ID for nodegroup %s", *ng.NodegroupName)
		}
		latestAmiId := LatestAmiID(ctx, ssmClient, k8sVersion)

		// Determine AMI status using enum
		var amiStatus refreshTypes.AMIStatus
		if ng.Status == types.NodegroupStatusUpdating {
			amiStatus = refreshTypes.AMIUpdating
		} else if currentAmiId == "" || latestAmiId == "" {
			amiStatus = refreshTypes.AMIUnknown
		} else if currentAmiId == latestAmiId {
			amiStatus = refreshTypes.AMILatest
		} else {
			amiStatus = refreshTypes.AMIOutdated
		}

		statusStr := string(ng.Status)
		if ng.Status == types.NodegroupStatusUpdating {
			statusStr = color.YellowString("UPDATING")
		}
		info := refreshTypes.NodegroupInfo{
			Name:         *ng.NodegroupName,
			Status:       statusStr,
			InstanceType: instanceType,
			Desired:      desired,
			CurrentAmi:   currentAmiId,
			AmiStatus:    amiStatus,
		}
		nodegroups = append(nodegroups, info)
	}
	return nodegroups, nil
}

// matchingNodegroups returns nodegroup names that contain the given pattern.
// If pattern is empty, returns all nodegroups.
func MatchingNodegroups(nodegroups []string, pattern string) []string {
	if pattern == "" {
		return nodegroups
	}

	var matches []string
	for _, ng := range nodegroups {
		if strings.Contains(ng, pattern) {
			matches = append(matches, ng)
		}
	}
	return matches
}

// confirmNodegroupSelection prompts user to confirm when multiple nodegroups match.
// Returns the selected nodegroups or error if user cancels.
func ConfirmNodegroupSelection(matches []string, pattern string) ([]string, error) {
	if len(matches) == 1 {
		return matches, nil
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("no nodegroups found matching pattern: %s", pattern)
	}

	// If no pattern specified (empty string), user wants to update all - no confirmation needed
	if pattern == "" {
		return matches, nil
	}

	// Multiple matches with a specific pattern - show them and ask for confirmation
	color.Yellow("Multiple nodegroups match pattern '%s':", pattern)
	for i, ng := range matches {
		fmt.Printf("  %d) %s\n", i+1, ng)
	}

	color.Cyan("Update all %d matching nodegroups? (y/N): ", len(matches))
	var response string
	if _, err := fmt.Scanln(&response); err != nil {
		// If there's an error reading input, treat as cancellation
		return nil, fmt.Errorf("operation cancelled: failed to read input")
	}

	response = strings.ToLower(strings.TrimSpace(response))
	if response == "y" || response == "yes" {
		return matches, nil
	}

	return nil, fmt.Errorf("operation cancelled by user")
}
