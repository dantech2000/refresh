package nodegroup

import (
	"context"
	"fmt"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
)

func runDescribe(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsStandard); err != nil {
		return err
	}

	// Validate the nodegroup name before any AWS work: PositionalSlot reads only
	// flags/positionals, so a missing name should fail fast — not after loading
	// AWS config, resolving the cluster, and printing a misleading "Cluster name
	// resolved!" success message. (REF-131)
	ngName := runner.PositionalSlot(cmd, "nodegroup", "cluster")
	if ngName == "" {
		return fmt.Errorf("missing nodegroup name; pass as second argument or --nodegroup <name>")
	}

	ctx, cancel, awsCfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, awsCfg, cmd)
	if err != nil || listed {
		return err
	}

	logger := factory.NewDefaultLogger(nil)
	svc := factory.NewNodegroupService(awsCfg, false, logger)

	opts := nodegroupsvc.DescribeOptions{
		ShowInstances: cmd.Bool("show-instances"),
		ShowWorkloads: cmd.Bool("show-workloads"),
	}
	if opts.ShowWorkloads {
		opts.KubeClient, _ = runner.ResolveClusterKubeClient(ctx, runner.KubeRequest{
			API:         factory.NewEKSClient(awsCfg),
			Cluster:     clusterName,
			Region:      awsCfg.Region,
			Kubeconfig:  cmd.String("kubeconfig"),
			KubeContext: cmd.String("kube-context"),
			Verbose:     !runner.IsMachineFormat(cmd.String("format")),
			SkipNote:    "Workload placement will show as unavailable.",
		})
	}

	var details *nodegroupsvc.NodegroupDetails
	start := time.Now()
	if err := runner.WithSpinner("nodegroup", "Nodegroup details gathered!", func() error {
		var derr error
		details, derr = svc.Describe(ctx, clusterName, ngName, opts)
		return derr
	}); err != nil {
		return err
	}

	if handled, err := runner.EncodeStdout(cmd.String("format"), details); handled {
		if err != nil {
			return err
		}
	} else if err := outputNodegroupDetailsTable(details, time.Since(start)); err != nil {
		return err
	}
	// Advisory: the AMI lookup does not change the exit code.
	warnAMILookup(warnOut, []nodegroupsvc.NodegroupSummary{{AMILookupFailure: details.AMILookupFailure}})
	return nil
}
