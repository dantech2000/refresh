package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"
	"github.com/yarlson/pin"
	"gopkg.in/yaml.v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

type addonRow struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
	Health  string `json:"health"`
}

// ListAddonsCommand lists EKS add-ons for a cluster
func ListAddonsCommand() *cli.Command {
	return &cli.Command{
		Name:      "list-addons",
		Usage:     "List EKS add-ons in a cluster",
		ArgsUsage: "[cluster]",
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout", Value: appconfig.DefaultTimeout, EnvVars: []string{"REFRESH_TIMEOUT"}},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.BoolFlag{Name: "show-health", Aliases: []string{"H"}, Usage: "Include health mapping in table output"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml)", Value: "table"},
		},
		Action: func(c *cli.Context) error { return runListAddons(c) },
	}
}

func runListAddons(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		color.Red("Failed to load AWS config: %v", err)
		return err
	}
	if err := awsinternal.ValidateAWSCredentials(ctx, cfg); err != nil {
		color.Red("%v", err)
		fmt.Println()
		awsinternal.PrintCredentialHelp()
		return fmt.Errorf("AWS credential validation failed")
	}
	// Positional cluster support; list clusters if omitted
	requested := c.Args().First()
	if requested == "" {
		requested = c.String("cluster")
	}
	if strings.TrimSpace(requested) == "" {
		fmt.Println("No cluster specified. Available clusters:")
		fmt.Println()
		start := time.Now()
		svc := newClusterService(cfg, false, nil)
		summaries, err := svc.List(ctx, cluster.ListOptions{})
		if err != nil {
			return err
		}
		_ = outputClustersTable(summaries, time.Since(start), false, false)
		return nil
	}
	clusterName, err := awsinternal.ClusterName(ctx, cfg, requested)
	if err != nil {
		return err
	}
	eksClient := eks.NewFromConfig(cfg)

	spinner := pin.New("Gathering add-on information...",
		pin.WithSpinnerColor(pin.ColorCyan),
		pin.WithTextColor(pin.ColorYellow),
	)
	cancelSpin := spinner.Start(ctx)
	defer cancelSpin()

	start := time.Now()
	rows, err := fetchAddons(ctx, eksClient, clusterName, c.Bool("show-health"))
	spinner.Stop("Add-on information gathered!")
	if err != nil {
		return err
	}

	switch strings.ToLower(c.String("format")) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"cluster": clusterName, "addons": rows, "count": len(rows)})
	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(map[string]any{"cluster": clusterName, "addons": rows, "count": len(rows)})
	default:
		return outputAddonsTable(clusterName, rows, time.Since(start))
	}
}

func fetchAddons(ctx context.Context, eksClient *eks.Client, clusterName string, withHealth bool) ([]addonRow, error) {
	var addonNames []string
	var nextToken *string
	for {
		out, err := eksClient.ListAddons(ctx, &eks.ListAddonsInput{ClusterName: aws.String(clusterName), NextToken: nextToken})
		if err != nil {
			return nil, awsinternal.FormatAWSError(err, "listing add-ons")
		}
		addonNames = append(addonNames, out.Addons...)
		if out.NextToken == nil || (out.NextToken != nil && aws.ToString(out.NextToken) == "") {
			break
		}
		nextToken = out.NextToken
	}
	rows := make([]addonRow, 0, len(addonNames))
	for _, name := range addonNames {
		d, err := eksClient.DescribeAddon(ctx, &eks.DescribeAddonInput{ClusterName: aws.String(clusterName), AddonName: aws.String(name)})
		if err != nil || d.Addon == nil {
			rows = append(rows, addonRow{Name: name, Version: "", Status: "UNKNOWN", Health: "Unknown"})
			continue
		}
		health := ""
		if withHealth {
			health = mapAddonHealth(d.Addon.Status)
		}
		rows = append(rows, addonRow{Name: aws.ToString(d.Addon.AddonName), Version: aws.ToString(d.Addon.AddonVersion), Status: string(d.Addon.Status), Health: health})
	}
	return rows, nil
}

func mapAddonHealth(s ekstypes.AddonStatus) string {
	switch s {
	case ekstypes.AddonStatusActive:
		return color.GreenString("PASS")
	case ekstypes.AddonStatusDegraded:
		return color.RedString("FAIL")
	case ekstypes.AddonStatusCreateFailed, ekstypes.AddonStatusDeleteFailed:
		return color.RedString("FAIL")
	case ekstypes.AddonStatusCreating, ekstypes.AddonStatusDeleting, ekstypes.AddonStatusUpdating:
		return color.CyanString("[IN PROGRESS]")
	default:
		return color.WhiteString("UNKNOWN")
	}
}

func outputAddonsTable(cluster string, rows []addonRow, elapsed time.Duration) error {
	fmt.Printf("Add-ons for cluster: %s\n", color.CyanString(cluster))
	fmt.Printf("Retrieved in %s\n\n", color.GreenString("%.1fs", elapsed.Seconds()))

	if len(rows) == 0 {
		color.Yellow("No add-ons found")
		return nil
	}

	columns := []ui.Column{
		{Title: "NAME", Min: 4, Max: 24, Align: ui.AlignLeft},
		{Title: "VERSION", Min: 8, Max: 0, Align: ui.AlignLeft},
		{Title: "STATUS", Min: 10, Max: 0, Align: ui.AlignLeft},
		{Title: "HEALTH", Min: 8, Max: 0, Align: ui.AlignLeft},
	}
	table := ui.NewTable(columns, ui.WithHeaderColor(func(s string) string { return color.CyanString(s) }))
	for _, r := range rows {
		table.AddRow(r.Name, r.Version, r.Status, r.Health)
	}
	table.Render()
	return nil
}
