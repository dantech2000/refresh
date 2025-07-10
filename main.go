package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

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
	AmiStatus    AMIStatus
}

type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
}

type UpdateProgress struct {
	NodegroupName string
	UpdateID      string
	ClusterName   string
	Status        types.UpdateStatus
	StartTime     time.Time
	LastChecked   time.Time
	ErrorMessage  string
}

type ProgressMonitor struct {
	Updates     []UpdateProgress
	StartTime   time.Time
	Quiet       bool
	NoWait      bool
	Timeout     time.Duration
	LastPrinted int // Track lines printed in last update
}

type MonitorConfig struct {
	PollInterval    time.Duration
	MaxRetries      int
	BackoffMultiple float64
	Quiet           bool
	NoWait          bool
	Timeout         time.Duration
}

// AMIStatus represents the status of a nodegroup's AMI
type AMIStatus int

const (
	AMILatest AMIStatus = iota
	AMIOutdated
	AMIUpdating
	AMIUnknown
)

func (s AMIStatus) String() string {
	switch s {
	case AMILatest:
		return color.GreenString("Latest")
	case AMIOutdated:
		return color.RedString("Outdated")
	case AMIUpdating:
		return color.YellowString("Updating")
	case AMIUnknown:
		return color.WhiteString("Unknown")
	default:
		return color.WhiteString("Unknown")
	}
}

// DryRunAction represents what action would be taken in dry run mode
type DryRunAction int

const (
	ActionUpdate DryRunAction = iota
	ActionSkipUpdating
	ActionSkipLatest
	ActionForceUpdate
)

func (a DryRunAction) String() string {
	switch a {
	case ActionUpdate:
		return color.GreenString("UPDATE")
	case ActionSkipUpdating:
		return color.YellowString("SKIP")
	case ActionSkipLatest:
		return color.GreenString("SKIP")
	case ActionForceUpdate:
		return color.CyanString("FORCE UPDATE")
	default:
		return color.WhiteString("UNKNOWN")
	}
}

var versionInfo = VersionInfo{
	Version:   "v0.1.6",
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
					&cli.BoolFlag{
						Name:  "dry-run",
						Usage: "Preview changes without executing them",
					},
					&cli.BoolFlag{
						Name:  "no-wait",
						Usage: "Don't wait for update completion (original behavior)",
					},
					&cli.BoolFlag{
						Name:  "quiet",
						Usage: "Minimal output mode",
					},
					&cli.DurationFlag{
						Name:  "timeout",
						Usage: "Maximum time to wait for update completion",
						Value: 40 * time.Minute,
					},
					&cli.DurationFlag{
						Name:  "poll-interval",
						Usage: "Polling interval for checking update status",
						Value: 15 * time.Second,
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

					// Parse command flags
					nodegroupPattern := c.String("nodegroup")
					force := c.Bool("force")
					dryRun := c.Bool("dry-run")
					noWait := c.Bool("no-wait")
					quiet := c.Bool("quiet")
					timeout := c.Duration("timeout")
					pollInterval := c.Duration("poll-interval")

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

					// Create monitor configuration
					config := MonitorConfig{
						PollInterval:    pollInterval,
						MaxRetries:      3,
						BackoffMultiple: 2.0,
						Quiet:           quiet,
						NoWait:          noWait,
						Timeout:         timeout,
					}

					// Initialize progress monitor
					monitor := &ProgressMonitor{
						Updates:   make([]UpdateProgress, 0),
						StartTime: time.Now(),
						Quiet:     quiet,
						NoWait:    noWait,
						Timeout:   timeout,
					}

					// If dry run mode, show what would be updated with details
					if dryRun {
						return performDryRun(ctx, eksClient, clusterName, selectedNodegroups, force, quiet)
					}

					// Start updates and collect update IDs
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

						if !quiet {
							color.Cyan("Starting update for nodegroup %s...", ng)
						}

						updateResp, err := eksClient.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
							ClusterName:   aws.String(clusterName),
							NodegroupName: aws.String(ng),
							Force:         force,
						})
						if err != nil {
							color.Red("Failed to update nodegroup %s: %v", ng, err)
							continue
						}

						// Add to monitoring list
						updateProgress := UpdateProgress{
							NodegroupName: ng,
							UpdateID:      *updateResp.Update.Id,
							ClusterName:   clusterName,
							Status:        updateResp.Update.Status,
							StartTime:     time.Now(),
							LastChecked:   time.Now(),
						}
						monitor.Updates = append(monitor.Updates, updateProgress)

						if !quiet {
							color.Green("Update started for nodegroup %s (ID: %s)", ng, *updateResp.Update.Id)
						}
					}

					// If no updates were started, return
					if len(monitor.Updates) == 0 {
						color.Yellow("No nodegroup updates were started")
						return nil
					}

					// If no-wait flag is set, return after starting updates
					if noWait {
						if !quiet {
							fmt.Printf("Started %d nodegroup update(s). Use 'refresh list --cluster %s' to check status.\n",
								len(monitor.Updates), clusterName)
						}
						return nil
					}

					// Monitor progress
					return monitorUpdates(ctx, eksClient, monitor, config)
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

		// Determine AMI status using enum
		var amiStatus AMIStatus
		if ng.Status == types.NodegroupStatusUpdating {
			amiStatus = AMIUpdating
		} else if currentAmiId == "" || latestAmiId == "" {
			amiStatus = AMIUnknown
		} else if currentAmiId == latestAmiId {
			amiStatus = AMILatest
		} else {
			amiStatus = AMIOutdated
		}

		statusStr := string(ng.Status)
		if ng.Status == types.NodegroupStatusUpdating {
			statusStr = color.YellowString("UPDATING")
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
	if _, err := fmt.Scanln(&response); err != nil {
		// If there's an error reading input, treat as cancellation
		return "", fmt.Errorf("operation cancelled: failed to read input")
	}

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

// monitorUpdates monitors the progress of multiple nodegroup updates
func monitorUpdates(ctx context.Context, eksClient *eks.Client, monitor *ProgressMonitor, config MonitorConfig) error {
	// Set up signal handling for graceful cancellation
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Create a cancellable context with timeout
	monitorCtx, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()

	if !config.Quiet {
		fmt.Printf("\nMonitoring %d nodegroup update(s)...\n", len(monitor.Updates))
		fmt.Printf("Timeout: %v | Poll interval: %v\n", config.Timeout, config.PollInterval)
		fmt.Printf("Press Ctrl+C to stop monitoring (updates will continue)\n\n")
	}

	ticker := time.NewTicker(config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sigChan:
			if !config.Quiet {
				color.Yellow("\nMonitoring cancelled by user. Updates are still running in AWS.")
				fmt.Printf("Use 'refresh list --cluster %s' to check status manually.\n", monitor.Updates[0].ClusterName)
			}
			return nil

		case <-monitorCtx.Done():
			if !config.Quiet {
				color.Red("\nMonitoring timeout reached after %v", config.Timeout)
				fmt.Printf("Updates may still be running. Use 'refresh list' to check status.\n")
			}
			return fmt.Errorf("monitoring timeout reached")

		case <-ticker.C:
			allComplete, err := checkAndUpdateProgress(monitorCtx, eksClient, monitor, config)
			if err != nil {
				if !config.Quiet {
					color.Red("Error checking update progress: %v", err)
				}
				// Continue monitoring unless it's a critical error
				continue
			}

			if allComplete {
				return displayCompletionSummary(monitor, config)
			}
		}
	}
}

// checkAndUpdateProgress checks the status of all updates and updates the display
func checkAndUpdateProgress(ctx context.Context, eksClient *eks.Client, monitor *ProgressMonitor, config MonitorConfig) (bool, error) {
	allComplete := true
	now := time.Now()

	for i := range monitor.Updates {
		update := &monitor.Updates[i]

		// Skip if already completed or failed
		if update.Status == types.UpdateStatusSuccessful ||
			update.Status == types.UpdateStatusFailed ||
			update.Status == types.UpdateStatusCancelled {
			continue
		}

		// Check update status with retry logic
		updateStatus, err := checkUpdateWithRetry(ctx, eksClient, update, config)
		if err != nil {
			update.ErrorMessage = err.Error()
			continue
		}

		// Update progress
		update.Status = updateStatus.Update.Status
		update.LastChecked = now

		// Check for errors
		if len(updateStatus.Update.Errors) > 0 {
			var errorMessages []string
			for _, e := range updateStatus.Update.Errors {
				if e.ErrorMessage != nil {
					errorMessages = append(errorMessages, *e.ErrorMessage)
				}
			}
			update.ErrorMessage = strings.Join(errorMessages, "; ")
		}

		// Still in progress
		if update.Status == types.UpdateStatusInProgress {
			allComplete = false
		}
	}

	// Display current status
	if !config.Quiet {
		displayProgressUpdate(monitor)
	}

	return allComplete, nil
}

// checkUpdateWithRetry checks update status with exponential backoff retry
func checkUpdateWithRetry(ctx context.Context, eksClient *eks.Client, update *UpdateProgress, config MonitorConfig) (*eks.DescribeUpdateOutput, error) {
	var lastErr error
	backoff := time.Second

	for attempt := 0; attempt < config.MaxRetries; attempt++ {
		updateStatus, err := eksClient.DescribeUpdate(ctx, &eks.DescribeUpdateInput{
			Name:          aws.String(update.ClusterName),
			NodegroupName: aws.String(update.NodegroupName),
			UpdateId:      aws.String(update.UpdateID),
		})

		if err == nil {
			return updateStatus, nil
		}

		lastErr = err

		// Don't retry on context cancellation or timeout
		if ctx.Err() != nil {
			break
		}

		// Exponential backoff
		if attempt < config.MaxRetries-1 {
			time.Sleep(backoff)
			backoff = time.Duration(float64(backoff) * config.BackoffMultiple)
		}
	}

	return nil, lastErr
}

// displayProgressUpdate shows current progress in a live updating format with tree structure
func displayProgressUpdate(monitor *ProgressMonitor) {
	// Clear previous output if we have printed before
	if monitor.LastPrinted > 0 {
		fmt.Printf("\033[%dA", monitor.LastPrinted)
		fmt.Print("\033[J")
	}

	// Count lines as we print them
	lineCount := 0

	elapsed := time.Since(monitor.StartTime)
	fmt.Printf("Elapsed: %v\n", elapsed.Round(time.Second))
	lineCount++

	// Print cluster name as root (get from first update)
	if len(monitor.Updates) > 0 {
		fmt.Printf("%s\n", color.CyanString(monitor.Updates[0].ClusterName))
		lineCount++

		additionalLines := printUpdateProgressTree(monitor.Updates)
		lineCount += additionalLines
	}

	fmt.Println() // Extra line for readability
	lineCount++

	// Store the number of lines we printed for next iteration
	monitor.LastPrinted = lineCount
}

// printUpdateProgressTree displays update progress in tree format similar to list command
func printUpdateProgressTree(updates []UpdateProgress) int {
	lineCount := 0

	for i, update := range updates {
		isLast := i == len(updates)-1
		var prefix, itemPrefix string

		if isLast {
			prefix = "└── "
			itemPrefix = "    "
		} else {
			prefix = "├── "
			itemPrefix = "│   "
		}

		// Print nodegroup name with status
		statusPrefix := getStatusPrefix(update.Status)
		fmt.Printf("%s%s %s\n", prefix, statusPrefix, color.YellowString(update.NodegroupName))
		lineCount++

		// Print update details
		duration := time.Since(update.StartTime).Round(time.Second)
		statusColor := getStatusColor(update.Status)

		statusText := statusColor(string(update.Status))
		if update.ErrorMessage != "" {
			statusText = color.RedString("FAILED: %s", update.ErrorMessage)
		}

		fmt.Printf("%s├── Status: %s\n", itemPrefix, statusText)
		fmt.Printf("%s├── Duration: %s\n", itemPrefix, color.BlueString(duration.String()))
		fmt.Printf("%s├── Update ID: %s\n", itemPrefix, color.WhiteString(update.UpdateID))
		fmt.Printf("%s└── Last Checked: %s\n", itemPrefix, color.GreenString(update.LastChecked.Format("15:04:05")))
		lineCount += 4

		// Add spacing between nodegroups except for the last one
		if !isLast {
			fmt.Println()
			lineCount++
		}
	}

	return lineCount
}

// displayCompletionSummary shows the final summary when all updates are complete in tree format
func displayCompletionSummary(monitor *ProgressMonitor, config MonitorConfig) error {
	if !config.Quiet {
		totalDuration := time.Since(monitor.StartTime)

		fmt.Printf("\nAll updates completed in %v\n\n", totalDuration.Round(time.Second))

		// Print cluster name as root
		if len(monitor.Updates) > 0 {
			fmt.Printf("%s\n", color.CyanString(monitor.Updates[0].ClusterName))
			printCompletionSummaryTree(monitor.Updates)
		}

		// Print results summary
		successful := 0
		failed := 0
		for _, update := range monitor.Updates {
			switch update.Status {
			case types.UpdateStatusSuccessful:
				successful++
			case types.UpdateStatusFailed, types.UpdateStatusCancelled:
				failed++
			}
		}

		fmt.Printf("\nResults: %s successful, %s failed\n",
			color.GreenString("%d", successful),
			color.RedString("%d", failed))
	}

	// Return error if any updates failed
	for _, update := range monitor.Updates {
		if update.Status == types.UpdateStatusFailed {
			return fmt.Errorf("one or more nodegroup updates failed")
		}
	}

	return nil
}

// printCompletionSummaryTree displays completion summary in tree format
func printCompletionSummaryTree(updates []UpdateProgress) {
	for i, update := range updates {
		isLast := i == len(updates)-1
		var prefix, itemPrefix string

		if isLast {
			prefix = "└── "
			itemPrefix = "    "
		} else {
			prefix = "├── "
			itemPrefix = "│   "
		}

		// Print nodegroup name with status
		statusPrefix := getStatusPrefix(update.Status)
		fmt.Printf("%s%s %s\n", prefix, statusPrefix, color.YellowString(update.NodegroupName))

		// Print completion details
		duration := time.Since(update.StartTime).Round(time.Second)

		var statusText string
		switch update.Status {
		case types.UpdateStatusSuccessful:
			statusText = color.GreenString("SUCCESSFUL")
		case types.UpdateStatusFailed:
			statusText = color.RedString("FAILED")
			if update.ErrorMessage != "" {
				statusText = color.RedString("FAILED: %s", update.ErrorMessage)
			}
		case types.UpdateStatusCancelled:
			statusText = color.YellowString("CANCELLED")
		default:
			statusText = color.WhiteString(string(update.Status))
		}

		fmt.Printf("%s├── Status: %s\n", itemPrefix, statusText)
		fmt.Printf("%s├── Duration: %s\n", itemPrefix, color.BlueString(duration.String()))
		fmt.Printf("%s└── Update ID: %s\n", itemPrefix, color.WhiteString(update.UpdateID))

		// Add spacing between nodegroups except for the last one
		if !isLast {
			fmt.Println()
		}
	}
}

// getStatusPrefix returns an appropriate text prefix for the update status
func getStatusPrefix(status types.UpdateStatus) string {
	switch status {
	case types.UpdateStatusInProgress:
		return "[IN PROGRESS]"
	case types.UpdateStatusSuccessful:
		return "[SUCCESSFUL]"
	case types.UpdateStatusFailed:
		return "[FAILED]"
	case types.UpdateStatusCancelled:
		return "[CANCELLED]"
	default:
		return "[UNKNOWN]"
	}
}

// getStatusColor returns a color function for the update status
func getStatusColor(status types.UpdateStatus) func(format string, a ...interface{}) string {
	switch status {
	case types.UpdateStatusInProgress:
		return color.CyanString
	case types.UpdateStatusSuccessful:
		return color.GreenString
	case types.UpdateStatusFailed:
		return color.RedString
	case types.UpdateStatusCancelled:
		return color.YellowString
	default:
		return color.WhiteString
	}
}

// performDryRun shows what would be updated without making changes
func performDryRun(ctx context.Context, eksClient *eks.Client, clusterName string, selectedNodegroups []string, force bool, quiet bool) error {
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
				fmt.Printf("%s: Nodegroup %s is already UPDATING\n", ActionSkipUpdating, ng)
			}
			continue
		}

		// Check if update is actually needed (unless force is enabled)
		if !force {
			currentAmiId := currentAmiID(ctx, ngDesc.Nodegroup, ec2Client, autoscalingClient)
			latestAmiId := latestAmiID(ctx, ssmClient, k8sVersion)

			if currentAmiId != "" && latestAmiId != "" && currentAmiId == latestAmiId {
				alreadyLatest = append(alreadyLatest, ng)
				if !quiet {
					fmt.Printf("%s: Nodegroup %s is already on latest AMI\n", ActionSkipLatest, ng)
				}
				continue
			}
		}

		updatesNeeded = append(updatesNeeded, ng)
		if !quiet {
			if force {
				fmt.Printf("%s: Nodegroup %s would be force updated\n", ActionForceUpdate, ng)
			} else {
				fmt.Printf("%s: Nodegroup %s would be updated (AMI outdated)\n", ActionUpdate, ng)
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
