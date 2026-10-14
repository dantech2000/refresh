package cluster

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmd/changecmd"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
)

type rollbackOptions struct {
	ios *cmdutil.IOStreams
	// deps builds the dependencies of the change. Calling it is the first
	// use of the network.
	deps func(ctx context.Context) (change.Deps, error)
	// resolve returns the cluster of the run, the way a change does:
	// never from the kubeconfig.
	resolve  func(explicit string) (cmdutil.Target, error)
	timeout  func() time.Duration
	followUp func() change.FollowUp

	skipNodegroups  []string
	skipInsights    bool
	skipVerify      bool
	rollbackTimeout time.Duration
	output          cmdutil.Format
	change          cmdutil.ChangeFlags

	// target is set by complete.
	target cmdutil.Target
}

func newCmdRollback(f *cmdutil.Factory) *cobra.Command {
	o := &rollbackOptions{
		ios:      f.IOStreams,
		deps:     changecmd.Deps(f),
		resolve:  func(explicit string) (cmdutil.Target, error) { return cmdutil.ResolveCluster(f, explicit, false) },
		timeout:  func() time.Duration { return f.Global.Timeout },
		followUp: followUp(f),
	}
	cmd := &cobra.Command{
		Use:   "rollback [cluster]",
		Short: "Roll a cluster back to the Kubernetes version before its upgrade",
		Long: `Roll a cluster back one minor version, within 7 days of an in-place
upgrade. EKS rolls back only the control plane, so the command first rolls
the managed nodegroups back, one after another, then downgrades the add-ons
that the older version cannot run, and then rolls the control plane back.

The plan comes from the live state of the cluster and its update history.
Run the same command again after a run that stopped: it goes on with what
is left. A cluster that was created at its version, and an upgrade that is
more than 7 days old, cannot roll back.

Before any change the health gate runs for every nodegroup that the plan
would roll, and it runs again right before each roll. The control plane
does not roll back while a managed nodegroup is above the target: a
skipped nodegroup and one with a custom AMI are yours to move first.
Self-managed nodes and Fargate pods are not rolled back by EKS or by
refresh.

A phase that fails ends the run with exit code 1. The message names the
phases that completed and the command that goes on from there.

The command asks once, with the whole order in the question. --yes skips
the question, and is required with -o json|yaml and without a terminal.
When the cluster changed while the question was open, the command asks
again about what changed. It refuses, whatever the answer and the flags,
when the version to roll back to is no longer the one that was confirmed.

--skip-insights-check stops the rollback readiness insights from blocking,
and sends force to EKS, which then skips its own insight checks too.
--rollback-timeout is the time that EKS gives the control-plane rollback:
whole minutes from 2h to 168h. 0 leaves the default of EKS.

The cluster is the argument, then REFRESH_CLUSTER, then the active refresh
context. It never comes from the kubeconfig.

` + changecmd.ExitCodeHelp(map[int]string{
			1: "A phase failed. The phases before it stay done.",
			3: "The cluster cannot roll back: no upgrade in the last 7 days, a\n     blocking insight, or a nodegroup above the target.",
			5: "A nodegroup was rolled and the verification found an issue, or\n     an add-on was downgraded and is not healthy.",
		}),
		Example: `  # See what a rollback would do
  refresh cluster rollback prod --dry-run

  # Roll back, after one question
  refresh cluster rollback prod

  # Roll back without a prompt, and give EKS three hours
  refresh cluster rollback prod --yes --rollback-timeout 3h`,
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
	cmd.Flags().StringSliceVar(&o.skipNodegroups, "skip-nodegroup", nil, "Leave these nodegroups alone, by exact name")
	cmd.Flags().BoolVar(&o.skipInsights, "skip-insights-check", false, "Roll back past blocking insights, and send force to EKS (not recommended)")
	cmd.Flags().BoolVar(&o.skipVerify, "skip-verify", false, "Skip the verification after each nodegroup roll")
	cmd.Flags().DurationVar(&o.rollbackTimeout, "rollback-timeout", 0, "Time that EKS gives the control-plane rollback: whole minutes from 2h to 168h; 0 leaves the default of EKS")
	cmdutil.AddChangeFlags(cmd, &o.change, engineWaitTimeout, "the whole rollback", false, true)
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *rollbackOptions) complete(args []string) error {
	explicit := ""
	if len(args) > 0 {
		explicit = args[0]
	}
	target, err := o.resolve(explicit)
	o.target = target
	return err
}

func (o *rollbackOptions) validate() error {
	if err := change.ValidateRollbackTimeout(o.rollbackTimeout); err != nil {
		return err
	}
	return o.change.Validate()
}

// request returns the change that the flags describe.
func (o *rollbackOptions) request() change.RollbackRequest {
	req := change.RollbackRequest{
		Common:          changecmd.Common(&o.change, o.timeout()),
		Target:          change.Target{Cluster: o.target.Cluster},
		SkipNodegroups:  o.skipNodegroups,
		SkipInsights:    o.skipInsights,
		RollbackTimeout: o.rollbackTimeout,
	}
	req.SkipVerify = o.skipVerify
	req.Nodes = nodesOnTerminal(o.ios, o.output, &o.change)
	return req
}

func (o *rollbackOptions) run(ctx context.Context) error {
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
