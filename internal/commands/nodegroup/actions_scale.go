package nodegroup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/urfave/cli/v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/common"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/render"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/ui"
)

func runScale(ctx context.Context, cmd *cli.Command) (err error) {
	// Bad or missing sizes are usage errors, before any AWS call.
	desired, minSize, maxSize, err := readScaleSizes(cmd)
	if err != nil {
		return err
	}
	nodegroupName := scaleNodegroup(cmd)
	if nodegroupName == "" {
		return errors.New("name the nodegroup: refresh nodegroup scale CLUSTER NODEGROUP --desired N (or -n NODEGROUP)")
	}
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
	// Only the health check and the PDB gate read the cluster API. --wait
	// polls EKS alone, so it needs no Kubernetes client (and no "checks will
	// be skipped" note when the cluster can't be reached).
	withHealth := cmd.Bool("health-check") || cmd.Bool("check-pdbs")
	var svc *nodegroupsvc.ServiceImpl
	if withHealth {
		// Wire a Kubernetes client so workload/PDB checks run against the right
		// cluster (--kubeconfig), with an actionable diagnostic when unreachable.
		k8sClient, _ := resolveHealthKubeClient(ctx, factory.NewEKSClient(awsCfg), awsCfg.Region, clusterName, cmd.String("kubeconfig"), cmd.String("kube-context"), true)
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

	// Pre-flight: warn if the nodegroup's instance type isn't offered in one of
	// its AZs — a scale-up would fail to place nodes there. Runs for both the
	// dry-run preview and a real scale, so the preview surfaces it too. (REF-143)
	warnInstanceTypeAvailability(ctx, svc, clusterName, nodegroupName)

	// A --min/--max that excludes the current desired size fails the same way
	// in a preview, and before the PDB gate and the confirmation prompt.
	if err := svc.CheckScaleBounds(ctx, clusterName, nodegroupName, desired, minSize, maxSize); err != nil {
		return err
	}

	// EKS refuses a scale while another update runs, but only after the
	// prompt: say so first. A dry run still previews a busy cluster.
	if !opts.DryRun {
		if err := runner.RefuseIfBusy(ctx, factory.NewEKSClient(awsCfg), clusterName, nil); err != nil {
			return err
		}
	}

	// --check-pdbs gate. Without --force the service refuses a blocked
	// scale-down itself; with --force (or --dry-run) run the check here so the
	// overridden blockers are shown before anything changes.
	var pdbCheck *nodegroupsvc.ScaleDownPDBCheck
	var pdbCheckErr error
	if opts.CheckPDBs && (opts.DryRun || opts.Force) {
		pdbCheck, pdbCheckErr = svc.CheckScaleDownPDBs(ctx, clusterName, nodegroupName, desired)
	}

	// A PDB check that could not read what it needs is a failure: named
	// once, here, in the INCOMPLETE DATA section. Without --force the gate
	// fails closed (exit 1); with --force the scale goes ahead and the run
	// exits 4.
	fs := pdbCheckFailures(awsCfg.Region, pdbCheckErr)
	runner.WriteFailures("", os.Stdout, ui.Stderr, fs)

	if opts.DryRun {
		if err := printScaleDryRun(ctx, factory.NewEKSClient(awsCfg), clusterName, nodegroupName, desired, minSize, maxSize); err != nil {
			return err
		}
		if opts.CheckPDBs {
			printScaleDryRunPDBGate(os.Stdout, clusterName, nodegroupName, pdbCheck, pdbCheckErr, opts.Force)
		}
		fmt.Println("\nNo changes were made. Re-run without --dry-run to execute.")
		// The preview exits as the real run would at the gate: 3 when it
		// would refuse the scale-down, 1 when the PDBs could not be read,
		// and 4 when --force would scale without them.
		if opts.CheckPDBs && !opts.Force {
			return scaleExit(scaleDryRunGateErr(clusterName, nodegroupName, pdbCheck, pdbCheckErr))
		}
		return runner.IncompleteExit(fs)
	}

	if opts.CheckPDBs && opts.Force {
		warnForcedScaleDown(ui.Stderr, clusterName, nodegroupName, pdbCheck)
	}

	if !cmd.Bool("yes") {
		question, qerr := scaleQuestion(ctx, factory.NewEKSClient(awsCfg), clusterName, nodegroupName, desired, minSize, maxSize)
		if qerr != nil {
			return qerr
		}
		if err := runner.ConfirmMutation(ctx, question); err != nil {
			return err
		}
	}

	// Health warnings are held until the spinner stops, then printed on
	// stderr, so they don't interleave with the spinner line.
	var healthWarnings scaleHealthWarnings
	opts.OnHealthWarnings = healthWarnings.add
	err = runner.WithSpinner("nodegroup", "Scaling request submitted", func() error {
		return svc.Scale(ctx, clusterName, nodegroupName, desired, minSize, maxSize, opts)
	})
	healthWarnings.print(ui.Stderr)
	var pdbErr *nodegroupsvc.PDBCheckError
	if errors.As(err, &pdbErr) {
		runner.WriteFailures("", os.Stdout, ui.Stderr, pdbCheckFailures(awsCfg.Region, pdbErr))
		return pdbGateClosed()
	}
	if err != nil {
		return scaleExit(err)
	}
	// The spinner's line shows only on a terminal: say what happened either
	// way, and whether --wait saw the nodegroup settle.
	th := render.Default(os.Stdout)
	what := "Scaled"
	if !cmd.Bool("wait") {
		what = "Scale requested for"
	}
	var sizes []string
	for _, s := range []struct {
		label string
		v     *int32
	}{{"desired", desired}, {"min", minSize}, {"max", maxSize}} {
		if s.v != nil {
			sizes = append(sizes, fmt.Sprintf("%s %d", s.label, *s.v))
		}
	}
	line := fmt.Sprintf("%s %s/%s: %s", what, clusterName, nodegroupName, strings.Join(sizes, ", "))
	if cmd.Bool("wait") {
		line += " · nodegroup ACTIVE"
	} else {
		line += " · add --wait to wait for the nodes"
	}
	fmt.Println(th.Line(render.Healthy, "%s", line))
	return runner.IncompleteExit(fs)
}

// scaleHealthWarnings collects the health-check warnings of a scale, per
// stage, to print once the spinner has stopped.
type scaleHealthWarnings struct {
	stages   []string
	warnings map[string][]string
}

// add records the warnings of one stage (nodegroupsvc.ScaleOptions.OnHealthWarnings).
func (h *scaleHealthWarnings) add(stage string, warnings []string) {
	if h.warnings == nil {
		h.warnings = map[string][]string{}
	}
	if _, seen := h.warnings[stage]; !seen {
		h.stages = append(h.stages, stage)
	}
	h.warnings[stage] = append(h.warnings[stage], warnings...)
}

// print writes the collected warnings to w, one block per stage.
func (h *scaleHealthWarnings) print(w io.Writer) {
	th := render.Default(w)
	for _, stage := range h.stages {
		_, _ = fmt.Fprintln(w, th.Line(render.Warn, "The %s health check reported warnings:", stage))
		for _, msg := range h.warnings[stage] {
			_, _ = fmt.Fprintf(w, "  - %s\n", msg)
		}
	}
}

// pdbCheckFailures returns the failure behind a --check-pdbs check that
// could not read what it needs, with region set, or nil for any other
// error.
func pdbCheckFailures(region string, err error) []diag.Failure {
	var pe *nodegroupsvc.PDBCheckError
	if !errors.As(err, &pe) {
		return nil
	}
	f := pe.Failure
	if f.Region == "" {
		f.Region = region
	}
	return []diag.Failure{f}
}

// pdbGateClosed is the error of a --check-pdbs gate that could not read what
// it needs: the scale is refused (fail closed), exit 1. The INCOMPLETE DATA
// section names the read.
func pdbGateClosed() error {
	return cli.Exit("scale refused: the PodDisruptionBudgets could not be checked; fix cluster access with --kubeconfig/--kube-context, or pass --force to scale without the PDB gate", runner.ExitError)
}

// scaleExit maps a scale error to the exit-code contract: exit 3 when the
// --check-pdbs gate or the pre-scaling health check blocked the scale (nothing
// changed), exit 5 when the scale was applied but the post-scaling health
// check found blocking issues, else the error unchanged (exit 1).
func scaleExit(err error) error {
	var blocked *nodegroupsvc.ScaleDownBlockedError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &blocked), errors.Is(err, nodegroupsvc.ErrScaleHealthBlocked):
		return cli.Exit(err.Error(), runner.ExitBlocked)
	case errors.Is(err, nodegroupsvc.ErrScaleVerifyFailed):
		return cli.Exit(err.Error(), runner.ExitVerifyFailed)
	default:
		return err
	}
}

// scaleDryRunGateErr returns the error the --check-pdbs gate would stop a
// real scale with (without --force), or nil when the gate would pass.
func scaleDryRunGateErr(clusterName, nodegroupName string, check *nodegroupsvc.ScaleDownPDBCheck, checkErr error) error {
	var pe *nodegroupsvc.PDBCheckError
	if errors.As(checkErr, &pe) {
		return pdbGateClosed()
	}
	if checkErr != nil {
		return checkErr
	}
	if check != nil && check.Refused() {
		return &nodegroupsvc.ScaleDownBlockedError{Cluster: clusterName, Nodegroup: nodegroupName, Check: *check}
	}
	return nil
}

// warnForcedScaleDown prints, to w, the PDB blockers that --force is
// overriding. It prints nothing when the gate would pass, or when the check
// could not run (its failure is on stderr already).
func warnForcedScaleDown(w io.Writer, clusterName, nodegroupName string, check *nodegroupsvc.ScaleDownPDBCheck) {
	if check == nil || !check.Refused() {
		return
	}
	th := render.Default(w)
	_, _ = fmt.Fprintln(w, th.Line(render.Warn, "--force: scaling %s/%s down from %d to %d despite %d PodDisruptionBudget(s) it could violate:",
		clusterName, nodegroupName, check.CurrentDesired, check.RequestedDesired, len(check.Blockers)))
	for _, p := range check.Blockers {
		_, _ = fmt.Fprintf(w, "  - %s\n", p.DrainBlockerSummary())
	}
	_, _ = fmt.Fprintln(w, th.Paint(th.Pal.Yellow, "EKS terminates the removed nodes without honoring these PDBs; their pods on those nodes go down."))
}

// printScaleDryRunPDBGate shows what the --check-pdbs gate would decide for
// the previewed scale.
func printScaleDryRunPDBGate(w io.Writer, clusterName, nodegroupName string, check *nodegroupsvc.ScaleDownPDBCheck, checkErr error, force bool) {
	th := render.Default(w)
	gate := func(st render.Status, format string, args ...any) {
		_, _ = fmt.Fprintln(w, "\n"+th.Line(st, "PDB gate: "+format, args...))
	}
	switch {
	case checkErr != nil:
		if force {
			gate(render.Warn, "could not validate PodDisruptionBudgets (see INCOMPLETE DATA); --force would scale anyway.")
			return
		}
		gate(render.Fail, "would be REFUSED, PodDisruptionBudgets could not be validated (see INCOMPLETE DATA).")
	case check == nil || !check.ScaleDown:
		gate(render.Neutral, "not a scale-down; nothing to check.")
	case !check.Refused():
		msg := fmt.Sprintf("no PodDisruptionBudget blocks removing nodes from %s.", nodegroupName)
		if check.Note != "" {
			msg += " " + check.Note + "."
		}
		gate(render.Healthy, "%s", msg)
	default:
		verdict, st := "would be REFUSED", render.Fail
		if force {
			verdict, st = "would be overridden by --force", render.Warn
		}
		scope := "with pods on this nodegroup's nodes"
		if !check.Scoped {
			scope = "in the cluster (could not scope to this nodegroup's nodes)"
		}
		gate(st, "%s. %d PodDisruptionBudget(s) %s could lose more pods than they allow:", verdict, len(check.Blockers), scope)
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
	desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
		return eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(nodegroupName),
		})
	})
	if err != nil {
		return awsinternal.FormatAWSError(err, fmt.Sprintf("describing nodegroup %s/%s", clusterName, nodegroupName))
	}
	if desc == nil || desc.Nodegroup == nil {
		return fmt.Errorf("describing nodegroup %s/%s: empty DescribeNodegroup response", clusterName, nodegroupName)
	}

	fmt.Println(render.Default(os.Stdout).DryRun("Would scale nodegroup %s in cluster %s", nodegroupName, clusterName))
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

// readScaleSizes reads --desired, --min, and --max. At least one is required:
// without a size, UpdateNodegroupConfig would start an EKS update that
// changes nothing.
func readScaleSizes(cmd *cli.Command) (desired, minSize, maxSize *int32, err error) {
	if desired, err = int32PtrIfSet(cmd, "desired"); err != nil {
		return nil, nil, nil, err
	}
	if minSize, err = int32PtrIfSet(cmd, "min"); err != nil {
		return nil, nil, nil, err
	}
	if maxSize, err = int32PtrIfSet(cmd, "max"); err != nil {
		return nil, nil, nil, err
	}
	if desired == nil && minSize == nil && maxSize == nil {
		return nil, nil, nil, errors.New("nodegroup scale needs at least one of --desired, --min, or --max")
	}
	return desired, minSize, maxSize, nil
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

// scaleNodegroup is the nodegroup to scale: -n, else the positional after the
// cluster (`scale CLUSTER NODEGROUP`, as nodegroup update takes it), or the
// only positional when --cluster names the cluster.
func scaleNodegroup(cmd *cli.Command) string {
	if v := strings.TrimSpace(cmd.String("nodegroup")); v != "" {
		return v
	}
	args := cmd.Args().Slice()
	i := 1
	if cmd.IsSet("cluster") {
		i = 0
	}
	if len(args) > i {
		return strings.TrimSpace(args[i])
	}
	return ""
}
