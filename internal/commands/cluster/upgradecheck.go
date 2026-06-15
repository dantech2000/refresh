package cluster

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/clusterview"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	appconfig "github.com/dantech2000/refresh/internal/config"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/services/status"
)

func upgradeCheckCommand() *cli.Command {
	return &cli.Command{
		Name:      "upgrade-check",
		Usage:     "Report upgrade readiness: EKS Cluster Insights + version skew (read-only)",
		ArgsUsage: "[cluster]",
		Description: `Read-only upgrade-readiness report for an EKS cluster.

Surfaces AWS Cluster Insights (the same upgrade checks the console shows) plus a
local version-skew picture: control-plane version vs each managed nodegroup, and
installed addons vs the latest compatible version — with ordered, actionable
findings. Nothing is mutated; this is the pre-flight read before 'cluster upgrade'.

Examples:
   refresh cluster upgrade-check -c prod-east
   refresh cluster upgrade-check -c prod-east --show-passing -o json
   refresh cluster upgrade-check -c prod-east --id <insight-id>   # detail view`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout (e.g. 60s, 2m)", Value: appconfig.DefaultTimeout, Sources: cli.EnvVars("REFRESH_TIMEOUT")},
			&cli.StringFlag{Name: "category", Usage: "Insight category (UPGRADE_READINESS, MISCONFIGURATION)", Value: "UPGRADE_READINESS"},
			&cli.StringSliceFlag{Name: "status", Usage: "Filter by insight status (PASSING, WARNING, ERROR, UNKNOWN)"},
			&cli.BoolFlag{Name: "show-passing", Usage: "Include PASSING insights (hidden by default)"},
			&cli.StringFlag{Name: "id", Usage: "Show the detail view (recommendation + resources) for a single insight ID"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain)", Value: "table"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error { return runUpgradeCheck(ctx, cmd) },
	}
}

func runUpgradeCheck(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsStandard); err != nil {
		return err
	}
	ctx, cancel, awsCfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, awsCfg, cmd)
	if err != nil || listed {
		return err
	}

	service := factory.NewClusterService(awsCfg, false, nil)

	// Detail view for a single insight.
	if id := cmd.String("id"); id != "" {
		var detail *clustersvc.InsightDetail
		if werr := runner.WithSpinner("cluster", "Insight detail loaded!", func() error {
			var derr error
			detail, derr = service.DescribeInsight(ctx, clusterName, id)
			return derr
		}); werr != nil {
			return werr
		}
		if handled, encErr := runner.EncodeStdout(cmd.String("format"), detail); handled {
			return encErr
		}
		return clusterview.OutputInsightDetail(detail)
	}

	opts := clustersvc.UpgradeCheckOptions{
		Category:    cmd.String("category"),
		Statuses:    cmd.StringSlice("status"),
		ShowPassing: cmd.Bool("show-passing"),
	}

	var report *clustersvc.UpgradeReport
	if werr := runner.WithSpinner("cluster", "Upgrade readiness computed!", func() error {
		var rerr error
		report, rerr = service.UpgradeCheck(ctx, clusterName, opts)
		return rerr
	}); werr != nil {
		return werr
	}

	// Support posture for the control-plane version, via the same resolver
	// behind `refresh status` (REF-145).
	if report != nil && report.Skew.ControlPlaneVersion != "" {
		posture := status.NewSupportResolver(eks.NewFromConfig(awsCfg)).Resolve(ctx, report.Skew.ControlPlaneVersion)
		report.Support = &posture
	}

	if handled, encErr := runner.EncodeStdout(cmd.String("format"), report); handled {
		return encErr
	}
	return clusterview.OutputUpgradeCheck(report)
}
