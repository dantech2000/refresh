package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/urfave/cli/v2"
	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"

	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	app := &cli.App{
		Name:    "refresh",
		Usage:   "Manage and monitor AWS EKS node groups",
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

					// 1. Try --cluster flag
					clusterName := c.String("cluster")

					// 2. If not set, try to parse from kubeconfig
					if clusterName == "" {
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
							color.Red("Failed to load kubeconfig: %v", err)
							return err
						}
						currentContext := rawConfig.Contexts[rawConfig.CurrentContext]
						if currentContext == nil {
							color.Red("No current context in kubeconfig")
							return fmt.Errorf("no current context in kubeconfig")
						}
						// Try to parse cluster name from ARN or server URL
						clusterRef := currentContext.Cluster
						if clusterRef == "" {
							color.Red("Could not determine EKS cluster name from kubeconfig context")
							return fmt.Errorf("no cluster name in kubeconfig context")
						}
						// If clusterRef matches AWS regex, use it
						awsNameRe := "^[0-9A-Za-z][A-Za-z0-9-_]*$"
						if matched := regexp.MustCompile(awsNameRe).MatchString(clusterRef); matched {
							clusterName = clusterRef
						} else {
							// Try to extract cluster name from ARN or server URL
							clusterEntry := rawConfig.Clusters[clusterRef]
							if clusterEntry != nil && clusterEntry.Server != "" {
								// EKS API endpoint: https://<cluster-name>.<hash>.eks.<region>.amazonaws.com
								server := clusterEntry.Server
								parts := strings.Split(server, ".")
								if len(parts) > 0 {
									maybeName := strings.TrimPrefix(parts[0], "https://")
									if regexp.MustCompile(awsNameRe).MatchString(maybeName) {
										clusterName = maybeName
									}
								}
							}
						}
					}
					if clusterName == "" {
						color.Red("Could not determine valid EKS cluster name. Please use --cluster flag.")
						return fmt.Errorf("invalid cluster name; use --cluster")
					}

					// Initialize AWS SDK config
					awsCfg, err := config.LoadDefaultConfig(ctx)
					if err != nil {
						color.Red("Failed to load AWS config: %v", err)
						return err
					}

					eksClient := eks.NewFromConfig(awsCfg)

					// List nodegroups
					ngOut, err := eksClient.ListNodegroups(ctx, &eks.ListNodegroupsInput{
						ClusterName: aws.String(clusterName),
					})
					if err != nil {
						color.Red("Failed to list nodegroups: %v", err)
						return err
					}

					// Get latest recommended AMI for the cluster
					clusterOut, err := eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
						Name: aws.String(clusterName),
					})
					if err != nil {
						color.Red("Failed to describe cluster: %v", err)
						return err
					}
					k8sVersion := *clusterOut.Cluster.Version

					ec2Client := ec2.NewFromConfig(awsCfg)
					autoscalingClient := autoscaling.NewFromConfig(awsCfg)
					ssmClient := ssm.NewFromConfig(awsCfg)

					green := color.New(color.FgGreen).SprintFunc()
					red := color.New(color.FgRed).SprintFunc()

					table := tablewriter.NewWriter(os.Stdout)
					table.SetHeader([]string{"Nodegroup", "Status", "InstanceType", "Desired", "Current AMI", "AMI Status"})
					table.SetAutoWrapText(false)
					table.SetAlignment(tablewriter.ALIGN_LEFT)

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

						// Get AMI ID used by nodegroup (via launch template or directly)
						var currentAmiId string
						// 1. Try launch template if present and fields are non-nil
						if ng.LaunchTemplate != nil && ng.LaunchTemplate.Version != nil && ng.LaunchTemplate.Id != nil {
							ltOut, err := ec2Client.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{
								LaunchTemplateId: ng.LaunchTemplate.Id,
								Versions:         []string{*ng.LaunchTemplate.Version},
							})
							if err == nil && len(ltOut.LaunchTemplateVersions) > 0 && ltOut.LaunchTemplateVersions[0].LaunchTemplateData != nil && ltOut.LaunchTemplateVersions[0].LaunchTemplateData.ImageId != nil {
								currentAmiId = *ltOut.LaunchTemplateVersions[0].LaunchTemplateData.ImageId
							}
						}
						// 2. Try ASG instance if no launch template, and all pointers are non-nil
						if currentAmiId == "" && ng.Resources != nil && len(ng.Resources.AutoScalingGroups) > 0 && ng.Resources.AutoScalingGroups[0].Name != nil {
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
										currentAmiId = *descInstOut.Reservations[0].Instances[0].ImageId
									}
								}
							}
						}
						if currentAmiId == "" {
							color.Yellow("[WARN] Could not determine AMI ID for nodegroup %s", *ng.NodegroupName)
						}

						// Fetch latest recommended AMI from SSM
						ssmParam := "/aws/service/eks/optimized-ami/" + k8sVersion + "/amazon-linux-2/recommended/image_id"
						ssmOut, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
							Name: aws.String(ssmParam),
						})
						var latestAmiId string
						if err == nil && ssmOut.Parameter != nil && ssmOut.Parameter.Value != nil {
							latestAmiId = *ssmOut.Parameter.Value
						}

						amiStatus := red("❌ Outdated")
						if currentAmiId != "" && latestAmiId != "" && currentAmiId == latestAmiId {
							amiStatus = green("✅ Latest")
						}
						row := []string{
							*ng.NodegroupName,
							string(ng.Status),
							instanceType,
							fmt.Sprintf("%d", desired),
							currentAmiId,
							amiStatus,
						}
						table.Append(row)
					}
					table.Render()
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
