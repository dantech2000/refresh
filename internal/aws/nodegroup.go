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

// NodegroupClient handles nodegroup operations.
type NodegroupClient struct {
	eksClient *eks.Client
	ec2Client *ec2.Client
	asgClient *autoscaling.Client
	ssmClient *ssm.Client
}

// NewNodegroupClient creates a new nodegroup client.
func NewNodegroupClient(awsCfg aws.Config) *NodegroupClient {
	return &NodegroupClient{
		eksClient: eks.NewFromConfig(awsCfg),
		ec2Client: ec2.NewFromConfig(awsCfg),
		asgClient: autoscaling.NewFromConfig(awsCfg),
		ssmClient: ssm.NewFromConfig(awsCfg),
	}
}

// Nodegroups retrieves all nodegroups for a cluster with their AMI status.
func Nodegroups(ctx context.Context, awsCfg aws.Config, clusterName string) ([]refreshTypes.NodegroupInfo, error) {
	eksClient := eks.NewFromConfig(awsCfg)
	ec2Client := ec2.NewFromConfig(awsCfg)
	autoscalingClient := autoscaling.NewFromConfig(awsCfg)
	ssmClient := ssm.NewFromConfig(awsCfg)

	// Get cluster's Kubernetes version
	k8sVersion, err := getClusterVersion(ctx, eksClient, clusterName)
	if err != nil {
		return nil, err
	}

	// List nodegroups with pagination
	nodegroupNames, err := listNodegroupNames(ctx, eksClient, clusterName)
	if err != nil {
		return nil, err
	}

	// Get details for each nodegroup
	nodegroups := make([]refreshTypes.NodegroupInfo, 0, len(nodegroupNames))
	for _, ngName := range nodegroupNames {
		info, err := getNodegroupInfo(ctx, eksClient, ec2Client, autoscalingClient, ssmClient, clusterName, ngName, k8sVersion)
		if err != nil {
			color.Red("Failed to describe nodegroup %s: %v", ngName, err)
			continue
		}
		nodegroups = append(nodegroups, info)
	}

	return nodegroups, nil
}

// getClusterVersion retrieves the Kubernetes version for a cluster.
func getClusterVersion(ctx context.Context, eksClient *eks.Client, clusterName string) (string, error) {
	clusterOut, err := eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: aws.String(clusterName),
	})
	if err != nil {
		return "", FormatAWSError(err, "describing cluster")
	}
	return *clusterOut.Cluster.Version, nil
}

// listNodegroupNames retrieves all nodegroup names for a cluster using pagination.
func listNodegroupNames(ctx context.Context, eksClient *eks.Client, clusterName string) ([]string, error) {
	var nodegroupNames []string
	var nextToken *string

	for {
		ngOut, err := eksClient.ListNodegroups(ctx, &eks.ListNodegroupsInput{
			ClusterName: aws.String(clusterName),
			NextToken:   nextToken,
		})
		if err != nil {
			return nil, FormatAWSError(err, "listing nodegroups")
		}

		nodegroupNames = append(nodegroupNames, ngOut.Nodegroups...)

		if ngOut.NextToken == nil {
			break
		}
		nextToken = ngOut.NextToken
	}

	return nodegroupNames, nil
}

// getNodegroupInfo retrieves detailed information about a single nodegroup.
func getNodegroupInfo(
	ctx context.Context,
	eksClient *eks.Client,
	ec2Client *ec2.Client,
	autoscalingClient *autoscaling.Client,
	ssmClient *ssm.Client,
	clusterName, ngName, k8sVersion string,
) (refreshTypes.NodegroupInfo, error) {
	ngDesc, err := eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(ngName),
	})
	if err != nil {
		return refreshTypes.NodegroupInfo{}, err
	}

	ng := ngDesc.Nodegroup
	info := refreshTypes.NodegroupInfo{
		Name:         aws.ToString(ng.NodegroupName),
		Status:       formatNodegroupStatus(ng.Status),
		InstanceType: getFirstInstanceType(ng.InstanceTypes),
		Desired:      getDesiredSize(ng.ScalingConfig),
		CurrentAmi:   CurrentAmiID(ctx, ng, ec2Client, autoscalingClient),
		AmiStatus:    determineAMIStatus(ctx, ng, ec2Client, autoscalingClient, ssmClient, k8sVersion),
	}

	if info.CurrentAmi == "" {
		color.Yellow("[WARN] Could not determine AMI ID for nodegroup %s", info.Name)
	}

	return info, nil
}

// formatNodegroupStatus returns a formatted status string.
func formatNodegroupStatus(status types.NodegroupStatus) string {
	if status == types.NodegroupStatusUpdating {
		return color.YellowString("UPDATING")
	}
	return string(status)
}

// getFirstInstanceType returns the first instance type or a default.
func getFirstInstanceType(instanceTypes []string) string {
	if len(instanceTypes) > 0 {
		return instanceTypes[0]
	}
	return "-"
}

// getDesiredSize returns the desired size from scaling config.
func getDesiredSize(scalingConfig *types.NodegroupScalingConfig) int32 {
	if scalingConfig != nil && scalingConfig.DesiredSize != nil {
		return *scalingConfig.DesiredSize
	}
	return 0
}

// determineAMIStatus determines the AMI status for a nodegroup.
func determineAMIStatus(
	ctx context.Context,
	ng *types.Nodegroup,
	ec2Client *ec2.Client,
	autoscalingClient *autoscaling.Client,
	ssmClient *ssm.Client,
	k8sVersion string,
) refreshTypes.AMIStatus {
	if ng.Status == types.NodegroupStatusUpdating {
		return refreshTypes.AMIUpdating
	}

	currentAmiId := CurrentAmiID(ctx, ng, ec2Client, autoscalingClient)
	latestAmiId := LatestAmiID(ctx, ssmClient, k8sVersion)

	if currentAmiId == "" || latestAmiId == "" {
		return refreshTypes.AMIUnknown
	}

	if currentAmiId == latestAmiId {
		return refreshTypes.AMILatest
	}

	return refreshTypes.AMIOutdated
}

// MatchingNodegroups returns nodegroup names that contain the given pattern.
// If pattern is empty, returns all nodegroups.
func MatchingNodegroups(nodegroups []string, pattern string) []string {
	if pattern == "" {
		return nodegroups
	}

	matches := make([]string, 0, len(nodegroups))
	for _, ng := range nodegroups {
		if strings.Contains(ng, pattern) {
			matches = append(matches, ng)
		}
	}

	return matches
}

// ConfirmNodegroupSelection prompts user to confirm when multiple nodegroups match.
// Returns the selected nodegroups or error if user cancels.
func ConfirmNodegroupSelection(matches []string, pattern string) ([]string, error) {
	switch {
	case len(matches) == 0:
		return nil, fmt.Errorf("no nodegroups found matching pattern: %s", pattern)
	case len(matches) == 1:
		return matches, nil
	case pattern == "":
		// No pattern specified - user wants to update all
		return matches, nil
	default:
		return promptForNodegroupConfirmation(matches, pattern)
	}
}

// promptForNodegroupConfirmation displays matching nodegroups and prompts for confirmation.
func promptForNodegroupConfirmation(matches []string, pattern string) ([]string, error) {
	color.Yellow("Multiple nodegroups match pattern '%s':", pattern)
	for i, ng := range matches {
		fmt.Printf("  %d) %s\n", i+1, ng)
	}

	color.Cyan("Update all %d matching nodegroups? (y/N): ", len(matches))

	var response string
	if _, err := fmt.Scanln(&response); err != nil {
		return nil, fmt.Errorf("operation cancelled: failed to read input")
	}

	response = strings.ToLower(strings.TrimSpace(response))
	if response == "y" || response == "yes" {
		return matches, nil
	}

	return nil, fmt.Errorf("operation cancelled by user")
}
