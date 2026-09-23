// Package nodegroup provides CLI commands for EKS nodegroup operations.
package nodegroup

import (
	"time"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
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
  refresh nodegroup list my-cluster -o plain | awk -F'\t' 'NR>1 {print $1}'
  refresh nodegroup list my-cluster --watch`,
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout (e.g. 60s, 2m)", Value: appconfig.DefaultTimeout, Sources: cli.EnvVars("REFRESH_TIMEOUT")},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain)", Value: "table"},
			&cli.StringFlag{Name: "sort", Usage: "Sort by field: name,status,instance,nodes", Value: "name"},
			&cli.BoolFlag{Name: "desc", Usage: "Sort descending"},
			&cli.StringSliceFlag{Name: "filter", Aliases: []string{"f"}, Usage: "Filter nodegroups (key=value; keys: name, status, instanceType, amiStatus)"},
			&cli.BoolFlag{Name: "check-readiness", Aliases: []string{"R"}, Usage: "Measure real Kubernetes node readiness (Ready/desired) via the cluster API; without it NODES shows desired count only"},
			&cli.StringFlag{Name: "kubeconfig", Usage: "Path to the kubeconfig for --check-readiness (defaults to $KUBECONFIG, then ~/.kube/config)"},
			runner.KubeContextFlag(),
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

--check-pdbs refuses a scale-down (exit 1, before any change) when it could
remove more of a Pod Disruption Budget's pods than the PDB allows. EKS does not
honor PDBs when a scaling change removes nodes, and the Auto Scaling group
picks which nodes go, so the gate assumes the removed nodes are the ones that
hold the most of the PDB's pods. --force scales down anyway and prints the blockers as a
warning. --health-check validates cluster health before and after; --dry-run
previews the impact (and the PDB verdict) without executing; --wait blocks
until the operation settles.

  refresh nodegroup scale my-cluster -n ng-default --desired 5
  refresh nodegroup scale my-cluster -n ng-default --desired 2 --check-pdbs --wait
  refresh nodegroup scale my-cluster -n ng-default --desired 1 --check-pdbs --force`,
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout (e.g. 60s, 2m)", Value: appconfig.DefaultTimeout, Sources: cli.EnvVars("REFRESH_TIMEOUT")},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name"},
			&cli.StringFlag{Name: "nodegroup", Aliases: []string{"n"}, Usage: "Nodegroup name", Required: true},
			&cli.IntFlag{Name: "desired", Usage: "Desired node count"},
			&cli.IntFlag{Name: "min", Usage: "Minimum node count"},
			&cli.IntFlag{Name: "max", Usage: "Maximum node count"},
			&cli.BoolFlag{Name: "health-check", Usage: "Validate cluster health before and after scaling"},
			&cli.BoolFlag{Name: "check-pdbs", Usage: "Refuse a scale-down that could remove more of a Pod Disruption Budget's pods than it allows"},
			&cli.BoolFlag{Name: "force", Usage: "With --check-pdbs, scale down even if PDBs would block it (the blockers are printed as a warning)"},
			&cli.BoolFlag{Name: "wait", Usage: "Wait for scaling operation to complete"},
			&cli.DurationFlag{Name: "op-timeout", Usage: "Scaling operation timeout for --wait (added on top of --timeout; 0 = no limit)", Value: 5 * time.Minute},
			&cli.StringFlag{Name: "kubeconfig", Usage: "Path to the kubeconfig for workload/PDB health checks (defaults to $KUBECONFIG, then ~/.kube/config)"},
			runner.KubeContextFlag(),
			&cli.BoolFlag{Name: "dry-run", Usage: "Preview scaling impact without executing"},
		},
		Action: runScale,
	}
}

func updateAMICommand() *cli.Command {
	return &cli.Command{
		Name:      "update",
		Aliases:   []string{"update-ami"},
		Usage:     "Update the AMI for all or a specific nodegroup (rolling by default)",
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
Fleet mode takes no positional args and rejects --cluster and --kube-context
(each cluster's kubeconfig context is matched by endpoint). A region that
can't be listed is reported and makes the run exit 4. The default sweep skips
regions these credentials can't use (SCP-denied or not enabled) with a note.

Unattended / CI use:
   --yes              skip confirmation prompts (multi-match selection, warnings)
   --require-healthy  treat warn-level health findings as a hard stop
   -o json|yaml       print one document on stdout: the run summary
                      (started/skipped/custom/failed), the dry-run plan, or
                      the --health-only verdict; notices go to stderr
   Without a TTY, or with -o json|yaml, a run that needs a prompt fails fast
   unless --yes is given.

Exit codes:
   0  success            1  error, interrupt, or monitoring timeout
   2  health warnings (--health-only / --require-healthy)
   3  health blocked     4  one or more nodegroup updates failed to start
   5  post-roll verification found issues

Example (cron): refresh nodegroup update -c prod --yes --require-healthy -o json`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or partial name pattern (overrides the active context; kubeconfig is not used). Falls back to EKS_CLUSTER_NAME unless a positional is clearly the cluster (with --nodegroup, or two positionals)"},
			&cli.StringFlag{Name: "nodegroup", Aliases: []string{"n"}, Usage: "Nodegroup name or partial name pattern (if not set, update all)"},
			&cli.BoolFlag{Name: "all-clusters", Usage: "Fleet mode: roll matching nodegroups across all discovered clusters (serial). Scope with -r."},
			&cli.StringSliceFlag{Name: "region", Aliases: []string{"r"}, Usage: "Region(s) for --all-clusters discovery (default: partition EKS regions / REFRESH_EKS_REGIONS)"},
			&cli.BoolFlag{Name: "force", Aliases: []string{"f"}, Usage: "Force update if possible"},
			&cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: "Preview changes without executing them"},
			&cli.BoolFlag{Name: "no-wait", Usage: "Don't wait for update completion (original behavior)"},
			&cli.BoolFlag{Name: "quiet", Aliases: []string{"q"}, Usage: "Minimal output mode"},
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Maximum time to wait for update completion (per cluster with --all-clusters; 0 = no limit)", Value: appconfig.DefaultUpdateTimeout},
			&cli.DurationFlag{Name: "poll-interval", Aliases: []string{"p"}, Usage: "Polling interval for checking update status", Value: appconfig.DefaultPollInterval},
			&cli.BoolFlag{Name: "skip-health-check", Aliases: []string{"s"}, Usage: "Skip pre-flight health validation"},
			&cli.BoolFlag{Name: "health-only", Usage: "Run health check only, don't update (exit code: 0=pass, 2=warn, 3=block)"},
			&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "Assume yes: skip confirmation prompts (multi-match selection, warn-level health) for unattended/CI use"},
			&cli.BoolFlag{Name: "require-healthy", Usage: "Treat warn-level health findings as a hard stop (exit 2) instead of prompting"},
			&cli.BoolFlag{Name: "skip-verify", Usage: "Skip post-roll verification (nodes ACTIVE, no new stuck pods)"},
			&cli.BoolFlag{Name: "changelog", Usage: "In dry-run, print full amazon-eks-ami release notes between the current and target AMI"},
			&cli.StringFlag{Name: "kubeconfig", Usage: "Path to the kubeconfig for workload/PDB health checks (defaults to $KUBECONFIG, then ~/.kube/config)"},
			runner.KubeContextFlag(),
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml). json/yaml print one document: the run summary, the --dry-run plan, or the --health-only verdict", Value: "table"},
			// The real-time per-node roll panel (driven from live Kubernetes
			// state) is the DEFAULT for a single-nodegroup roll when stdout is
			// a color terminal, falling back to standard monitoring when the
			// cluster API isn't reachable. --live forces it (also when piped,
			// as throttled snapshots) and reports the fallback reason. EKS
			// DescribeUpdate stays authoritative for the result. (REF-126)
			&cli.BoolFlag{Name: "live", Usage: "Force the live per-node roll view, also when stdout is not a color terminal (appends a snapshot at most every 15s, only on change), and report why if the cluster API can't be reached. The panel is already the default for a single-nodegroup roll on a color terminal"},
			// --simulate drives the live node-roll panel from a scripted observer
			// (no AWS, no cluster) — for demos, asciinema, and manual QA of the
			// live view. Hidden: it's a dev/demo aid, not a real operation.
			&cli.BoolFlag{Name: "simulate", Hidden: true, Usage: "Demo the live node-roll panel with simulated data (no AWS)"},
		},
		Action: runUpdateAMI,
	}
}
