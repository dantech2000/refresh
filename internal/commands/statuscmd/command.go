// Package statuscmd wires the top-level `refresh status` command — the fleet
// patch-posture "front door".
package statuscmd

import (
	"context"

	"github.com/urfave/cli/v3"
)

// Command returns the `refresh status` top-level command.
func Command() *cli.Command {
	return &cli.Command{
		Name:      "status",
		Usage:     "Fleet patch posture across clusters and regions (the front door)",
		ArgsUsage: "[name-pattern]",
		Description: `One command, all clusters, all regions, one table: Kubernetes version,
EKS support window (with extended-support cost callout), nodegroup AMI
staleness, and addons behind latest.

Exit codes (for CI/cron):
  0  everything current and in standard support
  2  something stale (nodegroup AMI or addon behind latest)
  3  a cluster is on extended support or unsupported`,
		Flags: []cli.Flag{
			// --timeout and --max-concurrency come from the global/persistent
			// flags (see main.go); status reads them via cmd.Duration/cmd.Int and
			// does not re-declare them, so help lists each once. (REF-134)
			//
			// --region is intentionally local: a repeatable slice for "scan these
			// regions", which deliberately shadows the global single-string AWS
			// region override. (REF-47)
			&cli.BoolFlag{Name: "all-regions", Aliases: []string{"A"}, Usage: "Query all EKS-supported regions"},
			&cli.StringSliceFlag{Name: "region", Aliases: []string{"r"}, Usage: "Specific region(s) to query (repeatable)"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain)", Value: "table"},
			&cli.StringFlag{Name: "sort", Usage: "Sort by field: cluster,region,version,support,stale", Value: "cluster"},
			&cli.BoolFlag{Name: "desc", Usage: "Sort descending"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error { return runStatus(ctx, cmd) },
	}
}
