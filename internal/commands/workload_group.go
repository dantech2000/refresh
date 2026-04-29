package commands

import "github.com/urfave/cli/v2"

func WorkloadCommand() *cli.Command {
	return &cli.Command{
		Name:    "workload",
		Aliases: []string{"workloads"},
		Usage:   "Workload operations (PDB coverage)",
		Subcommands: []*cli.Command{
			workloadPDBsCommand(),
		},
	}
}
