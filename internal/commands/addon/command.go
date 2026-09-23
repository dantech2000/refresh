// Package addon provides CLI commands for EKS add-on operations.
package addon

import (
	"context"
	"time"

	"github.com/urfave/cli/v3"

	appconfig "github.com/dantech2000/refresh/internal/config"
)

// Command returns the addon command group with list/describe/update subcommands.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "addon",
		Usage: "EKS add-on operations (list, get, update)",
		Description: `Inspect and update the managed EKS add-ons (vpc-cni, coredns, kube-proxy,
and others) on a cluster. List shows installed versions and status, describe
drills into one add-on, and update rolls a single add-on or every add-on
(--all) to a compatible version with optional health gating and waiting.`,
		Commands: []*cli.Command{
			listCommand(),
			describeCommand(),
			updateCommand(),
			updateAllHiddenCommand(),
		},
	}
}

func listCommand() *cli.Command {
	return &cli.Command{
		Name:      "list",
		Usage:     "List EKS add-ons in a cluster",
		ArgsUsage: "[cluster]",
		Description: `List the managed EKS add-ons installed on a cluster along with their
current version, status, and (with --health) a health badge.

Use --watch to keep the listing live: it redraws on the --watch-interval
(top-style on a terminal, appended when the output is piped) so you can watch
an add-on update progress without re-running the command. Press Ctrl+C to stop.

Examples:
  refresh addon list my-cluster
  refresh addon list my-cluster -o plain
  refresh addon list my-cluster --watch --watch-interval 5s`,
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout", Value: appconfig.DefaultTimeout, Sources: cli.EnvVars("REFRESH_TIMEOUT")},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.BoolFlag{Name: "show-health", Aliases: []string{"H"}, Usage: "Include health mapping in table output"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain)", Value: "table"},
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
		Usage:     "Describe a specific EKS add-on",
		ArgsUsage: "[cluster] [addon]",
		Description: `Show detailed information for one add-on: its version, status, and
configuration. The add-on name may be the second positional or --addon, and a
unique case-insensitive substring is resolved against the installed add-ons.

  refresh addon describe my-cluster vpc-cni
  refresh addon describe my-cluster coredns -o json`,
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout", Value: appconfig.DefaultTimeout, Sources: cli.EnvVars("REFRESH_TIMEOUT")},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.StringFlag{Name: "addon", Aliases: []string{"a"}, Usage: "Add-on name (e.g., vpc-cni)"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain)", Value: "table"},
		},
		Action: runDescribe,
	}
}

// updateCommand merges single-addon update and update-all behavior.
// Use --all to update every addon in the cluster.
func updateCommand() *cli.Command {
	return &cli.Command{
		Name:      "update",
		Usage:     "Update an EKS add-on (use --all to update every add-on)",
		ArgsUsage: "[cluster] [addon] [version]",
		Description: `Update a single managed add-on to a target version, or with --all update
every add-on in the cluster to its latest compatible version.

Single add-on: pass the add-on name and an optional version (defaults to
'latest'). The version may be the third positional or --version:

  refresh addon update my-cluster vpc-cni            # vpc-cni -> latest
  refresh addon update my-cluster coredns v1.11.1    # pin a version
  refresh addon update my-cluster vpc-cni --dry-run  # preview only

All add-ons (--all): updates every add-on, optionally in dependency-safe
order, in parallel, and/or waiting for each to settle. --parallel and
--dependency-order are mutually exclusive (parallel defeats ordering). The
--parallel/--skip/--dependency-order flags apply only with --all and are
ignored (with a warning) on a single-add-on update. The command exits non-zero
if any add-on update fails.

  refresh addon update my-cluster --all --dependency-order --wait
  refresh addon update my-cluster --all --skip vpc-cni --parallel

--all cannot be combined with an add-on name or version.

An ACTIVE add-on already at the target version is reported as UP_TO_DATE and
left alone; one that is DEGRADED or failed at the target is re-applied, and
one already UPDATING at the target is IN_PROGRESS. 'latest' never downgrades. A pinned version older than the installed
one proceeds with a warning. A name that only partially matches an installed
add-on is confirmed on a terminal; without one, pass the exact name or --yes.

With --wait, the command follows the EKS update until it succeeds, fails, or
is cancelled, then checks that the add-on reports the target version. Exit
codes: 0 success, 1 failure, 2 updated but the post-update health check found
issues (COMPLETED_WITH_ISSUES).

Use --health-check to verify the add-on is ACTIVE and version-compatible
before updating. -o json|yaml emits a machine-readable result/summary.`,
		Flags: []cli.Flag{
			// Update operations can legitimately run for minutes when --wait is
			// used, so the timeout default matches the legacy update-all command
			// rather than the 60s read-path default.
			// Not bound to REFRESH_TIMEOUT: that env sets short API/read timeouts
			// and must not cap a long-running update.
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Timeout for the update's API calls; with --wait, --wait-timeout per add-on is added on top (not read from REFRESH_TIMEOUT)", Value: 10 * time.Minute},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.StringFlag{Name: "addon", Aliases: []string{"a"}, Usage: "Add-on name (e.g., vpc-cni)"},
			&cli.StringFlag{Name: "version", Usage: "Target version or 'latest' (can be provided as third positional)", Value: "latest"},
			&cli.BoolFlag{Name: "all", Usage: "Update all add-ons in the cluster to their latest versions"},
			&cli.BoolFlag{Name: "health-check", Usage: "Verify the addon is ACTIVE before updating and validate version compatibility with the cluster"},
			&cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: "Preview without applying changes"},
			&cli.BoolFlag{Name: "parallel", Aliases: []string{"p"}, Usage: "(--all only) Update addons in parallel"},
			&cli.BoolFlag{Name: "wait", Usage: "Wait for each update to complete"},
			&cli.DurationFlag{Name: "wait-timeout", Usage: "Per-addon wait timeout (with --wait)", Value: 5 * time.Minute},
			&cli.BoolFlag{Name: "dependency-order", Usage: "(--all only) Update addons in dependency-safe order (vpc-cni -> coredns/kube-proxy -> others)"},
			&cli.StringSliceFlag{Name: "skip", Aliases: []string{"s"}, Usage: "(--all only) Skip specific addons (repeatable)"},
			&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "Accept a partial add-on name match without prompting (for unattended/CI use)"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain)", Value: "table"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Bool("all") {
				return runUpdateAll(ctx, cmd)
			}
			return runUpdate(ctx, cmd)
		},
	}
}

// updateAllHiddenCommand keeps `addon update-all` working as a hidden alias.
func updateAllHiddenCommand() *cli.Command {
	return &cli.Command{
		Name:      "update-all",
		Hidden:    true,
		Usage:     "Update all EKS add-ons to their latest versions",
		ArgsUsage: "[cluster]",
		Flags: []cli.Flag{
			// Not bound to REFRESH_TIMEOUT: that env sets short API/read timeouts
			// and must not cap a long-running update.
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Timeout for the update's API calls; with --wait, --wait-timeout per add-on is added on top (not read from REFRESH_TIMEOUT)", Value: 10 * time.Minute},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.BoolFlag{Name: "parallel", Aliases: []string{"p"}, Usage: "Update addons in parallel (faster but riskier)"},
			&cli.BoolFlag{Name: "wait", Usage: "Wait for each update to complete before proceeding"},
			&cli.DurationFlag{Name: "wait-timeout", Usage: "Timeout for waiting on each addon update", Value: 5 * time.Minute},
			&cli.BoolFlag{Name: "health-check", Usage: "Verify each addon is ACTIVE before updating and validate version compatibility"},
			&cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: "Preview changes without applying"},
			&cli.StringSliceFlag{Name: "skip", Aliases: []string{"s"}, Usage: "Skip specific addons (can be repeated)"},
			&cli.BoolFlag{Name: "dependency-order", Usage: "Update addons in dependency-safe order (vpc-cni → coredns/kube-proxy → others)"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain)", Value: "table"},
		},
		Action: runUpdateAll,
	}
}
