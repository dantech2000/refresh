package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/awsconfig"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// DescribeClusterCommand creates the describe-cluster command
func DescribeClusterCommand() *cli.Command {
	return &cli.Command{
		Name:      "describe-cluster",
		Aliases:   []string{"dc"},
		Usage:     "Describe comprehensive cluster information",
		ArgsUsage: "[cluster]",
		Description: `Get detailed information about an EKS cluster including networking,
security configuration, add-ons, and health status. Direct EKS API calls
provide fast, comprehensive results without CloudFormation dependency.`,
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout (e.g. 60s, 2m)", Value: appconfig.DefaultTimeout, EnvVars: []string{"REFRESH_TIMEOUT"}},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.BoolFlag{Name: "detailed", Aliases: []string{"d"}, Usage: "Show comprehensive information including networking and security"},
			&cli.BoolFlag{Name: "show-health", Aliases: []string{"H"}, Usage: "Include health status from existing health framework", Value: true},
			&cli.BoolFlag{Name: "show-security", Aliases: []string{"s"}, Usage: "Include security configuration analysis"},
			&cli.BoolFlag{Name: "include-addons", Aliases: []string{"a"}, Usage: "Include EKS add-on information", Value: true},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml)", Value: "table"},
		},
		Action: func(c *cli.Context) error { return runDescribeCluster(c) },
	}
}

func runDescribeCluster(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	awsCfg, err := awsconfig.Load(ctx, c)
	if err != nil {
		color.Red("Failed to load AWS config: %v", err)
		return err
	}
	if err := awsinternal.ValidateAWSCredentials(ctx, awsCfg); err != nil {
		color.Red("%v", err)
		ui.Outln()
		awsinternal.PrintCredentialHelp()
		return fmt.Errorf("AWS credential validation failed")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	requested := c.Args().First()
	if requested == "" {
		requested = c.String("cluster")
	}
	if strings.TrimSpace(requested) == "" {
		svc := newClusterService(awsCfg, false, logger)
		start := time.Now()
		summaries, err := svc.List(ctx, cluster.ListOptions{})
		if err != nil {
			return err
		}
		ui.Outln("No cluster specified. Available clusters:")
		ui.Outln()
		_ = outputClustersTable(summaries, time.Since(start), false, false)
		return nil
	}

	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, requested)
	if err != nil {
		return err
	}

	clusterService := newClusterService(awsCfg, c.Bool("show-health"), logger)
	options := cluster.DescribeOptions{
		ShowHealth:    c.Bool("show-health"),
		ShowSecurity:  c.Bool("show-security") || c.Bool("detailed"),
		IncludeAddons: c.Bool("include-addons"),
		Detailed:      c.Bool("detailed"),
	}

	spinner := ui.NewFunSpinnerForCategory("cluster")
	if err := spinner.Start(); err != nil {
		return err
	}
	defer spinner.Stop()

	startTime := time.Now()
	details, err := clusterService.Describe(ctx, clusterName, options)
	spinner.Success("Cluster information gathered!")
	if err != nil {
		return err
	}
	elapsed := time.Since(startTime)

	switch strings.ToLower(c.String("format")) {
	case "json":
		return outputClusterDetailsJSON(details)
	case "yaml":
		return outputClusterDetailsYAML(details)
	default:
		return outputClusterDetailsTable(details, elapsed)
	}
}
