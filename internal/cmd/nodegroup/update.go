package nodegroup

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmd/changecmd"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/aminotes"
)

type updateOptions struct {
	f   *cmdutil.Factory
	ios *cmdutil.IOStreams

	cluster     string
	filters     cmdutil.Filters
	allClusters bool
	reroll      bool
	skipVerify  bool
	changelog   bool
	live        bool
	output      cmdutil.Format
	change      cmdutil.ChangeFlags

	// nodegroups are the names from the arguments, and target the
	// cluster, both set by complete.
	nodegroups []string
	target     cmdutil.Target
}

func newCmdUpdate(f *cmdutil.Factory) *cobra.Command {
	o := &updateOptions{f: f, ios: f.IOStreams}
	cmd := &cobra.Command{
		Use:   "update [nodegroup...]",
		Short: "Roll nodegroups onto the latest AMI",
		Long: `Roll managed nodegroups onto the latest recommended AMI of their own
Kubernetes version. EKS replaces the nodes: it drains each one and honours
PodDisruptionBudgets. The Kubernetes version of a nodegroup never changes
here; "cluster upgrade" does that.

With no name, every nodegroup of the cluster is a target. A name is exact.
-f selects by pattern, such as -f name=web-*, and the confirmation lists
the matches. A nodegroup that is on the latest AMI is left alone, unless
--reroll asks for a roll anyway. A nodegroup with a custom AMI is never
rolled: publish a new launch template version for it.

Before any roll the command reads the whole cluster. It refuses while the
cluster or anything else in it is changing, and it leaves a target alone
that is already changing. Then the health gate judges all the targets
together, before the first roll starts: the nodes, the capacity to take
the drained pods, the control plane, the system workloads, and the
PodDisruptionBudgets that would stop a drain, and the EC2 vCPU quota: a
roll whose likely surge of new nodes does not fit is blocked. --force goes
past a drain blocker and a quota surge, with a warning, and tells EKS to
force the drain. It does nothing else: it does not roll a nodegroup that
is on the latest AMI. Without Kubernetes access the command asks before it
goes on.

The command asks once before it starts. --yes skips the question, and is
required with -o json|yaml and without a terminal. It starts every roll,
waits for all of them, and then verifies: the nodegroups are ACTIVE, the
new nodes are Ready, and no new pod is stuck Pending: unschedulable, or
unable to pull its image or read its configuration. --no-wait returns
once AWS accepted the rolls, and --skip-verify leaves the verification
out.

--all-clusters rolls every cluster of the regions of -r, one cluster
after another, after one confirmation for all of them. Without -r it rolls
the clusters of the configured region alone. A cluster that is blocked or
fails does not stop the next one.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context. It never comes from the kubeconfig. --all-clusters
ignores all three.

` + changecmd.ExitCodeHelp(map[int]string{
			1: "Every roll that was sent failed.",
			4: "A roll failed beside one that was started.",
			5: "The rolls were applied and the verification found an issue.",
		}),
		Example: `  # Roll every nodegroup of a cluster that is behind
  refresh nodegroup update -c prod

  # See what would be rolled, with the AMI release notes
  refresh nodegroup update -c prod --dry-run --changelog

  # Roll two nodegroups without a prompt
  refresh nodegroup update web api -c prod --yes

  # Roll the nodegroups that match a pattern
  refresh nodegroup update -c prod -f name=web-*

  # Roll every cluster of two regions
  refresh nodegroup update --all-clusters -r us-east-1,us-west-2 --yes`,
		Args:              cobra.MatchAll(cobra.ArbitraryArgs, cmdutil.NoEmptyArgs),
		ValidArgsFunction: fleetcmd.CompleteNodegroupNames(f, &o.cluster),
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
	cmdutil.AddChangeClusterFlag(cmd, &o.cluster)
	fleetcmd.RegisterClusterFlagCompletion(cmd, f)
	cmdutil.AddFilterFlag(cmd, &o.filters, filterKeys...)
	cmd.Flags().BoolVar(&o.allClusters, "all-clusters", false, "Roll every cluster of the regions of -r, or of the configured region, one after another")
	cmd.Flags().BoolVar(&o.reroll, "reroll", false, "Also roll a nodegroup that is on the latest AMI")
	cmd.Flags().BoolVar(&o.skipVerify, "skip-verify", false, "Skip the verification after each nodegroup roll")
	cmd.Flags().BoolVar(&o.changelog, "changelog", false, "With --dry-run, show the AMI release notes of each roll")
	cmd.Flags().BoolVar(&o.live, "live", false, "Show the nodes of the roll as they drain and join, also without a terminal")
	cmdutil.AddChangeFlags(cmd, &o.change, cmdutil.ChangeFlagOptions{Wait: change.DefaultWait(change.NodegroupUpdate), WaitFor: "the rolls of a cluster", NoWait: true, Force: true})
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *updateOptions) complete(args []string) error {
	o.nodegroups = args
	// A fleet names no cluster: it ignores REFRESH_CLUSTER and the active
	// context. validate rejects -c with it.
	if o.allClusters {
		return nil
	}
	// A change never takes its cluster from the kubeconfig.
	var err error
	o.target, err = cmdutil.ResolveCluster(o.f, o.cluster, false)
	return err
}

func (o *updateOptions) validate() error {
	if err := cmdutil.CheckNames(awsx.CheckNodegroupName, o.nodegroups...); err != nil {
		return err
	}
	switch {
	case o.allClusters && o.cluster != "":
		return errors.New("--all-clusters and -c/--cluster cannot be used together: one names every cluster and the other names one")
	case o.allClusters && len(o.nodegroups) > 0:
		return errors.New("--all-clusters takes no nodegroup name: a name belongs to one cluster. Select with -f, such as -f name=web-*")
	case o.allClusters && o.f.Global.KubeContext != "":
		return errors.New("--all-clusters and --kube-context cannot be used together: each cluster has its own kubeconfig context, matched by its endpoint")
	case o.changelog && !o.change.DryRun:
		return errors.New("--changelog shows the release notes of a plan; pass --dry-run with it")
	case o.live && o.output != cmdutil.FormatTable:
		return fmt.Errorf("--live draws the nodes of the roll, which -o %s cannot hold; use -o table", o.output)
	case o.live && o.change.Quiet:
		return errors.New("--live and --quiet cannot be used together: one shows the progress and the other hides it")
	}
	return o.change.Validate()
}

// selector returns the selection of -f for the request, or nil without
// -f.
func (o *updateOptions) selector() *change.Selector {
	if len(o.filters) == 0 {
		return nil
	}
	filters := o.filters
	return &change.Selector{
		Text:  filters.String(),
		Match: func(n fleet.Nodegroup) bool { return filters.Match(filterValues(n)) },
	}
}

// nodes reports whether the run shows the nodes of a roll: with --live,
// and for a table on a terminal without --quiet.
func (o *updateOptions) nodes() bool {
	return o.live || (o.output == cmdutil.FormatTable && o.ios.ErrTTY && !o.change.Quiet)
}

// request returns the change that the flags describe. regions are the
// regions of a fleet.
func (o *updateOptions) request(regions fleet.Regions) change.RollRequest {
	// A fleet has no budget for its whole plan: each cluster has its own.
	req := change.RollRequest{
		Common:     changecmd.Common(&o.change, o.f.Global.Timeout),
		Target:     change.Target{Cluster: o.target.Cluster},
		Nodegroups: o.nodegroups,
		Select:     o.selector(),
		Reroll:     o.reroll,
	}
	req.SkipVerify = o.skipVerify
	req.Nodes = o.nodes() && !o.change.DryRun
	if o.allClusters {
		req.Fleet = &change.FleetScope{Regions: regions}
	}
	return req
}

func (o *updateOptions) run(ctx context.Context) error {
	deps, timeout := changecmd.Deps, o.f.Global.Timeout
	if o.allClusters {
		deps, timeout = changecmd.FleetDeps, 0
	}
	d, err := deps(ctx, o.f)
	if err != nil {
		return err
	}
	var regions fleet.Regions
	if o.allClusters {
		// A fleet rolls where -r points, or in the home region alone:
		// never in the default sweep of every region.
		if regions, err = fleetcmd.ChangeRegions(&o.f.Global, d.AWS.Region()); err != nil {
			return err
		}
	}
	req := o.request(regions)
	if o.change.DryRun {
		var after func(ctx context.Context, plan *change.Plan)
		if o.changelog {
			after = o.printChangelog
		}
		return changecmd.PrintPlan(ctx, o.ios, o.output, d, req, timeout, after)
	}
	var r changecmd.Renderer = changecmd.NewRenderer(o.ios, o.change.Quiet)
	if req.Nodes {
		r = changecmd.NewRollRenderer(o.ios)
	}
	return changecmd.RunChange(ctx, o.ios, o.output, d, req, r)
}

// printChangelog prints the AMI release notes of the rolls of a plan on
// stderr, after the plan. The notes come from the web, and the plan does
// not depend on them: what cannot be fetched is a warning.
func (o *updateOptions) printChangelog(ctx context.Context, plan *change.Plan) {
	var targets []aminotes.Target
	for i := range plan.Clusters {
		cp := &plan.Clusters[i]
		for _, s := range cp.Steps() {
			if n, ok := cp.Nodegroup(s.Ref.Name); ok && s.Action == change.Submit {
				targets = append(targets, aminotes.Target{Nodegroup: s.Ref.Name, AMIType: n.AMIType, Release: n.ReleaseVersion})
			}
		}
	}
	if len(targets) == 0 {
		return
	}
	lines, warnings := aminotes.Lines(ctx, o.f.HTTPClient, targets)
	th := o.ios.ErrTheme()
	// Nothing can report a failed write to stderr.
	if len(lines) > 0 {
		_ = cmdutil.WriteLines(o.ios.ErrOut, append([]string{th.Strong("AMI release notes")}, lines...))
	}
	for _, w := range warnings {
		_, _ = fmt.Fprintln(o.ios.ErrOut, th.Caution("warning: "+w))
	}
}
