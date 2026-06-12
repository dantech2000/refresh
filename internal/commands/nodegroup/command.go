// Package nodegroup provides CLI commands for EKS nodegroup operations.
package nodegroup

import (
	"time"

	"github.com/urfave/cli/v3"

	appconfig "github.com/dantech2000/refresh/internal/config"
)

// Command returns the nodegroup command group with list, describe, scale, and
// update subcommands.
func Command() *cli.Command {
	return &cli.Command{
		Name:    "nodegroup",
		Aliases: []string{"ng"},
		Usage:   "Nodegroup operations (list, get, scale, update)",
		Description: `Inspect and operate on a cluster's managed nodegroups: list them with AMI
freshness, describe one in depth, scale desired/min/max size (with optional PDB
and health gating), and update (roll) nodegroups to the latest recommended AMI
with pre-flight health checks and live monitoring.`,
		Commands: []*cli.Command{
			listCommand(),
			describeCommand(),
			scaleCommand(),
			updateAMICommand(),
		},
	}
}

func listCommand() *cli.Command {
	return &cli.Command{
		Name:      "list",
		Usage:     "List nodegroups in a cluster with AMI status",
		ArgsUsage: "[cluster]",
		Description: `List the managed nodegroups in a cluster with their status, instance type,
node counts, and AMI freshness (whether each is on the latest recommended AMI).

Filter with repeatable --filter key=value (keys: name, status, instanceType,
amiStatus); sort with --sort and --desc. -o plain emits uncolored TSV for
grep/awk; -o json|yaml emit structured output. Use --watch to redraw on the
--watch-interval (top-style on a terminal, appended when piped) until Ctrl+C.

  refresh nodegroup list my-cluster --filter amiStatus=outdated
  refresh nodegroup list my-cluster -o plain | awk '{print $1}'
  refresh nodegroup list my-cluster --watch`,
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout (e.g. 60s, 2m)", Value: appconfig.DefaultTimeout, Sources: cli.EnvVars("REFRESH_TIMEOUT")},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain)", Value: "table"},
			&cli.StringFlag{Name: "sort", Usage: "Sort by field: name,status,instance,nodes", Value: "name"},
			&cli.BoolFlag{Name: "desc", Usage: "Sort descending"},
			&cli.StringSliceFlag{Name: "filter", Aliases: []string{"f"}, Usage: "Filter nodegroups (key=value; keys: name, status, instanceType, amiStatus)"},
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
		Usage:     "Describe a nodegroup with AMI status and optional instances/workloads info",
		ArgsUsage: "[cluster] [nodegroup]",
		Description: `Show detailed information for one nodegroup: scaling config, instance
type(s), AMI/release version and freshness, and (optionally) per-instance and
workload placement details. The nodegroup name may be the second positional or
--nodegroup.

  refresh nodegroup describe my-cluster ng-default
  refresh nodegroup describe my-cluster ng-default --show-instances --show-workloads`,
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout (e.g. 60s, 2m)", Value: appconfig.DefaultTimeout, Sources: cli.EnvVars("REFRESH_TIMEOUT")},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name"},
			&cli.StringFlag{Name: "nodegroup", Aliases: []string{"n"}, Usage: "Nodegroup name (can be provided as second positional)"},
			&cli.BoolFlag{Name: "show-instances", Aliases: []string{"I"}, Usage: "Include EC2 instance details"},
			&cli.BoolFlag{Name: "show-workloads", Aliases: []string{"W"}, Usage: "Include workload/pod placement info"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain)", Value: "table"},
		},
		Action: runDescribe,
	}
}

func scaleCommand() *cli.Command {
	return &cli.Command{
		Name:      "scale",
		Usage:     "Scale a nodegroup's desired/min/max size with optional health checks",
		ArgsUsage: "[cluster]",
		Description: `Change a managed nodegroup's desired/min/max size. Any subset of
--desired/--min/--max may be set; unspecified bounds are left unchanged.

--check-pdbs validates Pod Disruption Budgets before scaling down so you don't
strand workloads; --health-check validates cluster health before and after;
--dry-run previews the impact without executing; --wait blocks until the
operation settles.

  refresh nodegroup scale my-cluster -n ng-default --desired 5
  refresh nodegroup scale my-cluster -n ng-default --desired 2 --check-pdbs --wait`,
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout (e.g. 60s, 2m)", Value: appconfig.DefaultTimeout, Sources: cli.EnvVars("REFRESH_TIMEOUT")},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name"},
			&cli.StringFlag{Name: "nodegroup", Aliases: []string{"n"}, Usage: "Nodegroup name", Required: true},
			&cli.IntFlag{Name: "desired", Usage: "Desired node count"},
			&cli.IntFlag{Name: "min", Usage: "Minimum node count"},
			&cli.IntFlag{Name: "max", Usage: "Maximum node count"},
			&cli.BoolFlag{Name: "health-check", Usage: "Validate cluster health before and after scaling"},
			&cli.BoolFlag{Name: "check-pdbs", Usage: "Validate Pod Disruption Budgets before scaling down"},
			&cli.BoolFlag{Name: "wait", Usage: "Wait for scaling operation to complete"},
			&cli.DurationFlag{Name: "op-timeout", Usage: "Scaling operation timeout", Value: 5 * time.Minute},
			&cli.StringFlag{Name: "kubeconfig", Usage: "Path to the kubeconfig for workload/PDB health checks (defaults to $KUBECONFIG, then ~/.kube/config)"},
			&cli.BoolFlag{Name: "dry-run", Usage: "Preview scaling impact without executing"},
		},
		Action: runScale,
	}
}

func updateAMICommand() *cli.Command {
	return &cli.Command{
		Name:      "update",
		Aliases:   []string{"update-ami"},
		Usage:     "Update the AMI for all or a specific node group (rolling by default)",
		ArgsUsage: "[cluster] [nodegroup]",
		Description: `Roll managed nodegroups to the latest recommended AMI, with pre-flight
health gates and live monitoring.

Custom-AMI nodegroups (AmiType=CUSTOM) are skipped with guidance: their AMI is
managed via the launch template, so publish a new LT version to roll them.

Fleet mode (--all-clusters) discovers clusters across regions (scope with -r)
and rolls them serially with one batch confirmation, an aggregate summary, and a
worst-outcome exit code:
   refresh nodegroup update --all-clusters --dry-run        # fleet-wide plan
   refresh nodegroup update --all-clusters -r us-east-1 --yes

Unattended / CI use:
   --yes              skip confirmation prompts (multi-match selection, warnings)
   --require-healthy  treat warn-level health findings as a hard stop
   -o json            print a JSON run summary (started/skipped/custom/failed)
   Without a TTY and without --yes, a prompt-requiring run fails fast.

Exit codes:
   0  success            2  health warnings (--health-only / --require-healthy)
   3  health blocked     4  one or more nodegroup updates failed to start

Example (cron): refresh nodegroup update -c prod --yes --require-healthy -o json`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or partial name pattern (overrides kubeconfig)", Sources: cli.EnvVars("EKS_CLUSTER_NAME")},
			&cli.StringFlag{Name: "nodegroup", Aliases: []string{"n"}, Usage: "Nodegroup name or partial name pattern (if not set, update all)"},
			&cli.BoolFlag{Name: "all-clusters", Usage: "Fleet mode: roll matching nodegroups across all discovered clusters (serial). Scope with -r."},
			&cli.StringSliceFlag{Name: "region", Aliases: []string{"r"}, Usage: "Region(s) for --all-clusters discovery (default: partition EKS regions / REFRESH_EKS_REGIONS)"},
			&cli.BoolFlag{Name: "force", Aliases: []string{"f"}, Usage: "Force update if possible"},
			&cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: "Preview changes without executing them"},
			&cli.BoolFlag{Name: "no-wait", Usage: "Don't wait for update completion (original behavior)"},
			&cli.BoolFlag{Name: "quiet", Aliases: []string{"q"}, Usage: "Minimal output mode"},
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Maximum time to wait for update completion", Value: 40 * time.Minute},
			&cli.DurationFlag{Name: "poll-interval", Aliases: []string{"p"}, Usage: "Polling interval for checking update status", Value: 15 * time.Second},
			&cli.BoolFlag{Name: "skip-health-check", Aliases: []string{"s"}, Usage: "Skip pre-flight health validation"},
			&cli.BoolFlag{Name: "health-only", Usage: "Run health check only, don't update (exit code: 0=pass, 2=warn, 3=block)"},
			&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "Assume yes: skip confirmation prompts (multi-match selection, warn-level health) for unattended/CI use"},
			&cli.BoolFlag{Name: "require-healthy", Usage: "Treat warn-level health findings as a hard stop (exit 2) instead of prompting"},
			&cli.BoolFlag{Name: "skip-verify", Usage: "Skip post-roll verification (nodes ACTIVE, no new stuck pods)"},
			&cli.BoolFlag{Name: "changelog", Usage: "In dry-run, print full amazon-eks-ami release notes between the current and target AMI"},
			&cli.StringFlag{Name: "kubeconfig", Usage: "Path to the kubeconfig for workload/PDB health checks (defaults to $KUBECONFIG, then ~/.kube/config)"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format: health results with --health-only; a JSON run summary with -o json", Value: "table"},
		},
		Action: runUpdateAMI,
	}
}
