package nodegroup

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmd/changecmd"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
)

// scaleWaitTimeout is the default of --wait-timeout.
const scaleWaitTimeout = 5 * time.Minute

type scaleOptions struct {
	ios *cmdutil.IOStreams
	// deps builds the dependencies of the change. Calling it is the first
	// use of the network.
	deps func(ctx context.Context) (change.Deps, error)
	// resolve returns the cluster of the run, the way a change does:
	// never from the kubeconfig.
	resolve func(explicit string) (cmdutil.Target, error)
	// timeout returns --timeout, which the root command sets before the
	// command runs.
	timeout func() time.Duration

	cluster string
	// desired, min, and max are the values of the flags. complete keeps
	// only the ones that were given.
	desired, min, max int32
	output            cmdutil.Format
	change            cmdutil.ChangeFlags

	// nodegroup is the name from the argument, target the cluster, and
	// sizes the sizes that were given, all set by complete.
	nodegroup string
	target    cmdutil.Target
	sizes     struct{ desired, min, max *int32 }
}

func newCmdScale(f *cmdutil.Factory) *cobra.Command {
	o := &scaleOptions{
		ios:     f.IOStreams,
		deps:    changecmd.Deps(f),
		resolve: func(explicit string) (cmdutil.Target, error) { return cmdutil.ResolveCluster(f, explicit, false) },
		timeout: func() time.Duration { return f.Global.Timeout },
	}
	cmd := &cobra.Command{
		Use:   "scale <nodegroup>",
		Short: "Change the sizes of a nodegroup",
		Long: `Change the desired, minimum, and maximum size of one managed nodegroup.
The name is exact. A size that is not given keeps its value.

The sizes are checked before anything is sent: against each other, and
against the current sizes of the nodegroup for the ones that were not
given. Sizes that cannot hold are an error. A lone --min or --max that
excludes the current desired size says to pass --desired too. A scale to
the sizes that the nodegroup already has sends nothing.

Before any change the command reads the whole cluster. It refuses while
the cluster or anything else in it is changing, and it leaves a nodegroup
alone that is already changing.

A scale that lowers the desired size removes nodes, and EKS does not wait
for PodDisruptionBudgets when it does. So the health gate reads the nodes
and the budgets from the Kubernetes API, and stops a scale-down that can
take a budget below what it allows. --force goes past that, with a
warning. Without Kubernetes access the command asks before it goes on.
Any other scale checks the EC2 vCPU quota, and a scale-up warns when an
availability zone of the nodegroup does not offer one of its instance
types.

The command asks once before it starts. --yes skips the question, and is
required with -o json|yaml and without a terminal. It waits until EKS
reports the update done and the nodegroup is ACTIVE at the new sizes;
--no-wait returns once AWS accepted the change.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context. It never comes from the kubeconfig.

` + changecmd.ExitCodeHelp(map[int]string{
			1: "The update failed, the nodegroup is not ACTIVE at the new sizes\n     after it, or the sizes cannot hold. A scale has no verification\n     after it, so it never exits 5.",
		}),
		Example: `  # Add a node
  refresh nodegroup scale web -c prod --desired 4

  # See what would change, and what the health gate says
  refresh nodegroup scale web -c prod --desired 2 --dry-run

  # Raise the limit and the node count together, without a prompt
  refresh nodegroup scale web -c prod --desired 8 --max 10 --yes

  # Change only the bounds, and do not wait
  refresh nodegroup scale web -c prod --min 2 --max 12 --yes --no-wait`,
		Args:              cobra.MatchAll(cobra.ExactArgs(1), cmdutil.NoEmptyArgs),
		ValidArgsFunction: fleetcmd.CompleteNodegroups(f, &o.cluster),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.complete(cmd, args); err != nil {
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
	cmdutil.AddChangeClusterFlag(cmd, &o.cluster)
	fleetcmd.RegisterClusterFlagCompletion(cmd, f)
	cmd.Flags().Int32Var(&o.desired, "desired", 0, "Desired number of nodes")
	cmd.Flags().Int32Var(&o.min, "min", 0, "Minimum number of nodes")
	cmd.Flags().Int32Var(&o.max, "max", 0, "Maximum number of nodes")
	cmdutil.AddChangeFlags(cmd, &o.change, scaleWaitTimeout, "the scale", true, true)
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *scaleOptions) complete(cmd *cobra.Command, args []string) error {
	o.nodegroup = args[0]
	// A flag that was not given keeps the size of the nodegroup, which is
	// not the same as a size of 0.
	for _, s := range []struct {
		flag  string
		value *int32
		size  **int32
	}{{"desired", &o.desired, &o.sizes.desired}, {"min", &o.min, &o.sizes.min}, {"max", &o.max, &o.sizes.max}} {
		if cmd.Flags().Changed(s.flag) {
			*s.size = s.value
		}
	}
	target, err := o.resolve(o.cluster)
	o.target = target
	return err
}

func (o *scaleOptions) validate() error {
	if err := awsx.CheckNodegroupName(o.nodegroup); err != nil {
		return err
	}
	if err := change.ValidateSizes(o.sizes.desired, o.sizes.min, o.sizes.max); err != nil {
		return err
	}
	return o.change.Validate()
}

// request returns the change that the flags describe.
func (o *scaleOptions) request() change.ScaleRequest {
	return change.ScaleRequest{
		Common:    changecmd.Common(&o.change, o.timeout()),
		Target:    change.Target{Cluster: o.target.Cluster},
		Nodegroup: o.nodegroup,
		Desired:   o.sizes.desired,
		Min:       o.sizes.min,
		Max:       o.sizes.max,
	}
}

func (o *scaleOptions) run(ctx context.Context) error {
	d, err := o.deps(ctx)
	if err != nil {
		return err
	}
	req := o.request()
	if o.change.DryRun {
		return changecmd.PrintPlan(ctx, o.ios, o.output, d, req, o.timeout(), nil)
	}
	return changecmd.RunChange(ctx, o.ios, o.output, d, req, changecmd.NewRenderer(o.ios, o.change.Quiet))
}
