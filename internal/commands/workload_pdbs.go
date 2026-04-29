package commands

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"

	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/workloads"
	"github.com/dantech2000/refresh/internal/ui"
)

func workloadPDBsCommand() *cli.Command {
	return &cli.Command{
		Name:    "pdbs",
		Aliases: []string{"pdb", "pod-disruption-budgets"},
		Usage:   "List deployments with and without PodDisruptionBudgets",
		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:    "timeout",
				Aliases: []string{"t"},
				Usage:   "Operation timeout (e.g. 30s, 1m)",
				Value:   appconfig.DefaultTimeout,
				EnvVars: []string{"REFRESH_TIMEOUT"},
			},
			&cli.StringFlag{
				Name:    "namespace",
				Aliases: []string{"n"},
				Usage:   "Only check deployments in this namespace",
			},
			&cli.BoolFlag{
				Name:  "include-system",
				Usage: "Include system namespaces (kube-system, kube-public, kube-node-lease, default)",
			},
			&cli.StringFlag{
				Name:    "format",
				Aliases: []string{"o"},
				Usage:   "Output format (table, json, yaml)",
				Value:   "table",
			},
		},
		Action: runWorkloadPDBs,
	}
}

func runWorkloadPDBs(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	client, err := health.GetKubernetesClient()
	if err != nil {
		return err
	}

	start := time.Now()
	result, err := workloads.AnalyzePDBCoverage(ctx, client, workloads.PDBCoverageOptions{
		Namespace:     strings.TrimSpace(c.String("namespace")),
		IncludeSystem: c.Bool("include-system"),
	})
	if err != nil {
		return err
	}

	switch strings.ToLower(c.String("format")) {
	case "json":
		return outputPDBCoverageJSON(result)
	case "yaml":
		return outputPDBCoverageYAML(result)
	default:
		return outputPDBCoverageTable(result, time.Since(start))
	}
}

func outputPDBCoverageJSON(result workloads.PDBCoverageResult) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func outputPDBCoverageYAML(result workloads.PDBCoverageResult) error {
	enc := yaml.NewEncoder(os.Stdout)
	enc.SetIndent(2)
	defer func() { _ = enc.Close() }()
	return enc.Encode(result)
}

func outputPDBCoverageTable(result workloads.PDBCoverageResult, elapsed time.Duration) error {
	ui.Outf("PDB Coverage\n")
	ui.Outf("Retrieved in %s\n\n", color.GreenString("%.1fs", elapsed.Seconds()))

	if result.Summary.TotalDeployments == 0 {
		color.Yellow("No deployments found")
		return nil
	}

	cols := []ui.Column{
		{Title: "NAMESPACE", Min: 9, Align: ui.AlignLeft},
		{Title: "DEPLOYMENT", Min: 10, Align: ui.AlignLeft},
		{Title: "STATUS", Min: 9, Align: ui.AlignLeft},
		{Title: "PDBS", Min: 4, Align: ui.AlignLeft},
	}
	table := ui.NewPTable(cols, ui.WithPTableHeaderColor(func(s string) string { return color.CyanString(s) }))
	for _, row := range result.Deployments {
		status := color.RedString("MISSING")
		if row.HasPDB {
			status = color.GreenString("PROTECTED")
		}
		pdbs := "-"
		if len(row.PDBs) > 0 {
			pdbs = strings.Join(row.PDBs, ", ")
		}
		table.AddRow(row.Namespace, row.Deployment, status, pdbs)
	}
	table.Render()

	ui.Outf("\nSummary: %s protected, %s missing PDBs, %d total deployments\n",
		color.GreenString("%d", result.Summary.WithPDB),
		color.RedString("%d", result.Summary.WithoutPDB),
		result.Summary.TotalDeployments)
	if result.Summary.WithoutPDB > 0 {
		ui.Outf("Create PDBs for missing deployments before voluntary disruption or nodegroup maintenance.\n")
	}
	return nil
}
