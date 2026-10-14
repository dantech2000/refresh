package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmd/changecmd"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
)

// engineWaitTimeout is the default of --wait-timeout for an upgrade and
// for a rollback: one budget for the whole run.
const engineWaitTimeout = 4 * time.Hour

type upgradeOptions struct {
	ios *cmdutil.IOStreams
	// deps builds the dependencies of the change. Calling it is the first
	// use of the network.
	deps func(ctx context.Context) (change.Deps, error)
	// resolve returns the cluster of the run, the way a change does:
	// never from the kubeconfig.
	resolve func(explicit string) (cmdutil.Target, error)
	// timeout returns --timeout, and followUp the words around a command
	// that a notice names. The root command sets the global flags before
	// the command runs.
	timeout  func() time.Duration
	followUp func() change.FollowUp

	to             string
	only           []string
	nodegroups     []string
	skipAddons     []string
	skipNodegroups []string
	skipInsights   bool
	skipVerify     bool
	output         cmdutil.Format
	change         cmdutil.ChangeFlags

	// target, version, and parts are set by complete.
	target  cmdutil.Target
	version fleet.Version
	parts   []change.Part
}

// followUp returns the words that put a command of a notice in the
// account, the region, and the Kubernetes context of the run.
func followUp(f *cmdutil.Factory) func() change.FollowUp {
	return func() change.FollowUp {
		prefix := []string{"refresh"}
		if f.Global.Profile != "" {
			prefix = append(prefix, "--profile", f.Global.Profile)
		}
		if len(f.Global.Regions) == 1 {
			prefix = append(prefix, "--region", f.Global.Regions[0])
		}
		var suffix []string
		if f.Global.Kubeconfig != "" {
			suffix = append(suffix, "--kubeconfig", f.Global.Kubeconfig)
		}
		if f.Global.KubeContext != "" {
			suffix = append(suffix, "--kube-context", f.Global.KubeContext)
		}
		return change.FollowUp{Prefix: strings.Join(prefix, " "), Suffix: strings.Join(suffix, " ")}
	}
}

// resumeFlags returns the flags of the run that the command of a stopped
// run repeats and that the request does not hold: a --wait-timeout that
// is not the default.
func resumeFlags(flags *cmdutil.ChangeFlags) string {
	if flags.WaitTimeout == engineWaitTimeout {
		return ""
	}
	return "--wait-timeout " + flags.WaitTimeout.String()
}

func newCmdUpgrade(f *cmdutil.Factory) *cobra.Command {
	o := &upgradeOptions{
		ios:      f.IOStreams,
		deps:     changecmd.Deps(f),
		resolve:  func(explicit string) (cmdutil.Target, error) { return cmdutil.ResolveCluster(f, explicit, false) },
		timeout:  func() time.Duration { return f.Global.Timeout },
		followUp: followUp(f),
	}
	cmd := &cobra.Command{
		Use:   "upgrade [cluster]",
		Short: "Upgrade a cluster to a Kubernetes version",
		Long: `Upgrade a cluster to a Kubernetes version, one minor version at a time.
For each minor version the command moves the control plane, then updates
the add-ons that the new control plane cannot run, then rolls every managed
nodegroup, one after another, and then updates the other add-ons.

The plan comes from the live state of the cluster, and there is no state
file. Run the same command again after a run that stopped: it goes on with
what is left. --to with the version that the control plane already runs
only catches up the add-ons and the nodegroups.

Before any change the command reads the whole cluster and judges it. It
asks EKS to refresh the cluster insights and waits for that, up to 5
minutes: an ERROR or an UNKNOWN insight for the next version blocks, and
so does a nodegroup that the new version would put past the kubelet skew.
That refresh is the one request that a blocked run sends. A dry run sends
none. The health gate then runs for every nodegroup that the plan would
roll, so a PodDisruptionBudget that would stop a drain is known before the
control plane moves. Each gate runs again right before its own phase.

A phase that fails ends the run with exit code 1. The message names the
phases that completed and the command that goes on from there. The command
refuses while someone else changes the cluster, and it waits for a change
of its own that is still running.

On a terminal the command asks before each phase. --yes skips the
questions, and is required with -o json|yaml and without a terminal.
--wait-timeout bounds the whole run, and a question does not count.

--only limits the run to the parts that it names: control-plane, addons,
nodegroups. -n rolls only the nodegroups that it names. --skip-addon and
--skip-nodegroup leave something alone. A name is exact.

The cluster is the argument, then REFRESH_CLUSTER, then the active refresh
context. It never comes from the kubeconfig.

` + changecmd.ExitCodeHelp(map[int]string{
			1: "A phase failed. The phases before it stay done.",
			3: "The insights or the kubelet skew block the next version.",
			5: "A nodegroup was rolled and the verification found an issue, or\n     an add-on was updated and is not healthy.",
		}),
		Example: `  # See what an upgrade would do
  refresh cluster upgrade prod --to 1.33 --dry-run

  # Upgrade, with a question before each phase
  refresh cluster upgrade prod --to 1.33

  # Upgrade two minor versions without a prompt
  refresh cluster upgrade prod --to 1.34 --yes

  # Move only the control plane
  refresh cluster upgrade prod --to 1.33 --only control-plane

  # Roll the nodegroups later, and leave one alone
  refresh cluster upgrade prod --to 1.33 --only nodegroups --skip-nodegroup gpu`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: fleetcmd.CompleteClusters(f),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.complete(args); err != nil {
				return err
			}
			if err := o.validate(); err != nil {
				return err
			}
			if err := cmdutil.RequireYes(o.ios, o.output, o.change.Yes, o.change.DryRun); err != nil {
				return err
			}
			return o.run(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&o.to, "to", "", "Kubernetes version to reach, such as 1.33 (required)")
	cmd.Flags().StringSliceVar(&o.only, "only", nil, "Limit the run to these parts: control-plane, addons, nodegroups")
	cmd.Flags().StringSliceVarP(&o.nodegroups, "nodegroup", "n", nil, "Roll only these nodegroups, by exact name")
	cmd.Flags().StringSliceVar(&o.skipAddons, "skip-addon", nil, "Leave these add-ons alone, by exact name")
	cmd.Flags().StringSliceVar(&o.skipNodegroups, "skip-nodegroup", nil, "Leave these nodegroups alone, by exact name")
	cmd.Flags().BoolVar(&o.skipInsights, "skip-insights-check", false, "Do not refresh or read the cluster insights (not recommended)")
	cmd.Flags().BoolVar(&o.skipVerify, "skip-verify", false, "Skip the verification after each nodegroup roll")
	cmdutil.AddChangeFlags(cmd, &o.change, engineWaitTimeout, "the whole upgrade", false, true)
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *upgradeOptions) complete(args []string) error {
	explicit := ""
	if len(args) > 0 {
		explicit = args[0]
	}
	target, err := o.resolve(explicit)
	if err != nil {
		return err
	}
	o.target = target
	if strings.TrimSpace(o.to) == "" {
		return errors.New("--to is required: the Kubernetes version to reach, such as --to 1.33")
	}
	if o.version, err = fleet.ParseVersion(strings.TrimSpace(o.to)); err != nil {
		return fmt.Errorf("invalid --to %q: want a Kubernetes minor version, such as 1.33", o.to)
	}
	o.parts, err = change.ParseParts(o.only)
	return err
}

func (o *upgradeOptions) validate() error {
	return o.change.Validate()
}

// request returns the change that the flags describe. Without --yes the
// run asks before each phase.
func (o *upgradeOptions) request() change.UpgradeRequest {
	req := change.UpgradeRequest{
		Common:         changecmd.Common(&o.change, o.timeout()),
		Target:         change.Target{Cluster: o.target.Cluster},
		To:             o.version,
		Only:           o.parts,
		Nodegroups:     o.nodegroups,
		SkipAddons:     o.skipAddons,
		SkipNodegroups: o.skipNodegroups,
		SkipInsights:   o.skipInsights,
		ConfirmPhases:  !o.change.Yes,
	}
	req.SkipVerify = o.skipVerify
	// One nodegroup rolls at a time, so a terminal shows its nodes.
	req.Nodes = nodesOnTerminal(o.ios, o.output, &o.change)
	return req
}

// nodesOnTerminal reports whether a run shows the nodes of each roll: a
// table on a terminal, without --quiet and without --dry-run.
func nodesOnTerminal(ios *cmdutil.IOStreams, output cmdutil.Format, flags *cmdutil.ChangeFlags) bool {
	return output == cmdutil.FormatTable && ios.ErrTTY && !flags.Quiet && !flags.DryRun
}

func (o *upgradeOptions) run(ctx context.Context) error {
	d, err := o.deps(ctx)
	if err != nil {
		return err
	}
	d.FollowUp = o.followUp()
	d.FollowUp.Resume = resumeFlags(&o.change)
	req := o.request()
	if o.change.DryRun {
		return changecmd.PrintPlan(ctx, o.ios, o.output, d, req, o.timeout(), nil)
	}
	return changecmd.RunChange(ctx, o.ios, o.output, d, req, engineRenderer(o.ios, req.Nodes, o.change.Quiet))
}

// engineRenderer returns the renderer of an upgrade or a rollback: the
// one with the panel of a roll when the run shows the nodes.
func engineRenderer(ios *cmdutil.IOStreams, nodes, quiet bool) changecmd.Renderer {
	if nodes {
		return changecmd.NewRollRenderer(ios)
	}
	return changecmd.NewRenderer(ios, quiet)
}
