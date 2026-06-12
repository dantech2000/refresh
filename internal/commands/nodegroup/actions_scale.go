package nodegroup

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/health"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/ui"
)

func runScale(ctx context.Context, cmd *cli.Command) error {
	ctx, cancel, awsCfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, runner.RequestedCluster(cmd))
	if err != nil {
		return err
	}

	logger := factory.NewDefaultLogger(nil)
	withHealth := cmd.Bool("health-check") || cmd.Bool("check-pdbs") || cmd.Bool("wait")
	var svc *nodegroupsvc.ServiceImpl
	if withHealth {
		// Wire a Kubernetes client so workload/PDB checks run against the right
		// cluster (--kubeconfig), with an actionable diagnostic when unreachable.
		k8sClient := resolveHealthKubeClient(ctx, cmd.String("kubeconfig"), true)
		svc = factory.NewNodegroupServiceWithHealth(awsCfg, k8sClient, logger)
	} else {
		svc = factory.NewNodegroupService(awsCfg, false, logger)
	}

	opts := nodegroupsvc.ScaleOptions{
		HealthCheck: cmd.Bool("health-check"),
		CheckPDBs:   cmd.Bool("check-pdbs"),
		Wait:        cmd.Bool("wait"),
		Timeout:     cmd.Duration("op-timeout"),
		DryRun:      cmd.Bool("dry-run"),
	}

	desired, minSize, maxSize := int32PtrIfSet(cmd, "desired"), int32PtrIfSet(cmd, "min"), int32PtrIfSet(cmd, "max")

	if opts.DryRun {
		// With --check-pdbs, surface the actual PDBs that would constrain a
		// scale-down (name/namespace/disruptions-allowed), not just a generic
		// warning. svc carries a health checker whenever --check-pdbs is set. (REF-4)
		var pdbs []health.PDBInfo
		if cmd.Bool("check-pdbs") {
			if p, perr := svc.PodDisruptionBudgets(ctx); perr != nil {
				color.Yellow("Could not load PodDisruptionBudgets for preview: %v", perr)
			} else {
				pdbs = p
			}
		}
		return printScaleDryRun(ctx, eks.NewFromConfig(awsCfg), clusterName, cmd.String("nodegroup"), desired, minSize, maxSize, pdbs)
	}

	return runner.WithSpinner("nodegroup", "Scaling request submitted", func() error {
		return svc.Scale(ctx, clusterName, cmd.String("nodegroup"), desired, minSize, maxSize, opts)
	})
}

// printScaleDryRun shows the current vs requested scaling configuration
// without executing, honoring the flag's "Preview scaling impact" promise.
func printScaleDryRun(ctx context.Context, eksClient *eks.Client, clusterName, nodegroupName string, desired, minSize, maxSize *int32, pdbs []health.PDBInfo) error {
	desc, err := eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(nodegroupName),
	})
	if err != nil {
		return awsinternal.FormatAWSError(err, fmt.Sprintf("describing nodegroup %s/%s", clusterName, nodegroupName))
	}

	color.Cyan("DRY RUN: Would scale nodegroup %s in cluster %s", nodegroupName, clusterName)
	isScaleDown := false
	if sc := desc.Nodegroup.ScalingConfig; sc != nil {
		if desired != nil && *desired < aws.ToInt32(sc.DesiredSize) {
			isScaleDown = true
		}
		printScaleChange := func(label string, current *int32, requested *int32) {
			switch {
			case requested == nil:
				fmt.Printf("  %-8s %d (unchanged)\n", label+":", aws.ToInt32(current))
			case aws.ToInt32(current) == *requested:
				fmt.Printf("  %-8s %d (no change)\n", label+":", *requested)
			default:
				fmt.Printf("  %-8s %d -> %d\n", label+":", aws.ToInt32(current), *requested)
			}
		}
		printScaleChange("Desired", sc.DesiredSize, desired)
		printScaleChange("Min", sc.MinSize, minSize)
		printScaleChange("Max", sc.MaxSize, maxSize)
	}

	// On a scale-down, node drains can be blocked by PodDisruptionBudgets that
	// currently allow zero voluntary disruptions. List the specific PDBs at
	// risk (or confirm none constrain the change). (REF-4)
	if isScaleDown && pdbs != nil {
		printScaleDownPDBImpact(pdbs)
	}

	fmt.Println("\nNo changes were made. Re-run without --dry-run to execute.")
	return nil
}

// printScaleDownPDBImpact lists the PodDisruptionBudgets that would constrain a
// scale-down: those currently allowing zero voluntary disruptions block a node
// drain until their workload recovers. (REF-4)
func printScaleDownPDBImpact(pdbs []health.PDBInfo) {
	if len(pdbs) == 0 {
		ui.Outln("\nPod Disruption Budgets: none found in user namespaces — nothing constrains this scale-down.")
		return
	}
	var atRisk []health.PDBInfo
	for _, p := range pdbs {
		if p.AtRisk() {
			atRisk = append(atRisk, p)
		}
	}
	if len(atRisk) == 0 {
		color.Green("\nPod Disruption Budgets: all %d allow at least one disruption — none should block this scale-down.", len(pdbs))
		return
	}
	color.Yellow("\nPod Disruption Budgets at risk (%d): these allow 0 disruptions now and may block node drain:", len(atRisk))
	for _, p := range atRisk {
		fmt.Printf("  %s/%s: disruptionsAllowed=%d, healthy=%d/%d\n",
			p.Namespace, p.Name, p.DisruptionsAllowed, p.CurrentHealthy, p.DesiredHealthy)
	}
}

// int32PtrIfSet returns &v for cmd.Int(name) when the flag was explicitly set,
// otherwise nil.
func int32PtrIfSet(cmd *cli.Command, name string) *int32 {
	if !cmd.IsSet(name) {
		return nil
	}
	v := int32(cmd.Int(name))
	return &v
}
