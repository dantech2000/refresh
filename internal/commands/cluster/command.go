// Package cluster provides CLI commands for EKS cluster operations.
package cluster

import (
	"time"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/flagcanon"
)

// Command returns the cluster command group with list, describe, upgrade-check,
// and upgrade subcommands.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "cluster",
		Usage: "Cluster operations (list, describe, upgrade-check, upgrade)",
		Description: `Discover and operate on EKS clusters: list them (optionally across all
regions), describe one in depth, run an upgrade readiness check
(upgrade-check), and orchestrate a full control-plane + add-on + nodegroup
upgrade (upgrade).`,
		Commands: []*cli.Command{
			listCommand(),
			describeCommand(),
			upgradeCheckCommand(),
			upgradeCommand(),
		},
	}
}

func listCommand() *cli.Command {
	return &cli.Command{
		Name:      "list",
		Usage:     "List EKS clusters with health status (multi-region support)",
		ArgsUsage: "[name-pattern]",
		Description: `Fast cluster discovery across regions with integrated health validation.
Direct EKS API calls provide high performance along with comprehensive
health monitoring and multi-region capabilities.

Scope with -A/--all-regions (every EKS-supported region) or repeated
-r/--region. Filter with repeatable --filter key=value (keys: name, status,
version); sort with --sort and --desc. -o plain emits uncolored TSV for
grep/awk; -o tree (or --tree, which implies --all-regions) renders a
region/cluster hierarchy. Use --watch to redraw on the --watch-interval
(top-style on a terminal, appended when piped) until Ctrl+C.

  refresh cluster list -A --filter status=ACTIVE
  refresh cluster list -o tree
  refresh cluster list --watch --watch-interval 5s`,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "all-regions", Aliases: []string{"A"}, Usage: "Query all EKS-supported regions"},
			&cli.StringFlag{Name: "sort", Usage: "Sort by field: name,status,version,region", Value: "name"},
			&cli.BoolFlag{Name: "desc", Usage: "Sort descending"},
			&cli.StringSliceFlag{Name: "region", Aliases: []string{"r"}, Usage: "Specific region(s) to query (can be used multiple times)"},
			&cli.BoolFlag{Name: "show-health", Aliases: []string{"H"}, Usage: "Include health status for each cluster"},
			&cli.StringSliceFlag{Name: "filter", Aliases: []string{"f"}, Usage: "Filter clusters (format: key=value)"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain, tree)", Value: "table"},
			&cli.BoolFlag{Name: "tree", Aliases: []string{"T"}, Usage: "Display results as hierarchical tree (implies --all-regions)"},
			&cli.BoolFlag{Name: "watch", Aliases: []string{"w"}, Usage: "Re-run and redraw every --watch-interval until interrupted"},
			&cli.DurationFlag{Name: "watch-interval", Usage: "Refresh interval for --watch", Value: 10 * time.Second},
		},
		Action: runList,
	}
}

func describeCommand() *cli.Command {
	return &cli.Command{
		Name:      "describe",
		Aliases:   []string{"get"},
		Usage:     "Describe comprehensive cluster information",
		ArgsUsage: "[cluster]",
		Description: `Get detailed information about an EKS cluster including networking,
security configuration, add-ons, and health status. Direct EKS API calls
provide fast, comprehensive results without CloudFormation dependency.

Health status and add-ons are shown by default; --no-health and --no-addons
skip them. --detailed adds networking and security, --show-security adds the
security analysis alone.`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.BoolFlag{Name: "detailed", Usage: "Show comprehensive information including networking and security"},
			&cli.BoolFlag{Name: "no-health", Usage: "Skip the health checks (shown by default)"},
			&cli.BoolFlag{Name: "show-security", Usage: "Include security configuration analysis"},
			&cli.BoolFlag{Name: "no-addons", Usage: "Skip the EKS add-on section (shown by default)"},
			// Deprecated in 0.11.0: both were on by default, so the switches did
			// nothing. --show-health=false / --include-addons=false still map to
			// --no-health / --no-addons with a warning, for one release.
			flagcanon.DeprecatedSwitch("show-health", "no-health", "H"),
			flagcanon.DeprecatedSwitch("include-addons", "no-addons"),
			&cli.BoolFlag{Name: "check-readiness", Aliases: []string{"R"}, Usage: "Measure real Kubernetes node readiness (Ready/desired) via the cluster API; without it NODES shows desired count only"},
			runner.KubeconfigFlag("--check-readiness"),
			runner.KubeContextFlag(),
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain)", Value: "table"},
		},
		Action: runDescribe,
	}
}
