package workload

import (
	"context"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/workloads"
	"github.com/dantech2000/refresh/internal/ui"
)

func runPDBs(ctx context.Context, cmd *cli.Command) error {
	ctx, cancel := context.WithTimeout(ctx, cmd.Duration("timeout"))
	defer cancel()

	client, err := health.GetKubernetesClient()
	if err != nil {
		return err
	}

	start := time.Now()
	format := strings.ToLower(cmd.String("format"))
	var spinner *ui.FunSpinner
	if format == "table" || format == "plain" || format == "" {
		spinner = ui.NewFunSpinnerForCategory("workload")
		if err := spinner.Start(); err != nil {
			return err
		}
		defer spinner.Stop()
	}

	result, err := workloads.AnalyzePDBCoverage(ctx, client, workloads.PDBCoverageOptions{
		Namespace:     strings.TrimSpace(cmd.String("namespace")),
		IncludeSystem: cmd.Bool("include-system"),
	})
	if err != nil {
		if spinner != nil {
			spinner.Fail("PDB coverage check failed")
		}
		return err
	}
	if spinner != nil {
		spinner.Success("PDB coverage gathered!")
	}

	switch format {
	case "json":
		return outputPDBCoverageJSON(result)
	case "yaml":
		return outputPDBCoverageYAML(result)
	default:
		return outputPDBCoverageTable(result, time.Since(start))
	}
}
