package commands

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"
	"github.com/yarlson/pin"

	awsClient "github.com/dantech2000/refresh/internal/aws"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
)

func ListCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List all managed node groups",
		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:    "timeout",
				Aliases: []string{"t"},
				Usage:   "Operation timeout (e.g. 60s, 2m)",
				Value:   appconfig.DefaultTimeout,
				EnvVars: []string{"REFRESH_TIMEOUT"},
			},
			&cli.StringFlag{
				Name:    "cluster",
				Aliases: []string{"c"},
				Usage:   "EKS cluster name or partial name pattern (overrides kubeconfig)",
				EnvVars: []string{"EKS_CLUSTER_NAME"},
			},
			&cli.StringFlag{
				Name:    "nodegroup",
				Aliases: []string{"n"},
				Usage:   "Nodegroup name or partial name pattern to filter results",
			},
		},
		Action: func(c *cli.Context) error {
			ctx, cancelTimeout := context.WithTimeout(context.Background(), c.Duration("timeout"))
			defer cancelTimeout()
			awsCfg, err := config.LoadDefaultConfig(ctx)
			if err != nil {
				color.Red("Failed to load AWS config: %v", err)
				return err
			}

			// Validate AWS credentials early to provide better error messages
			if err := awsClient.ValidateAWSCredentials(ctx, awsCfg); err != nil {
				color.Red("%v", err)
				fmt.Println()
				awsClient.PrintCredentialHelp()
				return fmt.Errorf("AWS credential validation failed")
			}
			clusterName, err := awsClient.ClusterName(ctx, awsCfg, c.String("cluster"))
			if err != nil {
				color.Red("%v", err)
				return err
			}

			// Start spinner with funny messages
			messages := []string{
				"Interrogating EKS nodes... they're staying silent for now",
				"Asking AWS politely for nodegroup secrets...",
				"Counting how many nodes are having an existential crisis...",
				"Checking if the nodes have been doing their AMI homework...",
				"Waiting for AWS to stop procrastinating and return our data...",
				"Convincing stubborn nodes to reveal their status...",
				"Performing digital archaeology on your cluster...",
				"Teaching nodes to communicate in human language...",
				"Decoding the ancient art of EKS hieroglyphics...",
				"Bribing AWS APIs with virtual coffee for faster responses...",
			}

			spinner := pin.New("Gathering nodegroup information...",
				pin.WithSpinnerColor(pin.ColorCyan),
				pin.WithTextColor(pin.ColorYellow),
			)

			// Start spinner with message cycling
			cancel := spinner.Start(ctx)
			defer cancel()

			// Update messages periodically with a cancellable context
			messageCtx, msgCancel := context.WithCancel(ctx)
			defer msgCancel()
			go func() {
				ticker := time.NewTicker(2 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-messageCtx.Done():
						return
					case <-ticker.C:
						randomMsg := messages[rand.Intn(len(messages))]
						spinner.UpdateMessage(randomMsg)
					}
				}
			}()

			allNodegroups, err := awsClient.Nodegroups(ctx, awsCfg, clusterName)

			// Stop spinner and cancel message updater
			msgCancel()
			spinner.Stop("Nodegroup information gathered!")
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
				matchingNames := awsClient.MatchingNodegroups(allNames, nodegroupPattern)

				// Filter the NodegroupInfo slice to only include matches
				var filteredNodegroups []types.NodegroupInfo
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

				ui.PrintNodegroupsTree(clusterName, filteredNodegroups)
			} else {
				ui.PrintNodegroupsTree(clusterName, allNodegroups)
			}
			return nil
		},
	}
}
