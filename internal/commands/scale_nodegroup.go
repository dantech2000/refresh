package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"
	"github.com/yarlson/pin"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/services/nodegroup"
)

// ScaleNodegroupCommand creates the scale-nodegroup command
func ScaleNodegroupCommand() *cli.Command {
	return &cli.Command{
		Name:  "scale-nodegroup",
		Usage: "Scale a nodegroup's desired/min/max size with optional health checks",
		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:    "timeout",
				Aliases: []string{"t"},
				Usage:   "Operation timeout (e.g. 60s, 2m)",
				Value:   appconfig.DefaultTimeout,
				EnvVars: []string{"REFRESH_TIMEOUT"},
			},
			&cli.StringFlag{
				Name:     "cluster",
				Aliases:  []string{"c"},
				Usage:    "EKS cluster name",
				Required: false,
			},
			&cli.StringFlag{
				Name:     "nodegroup",
				Aliases:  []string{"n"},
				Usage:    "Nodegroup name",
				Required: true,
			},
			&cli.IntFlag{
				Name:  "desired",
				Usage: "Desired node count",
			},
			&cli.IntFlag{
				Name:  "min",
				Usage: "Minimum node count",
			},
			&cli.IntFlag{
				Name:  "max",
				Usage: "Maximum node count",
			},
			&cli.BoolFlag{Name: "health-check", Usage: "Validate cluster health before and after scaling"},
			&cli.BoolFlag{Name: "check-pdbs", Usage: "Validate Pod Disruption Budgets before scaling down"},
			&cli.BoolFlag{Name: "wait", Usage: "Wait for scaling operation to complete"},
			&cli.DurationFlag{Name: "op-timeout", Usage: "Scaling operation timeout", Value: 5 * time.Minute},
			&cli.BoolFlag{Name: "dry-run", Usage: "Preview scaling impact without executing"},
		},
		Action: func(c *cli.Context) error {
			return runScaleNodegroup(c)
		},
	}
}

func runScaleNodegroup(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		color.Red("Failed to load AWS config: %v", err)
		return err
	}

	if err := awsinternal.ValidateAWSCredentials(ctx, awsCfg); err != nil {
		color.Red("%v", err)
		fmt.Println()
		awsinternal.PrintCredentialHelp()
		return fmt.Errorf("AWS credential validation failed")
	}

	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, c.String("cluster"))
	if err != nil {
		return err
	}

	ngName := c.String("nodegroup")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// Centralized service init with optional health
	withHealth := c.Bool("health-check") || c.Bool("check-pdbs") || c.Bool("wait")
	svc := newNodegroupService(awsCfg, withHealth, logger)

	// Convert optional desired/min/max to pointers (zero means not set)
	var desiredPtr, minPtr, maxPtr *int32
	if c.IsSet("desired") {
		v := int32(c.Int("desired"))
		desiredPtr = &v
	}
	if c.IsSet("min") {
		v := int32(c.Int("min"))
		minPtr = &v
	}
	if c.IsSet("max") {
		v := int32(c.Int("max"))
		maxPtr = &v
	}

	opts := nodegroup.ScaleOptions{
		HealthCheck: c.Bool("health-check"),
		CheckPDBs:   c.Bool("check-pdbs"),
		Wait:        c.Bool("wait"),
		Timeout:     c.Duration("op-timeout"),
		DryRun:      c.Bool("dry-run"),
	}

	spinner := pin.New("Scaling nodegroup...",
		pin.WithSpinnerColor(pin.ColorCyan),
		pin.WithTextColor(pin.ColorYellow),
	)
	cancelSpinner := spinner.Start(ctx)
	defer cancelSpinner()

	if err := svc.Scale(ctx, clusterName, ngName, desiredPtr, minPtr, maxPtr, opts); err != nil {
		spinner.Stop("Scaling failed")
		return err
	}
	spinner.Stop("Scaling request submitted")
	return nil
}
