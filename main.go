package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/fatih/color"
	"github.com/urfave/cli/v2"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"k8s.io/client-go/tools/clientcmd"
)

type NodegroupInfo struct {
	Name         string
	Status       string
	InstanceType string
	Desired      int32
	CurrentAmi   string
	AmiStatus    string
}

type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
}

var versionInfo = VersionInfo{
	Version:   "v0.1.3",
	Commit:    "",
	BuildDate: "",
}

func main() {
	app := &cli.App{
		Name:  "refresh",
		Usage: "Manage and monitor AWS EKS node groups",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List all managed node groups",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:    "cluster",
						Usage:   "EKS cluster name or partial name pattern (overrides kubeconfig)",
						EnvVars: []string{"EKS_CLUSTER_NAME"},
					},
					&cli.StringFlag{
						Name:  "nodegroup",
						Usage: "Nodegroup name or partial name pattern to filter results",
					},
				},
				Action: func(c *cli.Context) error {
					ctx := context.Background()
					awsCfg, err := config.LoadDefaultConfig(ctx)
					if err != nil {
						color.Red("Failed to load AWS config: %v", err)
						return err
					}
					clusterName, err := clusterName(ctx, awsCfg, c.String("cluster"))
					if err != nil {
						color.Red("%v", err)
						return err
					}
					allNodegroups, err := nodegroups(ctx, awsCfg, clusterName)
					if err != nil {
						errStr := fmt.Sprintf("%v", err)
						// Friendly error handling for common AWS credential and cluster errors
						if strings.Contains(errStr, "no EC2 IMDS role found") ||
							strings.Contains(errStr, "failed to refresh cached credentials") ||
							strings.Contains(errStr, "NoCredentialProviders") ||
							strings.Contains(errStr, "could not find") ||
							strings.Contains(errStr, "request canceled") ||
							strings.Contains(errStr, "context deadline exceeded") {
							color.Red("Could not authenticate to AWS or access the cluster. Please ensure your AWS credentials are set up correctly and that your network allows access to AWS EKS. See https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-files.html for help.")
							return fmt.Errorf("authentication or cluster access error: %v", err)
						}
						color.Red("%v", err)
						return err
					}

					// Filter nodegroups if pattern provided
					nodegroupPattern := c.String("nodegroup")
					if nodegroupPattern != "" {
						// Extract nodegroup names for matching
						var allNames []string
						for _, ng := range allNodegroups {
							allNames = append(allNames, ng.Name)
						}

						// Find matching names
						matchingNames := matchingNodegroups(allNames, nodegroupPattern)

						// Filter the NodegroupInfo slice to only include matches
						var filteredNodegroups []NodegroupInfo
						for _, ng := range allNodegroups {
							for _, matchingName := range matchingNames {
								if ng.Name == matchingName {
									filteredNodegroups = append(filteredNodegroups, ng)
									break
								}
							}
						}

						if len(filteredNodegroups) == 0 {
							color.Yellow("No nodegroups found matching pattern: %s", nodegroupPattern)
							return nil
						}

						printNodegroupsTree(clusterName, filteredNodegroups)
					} else {
						printNodegroupsTree(clusterName, allNodegroups)
					}
					return nil
				},
			},
			{
				Name:  "version",
				Usage: "Print the version of this CLI",
				Action: func(c *cli.Context) error {
					fmt.Printf("refresh version: %s", versionInfo.Version)
					if versionInfo.Commit != "" {
						fmt.Printf(" (commit: %s)", versionInfo.Commit)
					}
					if versionInfo.BuildDate != "" {
						fmt.Printf(" (built: %s)", versionInfo.BuildDate)
					}
					fmt.Println()
					return nil
				},
			},
			{
				Name:  "update-ami",
				Usage: "Update the AMI for all or a specific node group (rolling by default)",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:    "cluster",
						Usage:   "EKS cluster name or partial name pattern (overrides kubeconfig)",
						EnvVars: []string{"EKS_CLUSTER_NAME"},
					},
					&cli.StringFlag{
						Name:  "nodegroup",
						Usage: "Nodegroup name or partial name pattern (if not set, update all)",
					},
					&cli.BoolFlag{
						Name:  "force",
						Usage: "Force update if possible",
					},
				},
				Action: func(c *cli.Context) error {
					ctx := context.Background()
					awsCfg, err := config.LoadDefaultConfig(ctx)
					if err != nil {
						color.Red("Failed to load AWS config: %v", err)
						return err
					}
					clusterName, err := clusterName(ctx, awsCfg, c.String("cluster"))
					if err != nil {
						color.Red("%v", err)
						return err
					}
					eksClient := eks.NewFromConfig(awsCfg)

					nodegroupPattern := c.String("nodegroup")
					force := c.Bool("force")

					// Get all nodegroups first
					ngOut, err := eksClient.ListNodegroups(ctx, &eks.ListNodegroupsInput{
						ClusterName: aws.String(clusterName),
					})
					if err != nil {
						color.Red("Failed to list nodegroups: %v", err)
						return err
					}

					// Find matching nodegroups
					matches := matchingNodegroups(ngOut.Nodegroups, nodegroupPattern)
					selectedNodegroups, err := confirmNodegroupSelection(matches, nodegroupPattern)
					if err != nil {
						color.Red("%v", err)
						return err
					}

					for _, ng := range selectedNodegroups {
						// Check nodegroup status before updating
						ngDesc, err := eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
							ClusterName:   aws.String(clusterName),
							NodegroupName: aws.String(ng),
						})
						if err != nil {
							color.Red("Failed to describe nodegroup %s: %v", ng, err)
							continue
						}
						if ngDesc.Nodegroup.Status == types.NodegroupStatusUpdating {
							color.Yellow("Nodegroup %s is already UPDATING. Skipping update.", ng)
							continue
						}
						color.Cyan("Updating nodegroup %s...", ng)
						_, err = eksClient.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
							ClusterName:   aws.String(clusterName),
							NodegroupName: aws.String(ng),
							Force:         force,
						})
						if err != nil {
							color.Red("Failed to update nodegroup %s: %v", ng, err)
						} else {
							color.Green("Update started for nodegroup %s", ng)
						}
					}
					return nil
				},
			},
		},
	}
	if err := app.Run(os.Args); err != nil {
		color.Red("Error: %v", err)
		os.Exit(1)
	}
}

func clusterName(ctx context.Context, awsCfg aws.Config, cliFlag string) (string, error) {
	var pattern string

	// If CLI flag provided, use it as pattern
	if cliFlag != "" {
		pattern = cliFlag
	} else {
		// Try to get from kubeconfig
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = os.ExpandEnv("$HOME/.kube/config")
		}
		configOverrides := &clientcmd.ConfigOverrides{}
		clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
			configOverrides,
		)
		rawConfig, err := clientConfig.RawConfig()
		if err != nil {
			return "", fmt.Errorf("failed to load kubeconfig: %v", err)
		}
		currentContext := rawConfig.Contexts[rawConfig.CurrentContext]
		if currentContext == nil {
			return "", fmt.Errorf("no current context in kubeconfig")
		}
		clusterRef := currentContext.Cluster
		if clusterRef == "" {
			return "", fmt.Errorf("could not determine EKS cluster name from kubeconfig context")
		}

		// Check if it's already a valid AWS name
		awsNameRe := "^[0-9A-Za-z][A-Za-z0-9-_]*$"
		if matched := regexp.MustCompile(awsNameRe).MatchString(clusterRef); matched {
			pattern = clusterRef
		} else {
			clusterEntry := rawConfig.Clusters[clusterRef]
			if clusterEntry != nil && clusterEntry.Server != "" {
				server := clusterEntry.Server
				parts := strings.Split(server, ".")
				if len(parts) > 0 {
					maybeName := strings.TrimPrefix(parts[0], "https://")
					if regexp.MustCompile(awsNameRe).MatchString(maybeName) {
						pattern = maybeName
					}
				}
			}
		}

		if pattern == "" {
			return "", fmt.Errorf("could not determine valid EKS cluster name; please use --cluster flag")
		}
	}

	// Get available clusters
	clusters, err := availableClusters(ctx, awsCfg)
	if err != nil {
		return "", fmt.Errorf("failed to list available clusters: %v", err)
	}

	if len(clusters) == 0 {
		return "", fmt.Errorf("no EKS clusters found in current region")
	}

	// Find matching clusters
	matches := matchingClusters(clusters, pattern)

	// If exact match exists, prefer it
	for _, match := range matches {
		if match == pattern {
			return match, nil
		}
	}

	// Handle matches
	selectedCluster, err := confirmClusterSelection(matches, pattern)
	if err != nil {
		// If no matches found, show available clusters for reference
		if len(matches) == 0 {
			color.Yellow("Available clusters:")
			for _, cluster := range clusters {
				fmt.Printf("  - %s\n", cluster)
			}
		}
		return "", err
	}

	// If we found a match but it's different from the pattern, inform the user
	if selectedCluster != pattern {
		color.Green("Using cluster: %s", selectedCluster)
	}

	return selectedCluster, nil
}

func nodegroups(ctx context.Context, awsCfg aws.Config, clusterName string) ([]NodegroupInfo, error) {
	eksClient := eks.NewFromConfig(awsCfg)
	ec2Client := ec2.NewFromConfig(awsCfg)
	autoscalingClient := autoscaling.NewFromConfig(awsCfg)
	ssmClient := ssm.NewFromConfig(awsCfg)
	green := color.New(color.FgGreen).SprintFunc()
	red := color.New(color.FgRed).SprintFunc()

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

	var nodegroups []NodegroupInfo
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
		currentAmiId := currentAmiID(ctx, ng, ec2Client, autoscalingClient)
		if currentAmiId == "" {
			color.Yellow("[WARN] Could not determine AMI ID for nodegroup %s", *ng.NodegroupName)
		}
		latestAmiId := latestAmiID(ctx, ssmClient, k8sVersion)
		amiStatus := red("❌ Outdated")
		if currentAmiId != "" && latestAmiId != "" && currentAmiId == latestAmiId {
			amiStatus = green("✅ Latest")
		}

		statusStr := string(ng.Status)
		if ng.Status == types.NodegroupStatusUpdating {
			statusStr = color.YellowString("UPDATING")
			amiStatus = color.YellowString("⚠️ Updating")
		}
		info := NodegroupInfo{
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

func currentAmiID(ctx context.Context, ng *types.Nodegroup, ec2Client *ec2.Client, autoscalingClient *autoscaling.Client) string {
	// 1. Try launch template
	if ng.LaunchTemplate != nil && ng.LaunchTemplate.Version != nil && ng.LaunchTemplate.Id != nil {
		ltOut, err := ec2Client.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{
			LaunchTemplateId: ng.LaunchTemplate.Id,
			Versions:         []string{*ng.LaunchTemplate.Version},
		})
		if err == nil && len(ltOut.LaunchTemplateVersions) > 0 && ltOut.LaunchTemplateVersions[0].LaunchTemplateData != nil && ltOut.LaunchTemplateVersions[0].LaunchTemplateData.ImageId != nil {
			return *ltOut.LaunchTemplateVersions[0].LaunchTemplateData.ImageId
		}
	}
	// 2. Try ASG instance
	if ng.Resources != nil && len(ng.Resources.AutoScalingGroups) > 0 && ng.Resources.AutoScalingGroups[0].Name != nil {
		asgName := *ng.Resources.AutoScalingGroups[0].Name
		describeAsgOut, err := autoscalingClient.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
			AutoScalingGroupNames: []string{asgName},
		})
		if err == nil && len(describeAsgOut.AutoScalingGroups) > 0 && len(describeAsgOut.AutoScalingGroups[0].Instances) > 0 {
			instanceId := describeAsgOut.AutoScalingGroups[0].Instances[0].InstanceId
			if instanceId != nil {
				descInstOut, err := ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
					InstanceIds: []string{*instanceId},
				})
				if err == nil && len(descInstOut.Reservations) > 0 && len(descInstOut.Reservations[0].Instances) > 0 && descInstOut.Reservations[0].Instances[0].ImageId != nil {
					return *descInstOut.Reservations[0].Instances[0].ImageId
				}
			}
		}
	}
	return ""
}

func latestAmiID(ctx context.Context, ssmClient *ssm.Client, k8sVersion string) string {
	ssmParam := "/aws/service/eks/optimized-ami/" + k8sVersion + "/amazon-linux-2/recommended/image_id"
	ssmOut, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name: aws.String(ssmParam),
	})
	if err == nil && ssmOut.Parameter != nil && ssmOut.Parameter.Value != nil {
		return *ssmOut.Parameter.Value
	}
	return ""
}

func printNodegroupsTree(clusterName string, nodegroups []NodegroupInfo) {
	// Print cluster name as root
	fmt.Printf("%s\n", color.CyanString(clusterName))

	for i, ng := range nodegroups {
		isLast := i == len(nodegroups)-1
		var prefix, itemPrefix string

		if isLast {
			prefix = "└── "
			itemPrefix = "    "
		} else {
			prefix = "├── "
			itemPrefix = "│   "
		}

		// Print nodegroup name
		fmt.Printf("%s%s\n", prefix, color.YellowString(ng.Name))

		// Print nodegroup details
		fmt.Printf("%s├── Status: %s\n", itemPrefix, ng.Status)
		fmt.Printf("%s├── Instance Type: %s\n", itemPrefix, color.BlueString(ng.InstanceType))
		fmt.Printf("%s├── Desired: %s\n", itemPrefix, color.GreenString(fmt.Sprintf("%d", ng.Desired)))

		if ng.CurrentAmi != "" {
			fmt.Printf("%s├── Current AMI: %s\n", itemPrefix, color.WhiteString(ng.CurrentAmi))
		} else {
			fmt.Printf("%s├── Current AMI: %s\n", itemPrefix, color.RedString("Unknown"))
		}

		fmt.Printf("%s└── AMI Status: %s\n", itemPrefix, ng.AmiStatus)

		// Add spacing between nodegroups except for the last one
		if !isLast {
			fmt.Println()
		}
	}
}

// matchingNodegroups returns nodegroup names that contain the given pattern.
// If pattern is empty, returns all nodegroups.
func matchingNodegroups(nodegroups []string, pattern string) []string {
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
func confirmNodegroupSelection(matches []string, pattern string) ([]string, error) {
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
	fmt.Scanln(&response)

	response = strings.ToLower(strings.TrimSpace(response))
	if response == "y" || response == "yes" {
		return matches, nil
	}

	return nil, fmt.Errorf("operation cancelled by user")
}

// availableClusters returns all EKS cluster names in the current region.
func availableClusters(ctx context.Context, awsCfg aws.Config) ([]string, error) {
	eksClient := eks.NewFromConfig(awsCfg)

	out, err := eksClient.ListClusters(ctx, &eks.ListClustersInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %v", err)
	}

	return out.Clusters, nil
}

// matchingClusters returns cluster names that contain the given pattern.
// If pattern is empty, returns all clusters.
func matchingClusters(clusters []string, pattern string) []string {
	if pattern == "" {
		return clusters
	}

	var matches []string
	for _, cluster := range clusters {
		if strings.Contains(cluster, pattern) {
			matches = append(matches, cluster)
		}
	}
	return matches
}

// confirmClusterSelection prompts user to confirm when multiple clusters match.
// Returns the selected cluster or error if user cancels.
func confirmClusterSelection(matches []string, pattern string) (string, error) {
	if len(matches) == 1 {
		return matches[0], nil
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("no clusters found matching pattern: %s", pattern)
	}

	// Multiple matches - show them and ask for confirmation
	color.Yellow("Multiple clusters match pattern '%s':", pattern)
	for i, cluster := range matches {
		fmt.Printf("  %d) %s\n", i+1, cluster)
	}

	color.Cyan("Select cluster number (1-%d) or press Enter to cancel: ", len(matches))
	var response string
	fmt.Scanln(&response)

	if response == "" {
		return "", fmt.Errorf("operation cancelled by user")
	}

	// Try to parse as number
	var selected int
	if n, err := fmt.Sscanf(response, "%d", &selected); n == 1 && err == nil {
		if selected >= 1 && selected <= len(matches) {
			return matches[selected-1], nil
		}
	}

	return "", fmt.Errorf("invalid selection: %s", response)
}
