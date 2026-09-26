package cluster

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/rollview"
	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/ui"
)

func rollbackCommand() *cli.Command {
	return &cli.Command{
		Name:      "rollback",
		Usage:     "Roll a cluster back one minor version after an in-place upgrade: nodegroups → add-ons → control plane",
		ArgsUsage: "[cluster]",
		Description: `Roll an EKS cluster back to the previous minor version (N to N-1) after an
in-place upgrade, in the order AWS documents:
https://docs.aws.amazon.com/eks/latest/userguide/rollback-cluster.html

  1. Check the prerequisites and the ROLLBACK_READINESS cluster insights.
  2. Roll back the managed nodegroups that run N to N-1.
  3. Downgrade the add-ons whose version N-1 cannot run to the newest
     version compatible with N-1.
  4. Roll back the control plane (UpdateClusterVersion to N-1) and wait.

EKS accepts a rollback only within about 7 days of the upgrade, only for a
cluster that was upgraded in place to its current version, only one minor
back, and only while the cluster is ACTIVE with no update in progress. To
roll back to a version in extended support, the cluster's upgrade policy must
be EXTENDED. refresh checks these first and exits 3 when one fails. EKS has
the final word: it also rejects a rollback when an EKS feature enabled on the
cluster does not exist in N-1.

ERROR and UNKNOWN rollback-readiness insights block the rollback.
--skip-insights-check proceeds anyway and tells EKS to skip them too (the
EKS "force" option). EKS does not roll back self-managed or hybrid nodes, or
Fargate pods: handle them yourself first. EKS rolls back EKS Auto Mode nodes
itself.

Before each nodegroup rollback, the same pre-flight checks as 'cluster
upgrade' run, including PodDisruptionBudgets that would block the drain. A
drain blocker stops the rollback unless --force.

The plan is derived from live cluster state on every run, so rerunning the
same command after a failure or Ctrl+C continues where it stopped, and
rerunning after a finished rollback does nothing.

Examples:
   # Print the plan only (exits 3 if anything blocks the rollback)
   refresh cluster rollback prod-east --dry-run

   # Roll back, after one confirmation
   refresh cluster rollback prod-east

   # Non-interactive run with a 4h EKS rollback timeout
   refresh cluster rollback prod-east --yes --rollback-timeout 4h -o json`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			runner.DryRunFlag("Print the rollback plan without changing anything"),
			runner.YesFlag("Skip the confirmation prompt (required with -o json/yaml or without a terminal)"),
			&cli.BoolFlag{Name: "force", Usage: "Force nodegroup rollbacks when pods can't be drained due to PDBs"},
			&cli.BoolFlag{Name: "skip-insights-check", Usage: "Roll back despite ERROR or UNKNOWN rollback-readiness insights, and tell EKS to skip them too (not recommended)"},
			&cli.BoolFlag{Name: "skip-health-check", Usage: "Roll back nodegroups without the pre-flight PDB drain-blocker and health checks (not recommended)"},
			runner.KubeconfigFlag("the PDB drain-blocker checks and the live roll panel"),
			runner.KubeContextFlag(),
			&cli.StringSliceFlag{Name: "skip-nodegroup", Usage: "Nodegroup name pattern to leave alone (repeatable); move it to N-1 yourself"},
			&cli.DurationFlag{Name: "rollback-timeout", Usage: "How long EKS may take before it cancels the control-plane rollback, 2h to 168h (default: EKS's 12h)"},
			&cli.BoolFlag{Name: "quiet", Aliases: []string{"q"}, Usage: "Suppress progress output"},
			runner.WaitTimeoutFlag("How long to wait for the whole rollback to finish (0 = no limit)", appconfig.DefaultUpgradeTimeout),
			&cli.DurationFlag{Name: "poll-interval", Usage: "How often to poll in-flight updates", Value: appconfig.DefaultPollInterval},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml). json/yaml print one document: the plan with --dry-run or when blocked, else {plan, report, failures} after the run (requires --yes)", Value: "table"},
		},
		Action: runRollback,
	}
}

// rollbackResult is the -o json/yaml document of an executed rollback.
type rollbackResult struct {
	Plan     *upgrade.RollbackPlan `json:"plan" yaml:"plan"`
	Report   *upgrade.Report       `json:"report" yaml:"report"`
	Failures diag.List             `json:"failures" yaml:"failures"`
}

// DocumentKind is RollbackRun.
func (rollbackResult) DocumentKind() apidoc.Kind { return apidoc.KindRollbackRun }

// rollbackBlocked is the exit of a plan with a blocker.
func rollbackBlocked(plan *upgrade.RollbackPlan, msg string) error {
	if plan.Blocked() {
		return cli.Exit(msg, runner.ExitBlocked)
	}
	return runner.IncompleteExit(plan.Failures)
}

func runRollback(ctx context.Context, cmd *cli.Command) (err error) {
	format := cmd.String("format")
	if err := runner.ValidateFormat(format, runner.FormatsDocument); err != nil {
		return err
	}
	if err := upgrade.ValidateRollbackTimeout(cmd.Duration("rollback-timeout")); err != nil {
		return err
	}
	if err := runner.RequireYesUnattended(cmd); err != nil {
		return err
	}
	pollInterval := cmd.Duration("poll-interval")
	if pollInterval <= 0 {
		return fmt.Errorf("--poll-interval must be greater than 0 (got %s)", pollInterval)
	}
	waitTimeout := runner.WaitTimeout(cmd, "")
	ctx, cancel, awsCfg, err := runner.SetupAWSWithDeadline(ctx, cmd, waitTimeout)
	if err != nil {
		return err
	}
	defer cancel()
	defer runner.WaitDeadlineHint(&err)

	clusterName, err := runner.ResolveCluster(ctx, awsCfg, cmd)
	if err != nil {
		return err
	}

	svc := upgrade.NewService(factory.NewEKSClient(awsCfg), factory.NewDefaultLogger(nil))
	svc.PollInterval = pollInterval

	var plan *upgrade.RollbackPlan
	if werr := runner.WithSpinner("cluster", "Rollback plan computed", func() error {
		var perr error
		plan, perr = svc.BuildRollbackPlan(ctx, clusterName, upgrade.RollbackOptions{
			SkipNodegroups:    cmd.StringSlice("skip-nodegroup"),
			SkipInsightsCheck: cmd.Bool("skip-insights-check"),
		})
		return perr
	}); werr != nil {
		if ctx.Err() != nil {
			how := "interrupted"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				how = "timed out"
			}
			return cli.Exit(fmt.Sprintf("rollback %s before it started; nothing was changed: %v", how, werr), runner.ExitError)
		}
		return werr
	}
	setRegion(plan.Failures, awsCfg.Region)

	healthGate := newNodegroupHealthGate(cmd, awsCfg, clusterName)
	opts := upgrade.ExecuteOptions{
		// The one confirmation is asked below, before the run starts.
		Yes:               true,
		SkipNodegroups:    cmd.StringSlice("skip-nodegroup"),
		Force:             cmd.Bool("force"),
		SkipInsightsCheck: cmd.Bool("skip-insights-check"),
		NodegroupGate:     healthGate.check,
		RollbackTimeout:   cmd.Duration("rollback-timeout"),
	}

	if runner.IsMachineFormat(format) {
		return runRollbackMachine(ctx, cmd, svc, plan, opts, healthGate, awsCfg.Region, format)
	}

	out := os.Stdout
	writeLines(out, rollbackPlanLines(render.Default(out), plan))
	runner.WriteFailures(format, out, ui.Stderr, plan.Failures)
	if plan.Blocked() || cmd.Bool("dry-run") {
		return rollbackBlocked(plan, "Rollback blocked — resolve the blockers above and re-run.")
	}
	if plan.PendingSteps() == 0 {
		writeLines(out, rollbackOutcomeLines(render.Default(out), plan, false))
		return runner.IncompleteExit(plan.Failures)
	}

	if !cmd.Bool("yes") {
		_, _ = fmt.Fprintln(ui.Stderr)
		if err := runner.ConfirmMutation(ctx, rollbackQuestion(plan)); err != nil {
			return err
		}
		healthGate.confirm = func(label string) bool {
			return runner.ConfirmMutation(ctx, "Proceed with "+label+"?") == nil
		}
	}
	progress := func(format string, args ...any) {
		if !cmd.Bool("quiet") {
			_, _ = fmt.Fprintf(out, "  "+format+"\n", args...)
		}
	}
	opts.Progress = progress
	opts.PhaseStart = phaseStart(out, cmd.Bool("quiet"))
	healthGate.progress = progress
	if !cmd.Bool("quiet") && rollview.Interactive(out) {
		if kube, _ := resolveReadinessKubeClient(ctx, factory.NewEKSClient(awsCfg), awsCfg.Region, clusterName, cmd.String("kubeconfig"), cmd.String("kube-context"), false); kube != nil {
			opts.NodegroupObserver = func(octx context.Context, ng string) {
				rollview.LiveRollForUpdate(octx, kube, ng, waitTimeout, pollInterval)
			}
		}
	}

	report, err := svc.ExecuteRollback(ctx, plan, opts)
	renderReport(out, report)
	if report != nil && report.Failure != nil {
		stop := []diag.Failure{*report.Failure}
		setRegion(stop, awsCfg.Region)
		runner.WriteFailures(format, out, ui.Stderr, stop)
	}
	if err != nil {
		th := render.Default(out)
		_, _ = fmt.Fprintf(out, "\nResume with: %s\n", th.Paint(th.Pal.Sky, rollbackResumeCommand(cmd, clusterName)))
		return err
	}
	writeLines(out, rollbackOutcomeLines(render.Default(out), plan, true))
	return runner.IncompleteExit(plan.Failures)
}

// runRollbackMachine is the -o json/yaml path: one document on stdout (the
// plan for --dry-run or a blocked plan, else {plan, report, failures}),
// progress on stderr, and no prompt (RequireYesUnattended needs --yes).
func runRollbackMachine(ctx context.Context, cmd *cli.Command, svc *upgrade.Service, plan *upgrade.RollbackPlan, opts upgrade.ExecuteOptions, healthGate *nodegroupHealthGate, region, format string) error {
	if plan.Blocked() || cmd.Bool("dry-run") {
		if _, err := runner.EncodeStdout(format, plan); err != nil {
			return err
		}
		runner.ReportFailures(ui.Stderr, plan.Failures)
		return rollbackBlocked(plan, "rollback blocked: resolve the blockers in the plan and re-run")
	}
	report := upgrade.NewReport()
	var err error
	if plan.PendingSteps() > 0 {
		opts.Progress = func(format string, args ...any) {
			if !cmd.Bool("quiet") {
				_, _ = fmt.Fprintf(ui.Stderr, "  "+format+"\n", args...)
			}
		}
		opts.PhaseStart = phaseStart(ui.Stderr, cmd.Bool("quiet"))
		healthGate.progress = opts.Progress
		var r *upgrade.Report
		r, err = svc.ExecuteRollback(ctx, plan, opts)
		if r != nil {
			report = r
		}
		if report.Failure != nil && report.Failure.Region == "" {
			report.Failure.Region = region
		}
	}
	fs := append(diag.List(nil), plan.Failures...)
	if report.Failure != nil && !slices.Contains(fs, *report.Failure) {
		fs = append(fs, *report.Failure)
	}
	diag.Sort(fs)
	if _, eerr := runner.EncodeStdout(format, rollbackResult{Plan: plan, Report: report, Failures: fs}); eerr != nil {
		return eerr
	}
	runner.ReportFailures(ui.Stderr, fs)
	if err != nil {
		return fmt.Errorf("%w (resume with: %s)", err, rollbackResumeCommand(cmd, plan.ClusterName))
	}
	return runner.IncompleteExit(plan.Failures)
}

// rollbackQuestion is the confirmation: what the run changes, in order.
func rollbackQuestion(plan *upgrade.RollbackPlan) string {
	var ngs, addons []string
	cp := false
	for _, s := range plan.Steps {
		if s.Status != upgrade.StatusPending {
			continue
		}
		switch s.Type {
		case upgrade.StepNodegroup:
			ngs = append(ngs, s.Target)
		case upgrade.StepAddon:
			addons = append(addons, s.Target+" → "+s.Version)
		case upgrade.StepControlPlane:
			cp = true
		}
	}
	var parts []string
	if len(ngs) > 0 {
		parts = append(parts, fmt.Sprintf("roll back %d nodegroup(s) to %s (%s); their nodes are replaced and pods rescheduled", len(ngs), plan.TargetVersion, strings.Join(ngs, ", ")))
	}
	if len(addons) > 0 {
		parts = append(parts, fmt.Sprintf("downgrade %d add-on(s) (%s)", len(addons), strings.Join(addons, ", ")))
	}
	if cp {
		parts = append(parts, fmt.Sprintf("roll back the control plane from %s to %s", plan.CurrentVersion, plan.TargetVersion))
	}
	return fmt.Sprintf("Roll back cluster %s to %s? This will, in order: %s. Proceed?", plan.ClusterName, plan.TargetVersion, strings.Join(parts, "; then "))
}

// rollbackResumeCommand is the command that resumes a stopped rollback,
// with every flag that decides what changes and where.
func rollbackResumeCommand(cmd *cli.Command, clusterName string) string {
	parts := []string{"refresh"}
	for _, name := range []string{"profile", "region"} {
		if cmd.IsSet(name) {
			if v := strings.TrimSpace(cmd.String(name)); v != "" {
				parts = append(parts, "--"+name, shellQuote(v))
			}
		}
	}
	parts = append(parts, "cluster", "rollback", "-c", shellQuote(clusterName))
	for _, v := range cmd.StringSlice("skip-nodegroup") {
		parts = append(parts, "--skip-nodegroup", shellQuote(v))
	}
	for _, name := range []string{"kubeconfig", "kube-context"} {
		if v := strings.TrimSpace(cmd.String(name)); v != "" {
			parts = append(parts, "--"+name, shellQuote(v))
		}
	}
	for _, name := range []string{"wait-timeout", "rollback-timeout"} {
		if cmd.IsSet(name) {
			parts = append(parts, "--"+name, shortDuration(cmd.Duration(name)))
		}
	}
	for _, name := range []string{"force", "skip-insights-check", "skip-health-check", "yes"} {
		if cmd.Bool(name) {
			parts = append(parts, "--"+name)
		}
	}
	return strings.Join(parts, " ")
}

// rollbackPlanLines is the human plan: the header, the notices, and the
// numbered steps.
func rollbackPlanLines(th *render.Theme, plan *upgrade.RollbackPlan) []string {
	head := fmt.Sprintf("Rollback plan: %s %s → %s", th.Bold(th.Pal.White, plan.ClusterName), plan.CurrentVersion, plan.TargetVersion)
	if plan.RolledBack {
		head = fmt.Sprintf("Rollback plan: %s (the control plane already rolled back to %s)", th.Bold(th.Pal.White, plan.ClusterName), plan.TargetVersion)
	} else if plan.AvailableUntil != nil {
		head += th.Paint(th.Pal.Dim, " (available until about "+plan.AvailableUntil.UTC().Format("2006-01-02 15:04 MST")+")")
	}
	out := []string{"", head}
	for _, n := range plan.Notices {
		out = append(out, "  "+th.Token(render.Warn, "notice: "+n))
	}
	width := 0
	for _, st := range []upgrade.StepStatus{upgrade.StatusCompleted, upgrade.StatusBlocked, upgrade.StatusManual, upgrade.StatusPending} {
		width = max(width, ui.VisibleWidth(stepToken(th, st)))
	}
	out = append(out, "")
	for i, step := range plan.Steps {
		line := fmt.Sprintf("  %2d. %s %s", i+1, ui.PadANSI(stepToken(th, step.Status), width, ui.AlignLeft), step.Description)
		if step.Reason != "" {
			line += th.Paint(th.Pal.Dim, " — "+step.Reason)
		}
		out = append(out, line)
	}
	return out
}

// rollbackOutcomeLines are the last lines of a run that did not fail.
func rollbackOutcomeLines(th *render.Theme, plan *upgrade.RollbackPlan, ran bool) []string {
	var head string
	switch {
	case ran:
		head = th.Line(render.Healthy, "Rollback complete: %s is at %s. EKS does not roll back add-on settings or workloads: check that they run correctly on %s.", plan.ClusterName, plan.TargetVersion, plan.TargetVersion)
	default:
		head = th.Line(render.Healthy, "Nothing to do: %s is rolled back to %s.", plan.ClusterName, plan.TargetVersion)
	}
	out := []string{"", head}
	for _, m := range plan.ManualSteps() {
		out = append(out, "  "+th.Token(render.Warn, "manual: "+m))
	}
	return out
}
