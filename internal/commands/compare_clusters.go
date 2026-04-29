package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/dantech2000/refresh/internal/awsconfig"
	"github.com/fatih/color"
	"github.com/pterm/pterm"
	"github.com/urfave/cli/v2"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// CompareClustersCommand creates the compare-clusters command
func CompareClustersCommand() *cli.Command {
	return &cli.Command{
		Name:    "compare-clusters",
		Aliases: []string{"cc"},
		Usage:   "Compare EKS clusters side-by-side for consistency validation",
		Description: `Analyze configuration differences between EKS clusters to ensure
consistency across environments. Supports comparison of networking,
security, add-ons, and version configurations.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "Cluster name or pattern (specify multiple times)", Required: true},
			&cli.BoolFlag{Name: "interactive", Usage: "Interactively select clusters when multiple patterns match"},
			&cli.BoolFlag{Name: "show-differences", Aliases: []string{"d"}, Usage: "Show only differences (hide identical configurations)"},
			&cli.StringSliceFlag{Name: "include", Aliases: []string{"i"}, Usage: "Compare specific aspects (networking, security, addons, versions)"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml)", Value: "table"},
		},
		Action: func(c *cli.Context) error { return runCompareClusters(c) },
	}
}

func runCompareClusters(c *cli.Context) error {
	timeout := c.Duration("timeout")
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	clusterPatterns := c.StringSlice("cluster")
	if len(clusterPatterns) < 2 {
		return fmt.Errorf("need at least 2 clusters to compare, use --cluster flag multiple times")
	}

	awsCfg, err := awsconfig.Load(ctx, c)
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

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	clusterService := newClusterService(awsCfg, true, logger)

	clusterNames, err := resolveCompareClusterNames(ctx, awsCfg, clusterPatterns, c.Bool("interactive"))
	if err != nil {
		return err
	}

	options := cluster.CompareOptions{
		ShowDifferencesOnly: c.Bool("show-differences"),
		Include:             c.StringSlice("include"),
		Format:              c.String("format"),
	}

	spinner := ui.NewFunSpinnerForCategory("cluster")
	if err := spinner.Start(); err != nil {
		return err
	}
	defer spinner.Stop()

	startTime := time.Now()
	comparison, err := clusterService.Compare(ctx, clusterNames, options)
	spinner.Success("Analysis complete!")
	if err != nil {
		return err
	}
	elapsed := time.Since(startTime)

	switch strings.ToLower(c.String("format")) {
	case "json":
		return outputComparisonJSON(comparison)
	case "yaml":
		return outputComparisonYAML(comparison)
	default:
		return outputComparisonTable(comparison, elapsed)
	}
}

// resolveCompareClusterNames resolves each pattern to one or more cluster names
// for use in a comparison. When a pattern is ambiguous (matches >1 cluster) it
// either launches an interactive multi-select (interactive=true or --interactive
// flag) or returns an error directing the user to use that flag.
func resolveCompareClusterNames(ctx context.Context, awsCfg aws.Config, patterns []string, interactive bool) ([]string, error) {
	spinner := ui.NewFunSpinnerForCategory("general")
	if err := spinner.Start(); err != nil {
		return nil, err
	}
	all, err := awsinternal.AvailableClusters(ctx, awsCfg)
	spinner.Stop()
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, "listing EKS clusters")
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("no EKS clusters found in current region")
	}

	// Expand each pattern into its matches, de-duplicate across all patterns.
	seen := make(map[string]bool)
	var candidates []string
	ambiguous := false

	for _, pat := range patterns {
		matches := awsinternal.MatchingClusters(all, pat)
		switch len(matches) {
		case 0:
			return nil, fmt.Errorf("no clusters found matching pattern %q", pat)
		case 1:
			if !seen[matches[0]] {
				seen[matches[0]] = true
				candidates = append(candidates, matches[0])
			}
		default:
			ambiguous = true
			for _, m := range matches {
				if !seen[m] {
					seen[m] = true
					candidates = append(candidates, m)
				}
			}
		}
	}

	if ambiguous || interactive {
		return interactiveSelectClusters(candidates)
	}

	if len(candidates) < 2 {
		return nil, fmt.Errorf("need at least 2 unique clusters to compare (patterns resolved to %d)", len(candidates))
	}
	return candidates, nil
}

// interactiveSelectClusters shows a pterm multi-select and returns the chosen clusters.
// At least 2 must be selected.
func interactiveSelectClusters(candidates []string) ([]string, error) {
	color.Cyan("Select clusters to compare (space to toggle, enter to confirm):")
	selected, err := pterm.DefaultInteractiveMultiselect.
		WithOptions(candidates).
		WithMaxHeight(15).
		Show()
	if err != nil {
		return nil, fmt.Errorf("cluster selection cancelled: %w", err)
	}
	if len(selected) < 2 {
		return nil, fmt.Errorf("select at least 2 clusters to compare (got %d)", len(selected))
	}
	return selected, nil
}

func removeDuplicates(slice []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range slice {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
