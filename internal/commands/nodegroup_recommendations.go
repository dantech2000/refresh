package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"
	"github.com/yarlson/pin"
	"gopkg.in/yaml.v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/ui"
)

// NodegroupRecommendationsCommand creates the nodegroup-recommendations command
func NodegroupRecommendationsCommand() *cli.Command {
	return &cli.Command{
		Name:  "nodegroup-recommendations",
		Usage: "Generate optimization recommendations for nodegroups",
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout", Value: appconfig.DefaultTimeout},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name"},
			&cli.StringFlag{Name: "nodegroup", Aliases: []string{"n"}, Usage: "Specific nodegroup (optional)"},
			&cli.BoolFlag{Name: "cost-optimization"},
			&cli.BoolFlag{Name: "performance-optimization"},
			&cli.BoolFlag{Name: "spot-analysis"},
			&cli.BoolFlag{Name: "right-sizing"},
			&cli.StringFlag{Name: "timeframe", Value: "30d"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml)", Value: "table"},
		},
		Action: func(c *cli.Context) error { return runNodegroupRecommendations(c) },
	}
}

func runNodegroupRecommendations(c *cli.Context) error {
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

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	svc := nodegroup.NewService(awsCfg, nil, logger)

	opts := nodegroup.RecommendationOptions{
		Nodegroup:               c.String("nodegroup"),
		CostOptimization:        c.Bool("cost-optimization"),
		PerformanceOptimization: c.Bool("performance-optimization"),
		SpotAnalysis:            c.Bool("spot-analysis"),
		RightSizing:             c.Bool("right-sizing"),
		Timeframe:               c.String("timeframe"),
	}

	spinner := pin.New("Analyzing optimization opportunities...",
		pin.WithSpinnerColor(pin.ColorCyan),
		pin.WithTextColor(pin.ColorYellow),
	)
	cancelSpinner := spinner.Start(ctx)
	defer cancelSpinner()

	recs, err := svc.GetRecommendations(ctx, clusterName, opts)
	spinner.Stop("Analysis complete!")
	if err != nil {
		return err
	}

	switch strings.ToLower(c.String("format")) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"cluster": clusterName, "recommendations": recs})
	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(map[string]any{"cluster": clusterName, "recommendations": recs})
	default:
		return outputRecommendationsTable(clusterName, recs)
	}
}

func outputRecommendationsTable(cluster string, recs []nodegroup.Recommendation) error {
	fmt.Printf("Nodegroup Optimization Recommendations for: %s\n\n", color.CyanString(cluster))
	if len(recs) == 0 {
		color.Green("No recommendations available (placeholder)")
		return nil
	}

	columns := []ui.Column{
		{Title: "TYPE", Min: 12, Max: 0, Align: ui.AlignLeft},
		{Title: "PRIORITY", Min: 6, Max: 0, Align: ui.AlignLeft},
		{Title: "IMPACT", Min: 6, Max: 0, Align: ui.AlignLeft},
		{Title: "DESCRIPTION", Min: 8, Max: 60, Align: ui.AlignLeft},
	}
	table := ui.NewTable(columns, ui.WithHeaderColor(func(s string) string { return color.CyanString(s) }))
	for _, r := range recs {
		table.AddRow(r.Type, r.Priority, r.Impact, r.Description)
	}
	table.Render()
	return nil
}
