package workload

import (
	"context"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/workloads"
)

func runPDBs(c *cli.Context) error {
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
