package workload

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"gopkg.in/yaml.v3"

	"github.com/dantech2000/refresh/internal/services/workloads"
	"github.com/dantech2000/refresh/internal/ui"
)

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
