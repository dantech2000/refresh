package dryrun

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/fatih/color"

	awsClient "github.com/dantech2000/refresh/internal/aws"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

// PerformDryRun shows what would be updated without making changes
func PerformDryRun(ctx context.Context, eksClient *eks.Client, clusterName string, selectedNodegroups []string, force bool, quiet bool) error {
	// Get AWS clients for AMI checking
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %v", err)
	}
	ec2Client := ec2.NewFromConfig(awsCfg)
	autoscalingClient := autoscaling.NewFromConfig(awsCfg)
	ssmClient := ssm.NewFromConfig(awsCfg)

	// Get cluster info for Kubernetes version
	clusterOut, err := eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: aws.String(clusterName),
	})
	if err != nil {
		return fmt.Errorf("failed to describe cluster: %v", err)
	}
	k8sVersion := *clusterOut.Cluster.Version

	if !quiet {
		color.Cyan("DRY RUN: Preview of nodegroup updates for cluster %s\n", clusterName)
		if force {
			color.Yellow("Force update would be enabled")
		}
		fmt.Println()
	}

	var updatesNeeded []string
	var updatesSkipped []string
	var alreadyLatest []string

	for _, ng := range selectedNodegroups {
		// Check nodegroup status before updating (read-only operation)
		ngDesc, err := eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(ng),
		})
		if err != nil {
			color.Red("Failed to describe nodegroup %s: %v", ng, err)
			continue
		}

		if ngDesc.Nodegroup.Status == types.NodegroupStatusUpdating {
			updatesSkipped = append(updatesSkipped, ng)
			if !quiet {
				fmt.Printf("%s: Nodegroup %s is already UPDATING\n", refreshTypes.ActionSkipUpdating, ng)
			}
			continue
		}

		// Check if update is actually needed (unless force is enabled)
		if !force {
			currentAmiId := awsClient.CurrentAmiID(ctx, ngDesc.Nodegroup, ec2Client, autoscalingClient)
			latestAmiId := awsClient.LatestAmiID(ctx, ssmClient, k8sVersion)

			if currentAmiId != "" && latestAmiId != "" && currentAmiId == latestAmiId {
				alreadyLatest = append(alreadyLatest, ng)
				if !quiet {
					fmt.Printf("%s: Nodegroup %s is already on latest AMI\n", refreshTypes.ActionSkipLatest, ng)
				}
				continue
			}
		}

		updatesNeeded = append(updatesNeeded, ng)
		if !quiet {
			if force {
				fmt.Printf("%s: Nodegroup %s would be force updated\n", refreshTypes.ActionForceUpdate, ng)
			} else {
				fmt.Printf("%s: Nodegroup %s would be updated (AMI outdated)\n", refreshTypes.ActionUpdate, ng)
			}
		}
	}

	if !quiet {
		fmt.Println()
		color.Cyan("Summary:")
		fmt.Printf("- Nodegroups that would be updated: %d\n", len(updatesNeeded))
		fmt.Printf("- Nodegroups that would be skipped (already updating): %d\n", len(updatesSkipped))
		fmt.Printf("- Nodegroups already on latest AMI: %d\n", len(alreadyLatest))

		if len(updatesNeeded) > 0 {
			fmt.Println("\nWould update:")
			for _, ng := range updatesNeeded {
				fmt.Printf("  - %s\n", ng)
			}
		}

		if len(updatesSkipped) > 0 {
			fmt.Println("\nWould skip (already updating):")
			for _, ng := range updatesSkipped {
				fmt.Printf("  - %s\n", ng)
			}
		}

		if len(alreadyLatest) > 0 {
			fmt.Println("\nAlready on latest AMI:")
			for _, ng := range alreadyLatest {
				fmt.Printf("  - %s\n", ng)
			}
		}

		fmt.Println("\nTo execute these updates, run the same command without --dry-run")
	}

	return nil
}
