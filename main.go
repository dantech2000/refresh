package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
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
						Usage:   "EKS cluster name (overrides kubeconfig)",
						EnvVars: []string{"EKS_CLUSTER_NAME"},
					},
				},
				Action: func(c *cli.Context) error {
					ctx := context.Background()
					clusterName, err := getClusterName(c.String("cluster"))
					if err != nil {
						color.Red("%v", err)
						return err
					}
					awsCfg, err := config.LoadDefaultConfig(ctx)
					if err != nil {
						color.Red("Failed to load AWS config: %v", err)
						return err
					}
					nodegroups, err := fetchNodegroups(ctx, awsCfg, clusterName)
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
					printNodegroupsTable(nodegroups)
					return nil
				},
			},
			{
				Name:  "update-ami",
				Usage: "Update the AMI for all or a specific node group (rolling by default)",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:    "cluster",
						Usage:   "EKS cluster name (overrides kubeconfig)",
						EnvVars: []string{"EKS_CLUSTER_NAME"},
					},
					&cli.StringFlag{
						Name:  "nodegroup",
						Usage: "Nodegroup name (if not set, update all)",
					},
					&cli.BoolFlag{
						Name:  "force",
						Usage: "Force update if possible",
					},
				},
				Action: func(c *cli.Context) error {
					ctx := context.Background()
					clusterName, err := getClusterName(c.String("cluster"))
					if err != nil {
						color.Red("%v", err)
						return err
					}
					awsCfg, err := config.LoadDefaultConfig(ctx)
					if err != nil {
						color.Red("Failed to load AWS config: %v", err)
						return err
					}
					eksClient := eks.NewFromConfig(awsCfg)

					nodegroupArg := c.String("nodegroup")
					force := c.Bool("force")
					var nodegroups []string
					if nodegroupArg != "" {
						nodegroups = []string{nodegroupArg}
					} else {
						ngOut, err := eksClient.ListNodegroups(ctx, &eks.ListNodegroupsInput{
							ClusterName: aws.String(clusterName),
						})
						if err != nil {
							color.Red("Failed to list nodegroups: %v", err)
							return err
						}
						nodegroups = ngOut.Nodegroups
					}

					for _, ng := range nodegroups {
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

func getClusterName(cliFlag string) (string, error) {
	if cliFlag != "" {
		return cliFlag, nil
	}
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
	awsNameRe := "^[0-9A-Za-z][A-Za-z0-9-_]*$"
	if matched := regexp.MustCompile(awsNameRe).MatchString(clusterRef); matched {
		return clusterRef, nil
	}
	clusterEntry := rawConfig.Clusters[clusterRef]
	if clusterEntry != nil && clusterEntry.Server != "" {
		server := clusterEntry.Server
		parts := strings.Split(server, ".")
		if len(parts) > 0 {
			maybeName := strings.TrimPrefix(parts[0], "https://")
			if regexp.MustCompile(awsNameRe).MatchString(maybeName) {
				return maybeName, nil
			}
		}
	}
	return "", fmt.Errorf("could not determine valid EKS cluster name; please use --cluster flag")
}

func fetchNodegroups(ctx context.Context, awsCfg aws.Config, clusterName string) ([]NodegroupInfo, error) {
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
		currentAmiId := getCurrentAmiID(ctx, ng, ec2Client, autoscalingClient)
		if currentAmiId == "" {
			color.Yellow("[WARN] Could not determine AMI ID for nodegroup %s", *ng.NodegroupName)
		}
		latestAmiId := getLatestAmiID(ctx, ssmClient, k8sVersion)
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

func getCurrentAmiID(ctx context.Context, ng *types.Nodegroup, ec2Client *ec2.Client, autoscalingClient *autoscaling.Client) string {
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

func getLatestAmiID(ctx context.Context, ssmClient *ssm.Client, k8sVersion string) string {
	ssmParam := "/aws/service/eks/optimized-ami/" + k8sVersion + "/amazon-linux-2/recommended/image_id"
	ssmOut, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name: aws.String(ssmParam),
	})
	if err == nil && ssmOut.Parameter != nil && ssmOut.Parameter.Value != nil {
		return *ssmOut.Parameter.Value
	}
	return ""
}

func printNodegroupsTable(nodegroups []NodegroupInfo) {
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Nodegroup", "Status", "InstanceType", "Desired", "Current AMI", "AMI Status"})
	table.SetAutoWrapText(false)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	for _, ng := range nodegroups {
		row := []string{
			ng.Name,
			ng.Status,
			ng.InstanceType,
			fmt.Sprintf("%d", ng.Desired),
			ng.CurrentAmi,
			ng.AmiStatus,
		}
		table.Append(row)
	}
	table.Render()
}
