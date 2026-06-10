// Package workload provides CLI commands for Kubernetes workload operations.
package workload

import (
	"github.com/urfave/cli/v2"

	appconfig "github.com/dantech2000/refresh/internal/config"
)

// Command returns the workload command group.
func Command() *cli.Command {
	return &cli.Command{
		Name:    "workload",
		Aliases: []string{"workloads"},
		Usage:   "Workload operations (PDB coverage)",
		Subcommands: []*cli.Command{
			pdbsCommand(),
		},
	}
}

func pdbsCommand() *cli.Command {
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
				Usage:   "Output format (table, json, yaml, plain)",
				Value:   "table",
			},
		},
		Action: runPDBs,
	}
}
