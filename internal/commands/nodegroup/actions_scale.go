package nodegroup

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/services/common"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/ui"
)

func runScale(ctx context.Context, cmd *cli.Command) (err error) {
	// A scale that can't be confirmed fails before any AWS call.
	if err := runner.RequireYesUnattended(cmd); err != nil {
		return err
	}
	// --timeout alone would cap --wait (default --wait-timeout 5m) at the API
	// timeout (default 60s); widen the deadline to cover the wait too.
	waitTimeout := runner.WaitTimeout(cmd, "op-timeout")
	setupTimeout := scaleSetupTimeout(runner.APITimeout(cmd), waitTimeout, cmd.Bool("wait"), cmd.Bool("health-check"))
	ctx, cancel, awsCfg, err := runner.SetupAWSWithDeadline(ctx, cmd, setupTimeout)
	if err != nil {
		return err
	}
	defer cancel()
	if cmd.Bool("wait") {
		// The wait adds --wait-timeout to the run deadline: a timeout names it.
		defer runner.WaitDeadlineHint(&err)
	}

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
		k8sClient, _ := resolveHealthKubeClient(ctx, eks.NewFromConfig(awsCfg), awsCfg.Region, clusterName, cmd.String("kubeconfig"), cmd.String("kube-context"), true)
		svc = factory.NewNodegroupServiceWithHealth(awsCfg, k8sClient, logger)
	} else {
		svc = factory.NewNodegroupService(awsCfg, false, logger)
	}

	opts := nodegroupsvc.ScaleOptions{
		HealthCheck: cmd.Bool("health-check"),
		CheckPDBs:   cmd.Bool("check-pdbs"),
		Wait:        cmd.Bool("wait"),
		Timeout:     waitTimeout,
		DryRun:      cmd.Bool("dry-run"),
		Force:       cmd.Bool("force"),
	}

	desired, err := int32PtrIfSet(cmd, "desired")
	if err != nil {
		return err
	}
	minSize, err := int32PtrIfSet(cmd, "min")
	if err != nil {
		return err
	}
	maxSize, err := int32PtrIfSet(cmd, "max")
	if err != nil {
		return err
	}

	// Pre-flight: warn if the nodegroup's instance type isn't offered in one of
	// its AZs — a scale-up would fail to place nodes there. Runs for both the
	// dry-run preview and a real scale, so the preview surfaces it too. (REF-143)
	warnInstanceTypeAvailability(ctx, svc, clusterName, cmd.String("nodegroup"))

	nodegroupName := cmd.String("nodegroup")

	// --check-pdbs gate. Without --force the service refuses a blocked
	// scale-down itself; with --force (or --dry-run) run the check here so the
	// overridden blockers are shown before anything changes.
	var pdbCheck *nodegroupsvc.ScaleDownPDBCheck
	var pdbCheckErr error
	if opts.CheckPDBs && (opts.DryRun || opts.Force) {
		pdbCheck, pdbCheckErr = svc.CheckScaleDownPDBs(ctx, clusterName, nodegroupName, desired)
	}

	// A --min/--max that excludes the current desired size fails the same way
	// in a preview, and before the confirmation prompt.
	if err := svc.CheckScaleBounds(ctx, clusterName, nodegroupName, desired, minSize, maxSize); err != nil {
		return err
	}

	if opts.DryRun {
		if err := printScaleDryRun(ctx, eks.NewFromConfig(awsCfg), clusterName, nodegroupName, desired, minSize, maxSize); err != nil {
			return err
		}
		if opts.CheckPDBs {
			printScaleDryRunPDBGate(os.Stdout, clusterName, nodegroupName, pdbCheck, pdbCheckErr, opts.Force)
		}
		fmt.Println("\nNo changes were made. Re-run without --dry-run to execute.")
		return nil
	}

	if opts.CheckPDBs && opts.Force {
		warnForcedScaleDown(ui.Stderr, clusterName, nodegroupName, pdbCheck, pdbCheckErr)
	}

	if !cmd.Bool("yes") {
		question, qerr := scaleQuestion(ctx, eks.NewFromConfig(awsCfg), clusterName, nodegroupName, desired, minSize, maxSize)
		if qerr != nil {
			return qerr
		}
		if err := runner.ConfirmMutation(ctx, question); err != nil {
			return err
		}
	}

	return runner.WithSpinner("nodegroup", "Scaling request submitted", func() error {
		return svc.Scale(ctx, clusterName, nodegroupName, desired, minSize, maxSize, opts)
	})
}

// warnForcedScaleDown prints, to w, the PDB blockers (or the failed check)
// that --force is overriding. It prints nothing when the gate would pass.
func warnForcedScaleDown(w io.Writer, clusterName, nodegroupName string, check *nodegroupsvc.ScaleDownPDBCheck, checkErr error) {
	warn := ui.ColorFor(w, color.FgYellow)
	if checkErr != nil {
		_, _ = warn.Fprintf(w, "Warning: --force: could not validate PodDisruptionBudgets, scaling anyway: %v\n", checkErr)
		return
	}
	if check == nil || !check.Refused() {
		return
	}
	_, _ = warn.Fprintf(w, "Warning: --force: scaling %s/%s down from %d to %d despite %d PodDisruptionBudget(s) it could violate:\n",
		clusterName, nodegroupName, check.CurrentDesired, check.RequestedDesired, len(check.Blockers))
	for _, p := range check.Blockers {
		_, _ = fmt.Fprintf(w, "  - %s\n", p.DrainBlockerSummary())
	}
	_, _ = warn.Fprintln(w, "EKS terminates the removed nodes without honoring these PDBs; their pods on those nodes go down.")
}

// printScaleDryRunPDBGate shows what the --check-pdbs gate would decide for
// the previewed scale.
func printScaleDryRunPDBGate(w io.Writer, clusterName, nodegroupName string, check *nodegroupsvc.ScaleDownPDBCheck, checkErr error, force bool) {
	switch {
	case checkErr != nil:
		if force {
			_, _ = ui.ColorFor(w, color.FgYellow).Fprintf(w, "\nPDB gate: could not validate PodDisruptionBudgets (%v); --force would scale anyway.\n", checkErr)
			return
		}
		_, _ = ui.ColorFor(w, color.FgRed).Fprintf(w, "\nPDB gate: would be REFUSED, PodDisruptionBudgets could not be validated: %v\n", checkErr)
	case check == nil || !check.ScaleDown:
		_, _ = fmt.Fprintln(w, "\nPDB gate: not a scale-down; nothing to check.")
	case !check.Refused():
		msg := fmt.Sprintf("\nPDB gate: no PodDisruptionBudget blocks removing nodes from %s.", nodegroupName)
		if check.Note != "" {
			msg += " " + check.Note + "."
		}
		_, _ = ui.ColorFor(w, color.FgGreen).Fprintln(w, msg)
	default:
		verdict := "would be REFUSED"
		c := color.New(color.FgRed)
		if force {
			verdict = "would be overridden by --force"
			c = color.New(color.FgYellow)
		}
		scope := "with pods on this nodegroup's nodes"
		if !check.Scoped {
			scope = "in the cluster (could not scope to this nodegroup's nodes)"
		}
		_, _ = c.Fprintf(w, "\nPDB gate: %s. %d PodDisruptionBudget(s) %s could lose more pods than they allow:\n", verdict, len(check.Blockers), scope)
		for _, p := range check.Blockers {
			_, _ = fmt.Fprintf(w, "  - %s\n", p.DrainBlockerSummary())
		}
		if !force {
			_, _ = fmt.Fprintf(w, "Scale up the workload or relax the PDB first, or pass --force to scale %s/%s down anyway.\n", clusterName, nodegroupName)
		}
	}
}

// scaleSetupTimeout returns the overall deadline for a scale run. --timeout
// covers the API calls and pre-checks; with --wait the run also gets the full
// --wait-timeout, plus another --timeout for the post-scale health check. A
// value <= 0 means no deadline (--timeout 0, or --wait with --wait-timeout 0).
func scaleSetupTimeout(apiTimeout, opTimeout time.Duration, wait, healthCheck bool) time.Duration {
	if apiTimeout <= 0 {
		return 0
	}
	if !wait {
		return apiTimeout
	}
	if opTimeout <= 0 {
		return 0
	}
	total := apiTimeout + opTimeout
	if healthCheck {
		total += apiTimeout
	}
	return total
}

// scaleQuestion builds the confirmation prompt for a scale, from the
// nodegroup's current scaling config: "Scale prod/ng-a desired 3 → 1?". Only
// the requested bounds are listed.
func scaleQuestion(ctx context.Context, eksClient *eks.Client, clusterName, nodegroupName string, desired, minSize, maxSize *int32) (string, error) {
	// Retried: a throttle here would otherwise abort the scale before the
	// prompt.
	desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
		return eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(nodegroupName),
		})
	})
	if err != nil {
		return "", awsinternal.FormatAWSError(err, fmt.Sprintf("describing nodegroup %s/%s", clusterName, nodegroupName))
	}
	var sc ekstypes.NodegroupScalingConfig
	if desc != nil && desc.Nodegroup != nil && desc.Nodegroup.ScalingConfig != nil {
		sc = *desc.Nodegroup.ScalingConfig
	}
	return formatScaleQuestion(clusterName, nodegroupName, sc, desired, minSize, maxSize), nil
}

// formatScaleQuestion is scaleQuestion's text, split out for tests.
func formatScaleQuestion(clusterName, nodegroupName string, sc ekstypes.NodegroupScalingConfig, desired, minSize, maxSize *int32) string {
	var parts []string
	for _, b := range []struct {
		label     string
		current   *int32
		requested *int32
	}{{"desired", sc.DesiredSize, desired}, {"min", sc.MinSize, minSize}, {"max", sc.MaxSize, maxSize}} {
		if b.requested != nil {
			parts = append(parts, fmt.Sprintf("%s %d → %d", b.label, aws.ToInt32(b.current), *b.requested))
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Scale %s/%s (no size change requested)?", clusterName, nodegroupName)
	}
	return fmt.Sprintf("Scale %s/%s %s?", clusterName, nodegroupName, strings.Join(parts, ", "))
}

// printScaleDryRun shows the current vs requested scaling configuration
// without executing, honoring the flag's "Preview scaling impact" promise.
func printScaleDryRun(ctx context.Context, eksClient *eks.Client, clusterName, nodegroupName string, desired, minSize, maxSize *int32) error {
	desc, err := eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(nodegroupName),
	})
	if err != nil {
		return awsinternal.FormatAWSError(err, fmt.Sprintf("describing nodegroup %s/%s", clusterName, nodegroupName))
	}
	if desc == nil || desc.Nodegroup == nil {
		return fmt.Errorf("describing nodegroup %s/%s: empty DescribeNodegroup response", clusterName, nodegroupName)
	}

	color.Cyan("DRY RUN: Would scale nodegroup %s in cluster %s", nodegroupName, clusterName)
	if sc := desc.Nodegroup.ScalingConfig; sc != nil {
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

	return nil
}

// int32PtrIfSet returns &v for cmd.Int(name) when the flag was explicitly set,
// otherwise nil. It rejects values outside [0, math.MaxInt32] so a too-large
// count can't silently wrap to a negative/garbage size in the mutating
// UpdateNodegroupConfig call.
func int32PtrIfSet(cmd *cli.Command, name string) (*int32, error) {
	if !cmd.IsSet(name) {
		return nil, nil
	}
	v := cmd.Int(name)
	if v < 0 || v > math.MaxInt32 {
		return nil, fmt.Errorf("--%s must be between 0 and %d, got %d", name, math.MaxInt32, v)
	}
	out := int32(v)
	return &out, nil
}
