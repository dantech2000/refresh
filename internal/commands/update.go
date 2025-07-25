package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"

	awsClient "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/dryrun"
	"github.com/dantech2000/refresh/internal/monitoring"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

func UpdateAmiCommand() *cli.Command {
	return &cli.Command{
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
			clusterName, err := awsClient.ClusterName(ctx, awsCfg, c.String("cluster"))
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
			matches := awsClient.MatchingNodegroups(ngOut.Nodegroups, nodegroupPattern)
			selectedNodegroups, err := awsClient.ConfirmNodegroupSelection(matches, nodegroupPattern)
			if err != nil {
				color.Red("%v", err)
				return err
			}

			// Create monitor configuration
			config := refreshTypes.MonitorConfig{
				PollInterval:    pollInterval,
				MaxRetries:      3,
				BackoffMultiple: 2.0,
				Quiet:           quiet,
				NoWait:          noWait,
				Timeout:         timeout,
			}

			// Initialize progress monitor
			monitor := &refreshTypes.ProgressMonitor{
				Updates:   make([]refreshTypes.UpdateProgress, 0),
				StartTime: time.Now(),
				Quiet:     quiet,
				NoWait:    noWait,
				Timeout:   timeout,
			}

			// If dry run mode, show what would be updated with details
			if dryRun {
				return dryrun.PerformDryRun(ctx, eksClient, clusterName, selectedNodegroups, force, quiet)
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
				updateProgress := refreshTypes.UpdateProgress{
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
			return monitoring.MonitorUpdates(ctx, eksClient, monitor, config)
		},
	}
}
