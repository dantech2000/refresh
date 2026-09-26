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
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/render"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/ui"
)

func runScale(ctx context.Context, cmd *cli.Command) (err error) {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsDocument); err != nil {
		return err
	}
	format := strings.ToLower(cmd.String("format"))
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

	eksClient := factory.NewEKSClient(awsCfg)
	svc := newScaleService(ctx, cmd, awsCfg, eksClient, clusterName)
	opts := nodegroupsvc.ScaleOptions{
		HealthCheck: cmd.Bool("health-check"),
		CheckPDBs:   cmd.Bool("check-pdbs"),
		Wait:        cmd.Bool("wait"),
		Timeout:     waitTimeout,
		DryRun:      cmd.Bool("dry-run"),
		// The command runs the --check-pdbs gate itself, for the document:
		// the service does not run it again (see ScaleOptions.Force).
		Force: true,
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
		if err := runner.RefuseIfBusy(ctx, eksClient, clusterName, nil); err != nil {
			return err
		}
	}

	current, err := svc.DescribeNodegroup(ctx, clusterName, nodegroupName)
	if err != nil {
		return err
	}
	r := &scaleRun{
		cmd: cmd, format: format, machine: runner.IsMachineFormat(format), force: cmd.Bool("force"),
		region: awsCfg.Region, cluster: clusterName, nodegroup: nodegroupName,
		desired: desired, minSize: minSize, maxSize: maxSize,
		svc: svc, opts: opts,
	}
	if current.ScalingConfig != nil {
		r.before = *current.ScalingConfig
	}
	r.doc = scaleDocument{
		Cluster:   clusterName,
		Nodegroup: nodegroupName,
		Region:    awsCfg.Region,
		DryRun:    opts.DryRun,
		Before:    sizesOf(r.before),
		After:     sizesOf(r.before).withRequested(desired, minSize, maxSize),
		Waited:    opts.Wait && !opts.DryRun,
		Failures:  diag.List{},
	}

	// A dry run or --force checks the PDBs first, so the blockers show
	// before anything changes. A real run without --force checks after the
	// prompt, right before the change (see execute).
	if opts.CheckPDBs && (opts.DryRun || r.force) {
		r.runGate(ctx)
	}
	if opts.DryRun {
		return r.preview()
	}
	return r.execute(ctx)
}

// newScaleService builds the nodegroup service for a scale. Only the health
// check and the PDB gate read the cluster API. --wait polls EKS alone, so it
// needs no Kubernetes client (and no "checks will be skipped" note when the
// cluster can't be reached).
func newScaleService(ctx context.Context, cmd *cli.Command, awsCfg aws.Config, eksClient *eks.Client, clusterName string) *nodegroupsvc.ServiceImpl {
	logger := factory.NewDefaultLogger(nil)
	if !cmd.Bool("health-check") && !cmd.Bool("check-pdbs") {
		return factory.NewNodegroupService(awsCfg, false, logger)
	}
	// Wire a Kubernetes client so workload/PDB checks run against the right
	// cluster (--kubeconfig), with an actionable diagnostic when unreachable.
	k8sClient, _ := resolveHealthKubeClient(ctx, eksClient, awsCfg.Region, clusterName, cmd.String("kubeconfig"), cmd.String("kube-context"), true)
	return factory.NewNodegroupServiceWithHealth(awsCfg, k8sClient, logger)
}

// scaleRun is a `nodegroup scale` run after setup: what it asks for, and
// what it has found so far.
type scaleRun struct {
	cmd                        *cli.Command
	format                     string
	machine, force             bool
	region, cluster, nodegroup string
	desired, minSize, maxSize  *int32
	svc                        *nodegroupsvc.ServiceImpl
	opts                       nodegroupsvc.ScaleOptions
	before                     ekstypes.NodegroupScalingConfig
	doc                        scaleDocument

	// The --check-pdbs gate: the check and its read error, the failures
	// the run reports, and the gate's refusal without --force (exit 3, or
	// exit 1 when it fails closed).
	pdbCheck    *nodegroupsvc.ScaleDownPDBCheck
	pdbCheckErr error
	fs          []diag.Failure
	gateErr     error
	failClosed  bool
}

// finish prints the document (-o json/yaml) and returns exit.
func (r *scaleRun) finish(outcome scaleOutcome, exit error) error {
	r.doc.Outcome = outcome
	if _, err := runner.EncodeStdout(r.format, r.doc); err != nil {
		return err
	}
	return exit
}

// runGate runs the --check-pdbs gate. Without --force a blocked scale-down
// is refused (exit 3) and a check that could not read what it needs fails
// closed (exit 1, with no document, as for any error); with --force the
// blockers are a warning.
func (r *scaleRun) runGate(ctx context.Context) {
	r.pdbCheck, r.pdbCheckErr = r.svc.CheckScaleDownPDBs(ctx, r.cluster, r.nodegroup, r.desired)
	r.doc.PDBGate = pdbGateOf(r.pdbCheck, r.pdbCheckErr, r.force)
	// A PDB check that could not read what it needs is a failure: named
	// once, here (in the INCOMPLETE DATA section of the table view). With
	// --force the scale goes ahead and the run exits 4.
	r.fs = pdbCheckFailures(r.region, r.pdbCheckErr)
	r.doc.Failures = append(diag.List{}, r.fs...)
	runner.WriteFailures(r.format, os.Stdout, ui.Stderr, r.fs)
	if !r.force {
		r.gateErr = scaleExit(scaleDryRunGateErr(r.cluster, r.nodegroup, r.pdbCheck, r.pdbCheckErr))
		r.failClosed = r.pdbCheckErr != nil
	}
}

// preview ends a --dry-run. It exits as the real run would at the gate: 3
// when it would refuse the scale-down, 1 when the PDBs could not be read,
// and 4 when --force would scale without them.
func (r *scaleRun) preview() error {
	if !r.machine {
		printScaleDryRun(r.cluster, r.nodegroup, r.before, r.desired, r.minSize, r.maxSize)
		if r.opts.CheckPDBs {
			printScaleDryRunPDBGate(os.Stdout, r.cluster, r.nodegroup, r.pdbCheck, r.pdbCheckErr, r.force)
		}
		fmt.Println("\nNo changes were made. Re-run without --dry-run to execute.")
	}
	switch {
	case r.failClosed:
		return r.gateErr
	case r.gateErr != nil:
		return r.finish(scalePlanned, r.gateErr)
	}
	return r.finish(scalePlanned, runner.IncompleteExit(r.fs))
}

// execute confirms, gates, and scales.
func (r *scaleRun) execute(ctx context.Context) error {
	if r.opts.CheckPDBs && r.force {
		warnForcedScaleDown(ui.Stderr, r.cluster, r.nodegroup, r.pdbCheck)
	}
	if !r.cmd.Bool("yes") {
		question := formatScaleQuestion(r.cluster, r.nodegroup, r.before, r.desired, r.minSize, r.maxSize)
		if err := runner.ConfirmMutation(ctx, question); err != nil {
			return err
		}
	}
	if r.opts.CheckPDBs && !r.force {
		r.runGate(ctx)
		switch {
		case r.failClosed:
			return r.gateErr
		case r.gateErr != nil:
			return r.finish(scaleBlocked, r.gateErr)
		}
	}

	// Health warnings are held until the spinner stops, then printed on
	// stderr, so they don't interleave with the spinner line.
	var healthWarnings scaleHealthWarnings
	r.opts.OnHealthWarnings = healthWarnings.add
	err := runner.WithSpinner("nodegroup", "Scaling request submitted", func() error {
		return r.svc.Scale(ctx, r.cluster, r.nodegroup, r.desired, r.minSize, r.maxSize, r.opts)
	})
	healthWarnings.print(ui.Stderr)
	switch {
	case errors.Is(err, nodegroupsvc.ErrScaleHealthBlocked):
		return r.finish(scaleBlocked, scaleExit(err))
	case errors.Is(err, nodegroupsvc.ErrScaleVerifyFailed):
		return r.finish(scaleCompletedWithIssues, scaleExit(err))
	case err != nil:
		return scaleExit(err)
	}
	if r.machine {
		return r.finishScaled(ctx)
	}
	r.printScaled()
	return runner.IncompleteExit(r.fs)
}

// finishScaled prints the document of a scale EKS accepted. After --wait
// it reads the nodegroup's status; a failed read is a failure (exit 4).
func (r *scaleRun) finishScaled(ctx context.Context) error {
	if !r.opts.Wait {
		return r.finish(scaleRequested, runner.IncompleteExit(r.fs))
	}
	if ng, err := r.svc.DescribeNodegroup(ctx, r.cluster, r.nodegroup); err != nil {
		f := diag.FromError(diag.KindNodegroup, r.nodegroup, diag.OpDescribeNodegroup, err)
		f.Cluster, f.Region = r.cluster, r.region
		r.fs = append(r.fs, f)
		r.doc.Failures = append(r.doc.Failures, f)
		runner.WriteFailures(r.format, os.Stdout, ui.Stderr, []diag.Failure{f})
	} else {
		r.doc.NodegroupStatus = string(ng.Status)
	}
	return r.finish(scaleCompleted, runner.IncompleteExit(r.fs))
}

// printScaled says what happened, as the spinner's line shows only on a
// terminal, and whether --wait saw the nodegroup settle.
func (r *scaleRun) printScaled() {
	th := render.Default(os.Stdout)
	what := "Scaled"
	if !r.opts.Wait {
		what = "Scale requested for"
	}
	var sizes []string
	for _, s := range []struct {
		label string
		v     *int32
	}{{"desired", r.desired}, {"min", r.minSize}, {"max", r.maxSize}} {
		if s.v != nil {
			sizes = append(sizes, fmt.Sprintf("%s %d", s.label, *s.v))
		}
	}
	line := fmt.Sprintf("%s %s/%s: %s", what, r.cluster, r.nodegroup, strings.Join(sizes, ", "))
	if r.opts.Wait {
		line += " · nodegroup ACTIVE"
	} else {
		line += " · add --wait to wait for the nodes"
	}
	fmt.Println(th.Line(render.Healthy, "%s", line))
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

// formatScaleQuestion is the confirmation prompt for a scale, from the
// nodegroup's current scaling config: "Scale prod/ng-a desired 3 → 1?". Only
// the requested bounds are listed.
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
func printScaleDryRun(clusterName, nodegroupName string, sc ekstypes.NodegroupScalingConfig, desired, minSize, maxSize *int32) {
	fmt.Println(render.Default(os.Stdout).DryRun("Would scale nodegroup %s in cluster %s", nodegroupName, clusterName))
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
