package nodegroup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/urfave/cli/v3"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/apidoc"
	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/dryrun"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/healthview"
	"github.com/dantech2000/refresh/internal/monitoring"
	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/rollview"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
)

// updateAMIFlags collects the flags that govern runUpdateAMI's behavior.
type updateAMIFlags struct {
	force, dryRun, noWait, quiet, skipHealthCheck, healthOnly bool
	yes, requireHealthy, skipVerify, changelog, live, reroll  bool
	timeout, pollInterval                                     time.Duration
	format                                                    string
	kubeconfig, kubeContext                                   string
}

// readUpdateAMIFlags reads and validates the update flags. Call it before any
// AWS call, so a bad value fails fast and never after a roll has started.
func readUpdateAMIFlags(cmd *cli.Command) (updateAMIFlags, error) {
	// A zero or negative poll interval would panic the monitor's ticker.
	if pi := cmd.Duration("poll-interval"); pi <= 0 {
		return updateAMIFlags{}, fmt.Errorf("--poll-interval must be greater than 0 (got %s)", pi)
	}
	// Flags placed after positional args (e.g. `nodegroup update my-cluster
	// --health-only`) are parsed natively by urfave/cli v3.
	return updateAMIFlags{
		force:           cmd.Bool("force"),
		reroll:          cmd.Bool("reroll"),
		dryRun:          cmd.Bool("dry-run"),
		noWait:          cmd.Bool("no-wait"),
		quiet:           cmd.Bool("quiet"),
		skipHealthCheck: cmd.Bool("skip-health-check"),
		healthOnly:      cmd.Bool("health-only"),
		yes:             cmd.Bool("yes"),
		requireHealthy:  cmd.Bool("require-healthy"),
		skipVerify:      cmd.Bool("skip-verify"),
		changelog:       cmd.Bool("changelog"),
		live:            cmd.Bool("live"),
		timeout:         runner.WaitTimeout(cmd, "timeout"),
		pollInterval:    cmd.Duration("poll-interval"),
		format:          strings.ToLower(cmd.String("format")),
		kubeconfig:      cmd.String("kubeconfig"),
		kubeContext:     cmd.String("kube-context"),
	}, nil
}

// isInteractive reports whether stdin is a terminal, so unattended runs (CI,
// cron) fail fast instead of blocking on a prompt that can never be answered.
// It is a var so tests can simulate a terminal.
var isInteractive = func() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// machine reports whether stdout carries one JSON/YAML document. In that mode
// every human line (progress, notices, warnings) goes to stderr or is
// suppressed, and nothing prompts: a confirmation needs --yes instead.
func (f updateAMIFlags) machine() bool {
	return runner.IsMachineFormat(f.format)
}

// machineHealthOutput reports whether the health verdict should be emitted as
// JSON/YAML instead of the human table (only meaningful with --health-only).
func (f updateAMIFlags) machineHealthOutput() bool {
	return f.healthOnly && f.machine()
}

// canPrompt reports whether the run may ask a question on the terminal. It
// can't without a TTY, and it doesn't with -o json/yaml or --quiet: a
// confirmation then needs --yes.
func (f updateAMIFlags) canPrompt() bool {
	return !f.machine() && !f.quiet && isInteractive()
}

// noPromptReason explains, in an error message, why no prompt was shown.
func (f updateAMIFlags) noPromptReason() string {
	switch {
	case f.machine():
		return "-o " + f.format + " does not prompt"
	case f.quiet:
		return "--quiet does not prompt"
	default:
		return "no interactive terminal for confirmation"
	}
}

// dryRunOptions maps the run flags to the dry-run preview options.
func (f updateAMIFlags) dryRunOptions() dryrun.Options {
	return dryrun.Options{Force: f.force, Reroll: f.reroll, Quiet: f.quiet}
}

// noticeOut is where per-nodegroup notices (skips, failures, warnings) go:
// stdout in the human view, stderr with -o json/yaml so stdout stays one
// document.
func (f updateAMIFlags) noticeOut() io.Writer {
	if f.machine() {
		return ui.Stderr
	}
	return os.Stdout
}

// notice writes one status line to noticeOut.
func (f updateAMIFlags) notice(st render.Status, format string, args ...any) {
	render.Notef(f.noticeOut(), st, format, args...)
}

// noticeDetail writes an indented continuation of the previous notice, with
// no status glyph of its own.
func (f updateAMIFlags) noticeDetail(format string, args ...any) {
	_, _ = fmt.Fprintf(f.noticeOut(), "  "+format+"\n", args...)
}

func runUpdateAMI(ctx context.Context, cmd *cli.Command) (err error) {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsDocument); err != nil {
		return err
	}
	if cmd.Bool("simulate") {
		if f := cmd.String("format"); runner.IsMachineFormat(f) {
			return fmt.Errorf("--simulate draws the live roll panel and has no -o %s output", strings.ToLower(f))
		}
		return rollview.SimulatedRoll(ctx, cmd.String("nodegroup"))
	}
	if cmd.Bool("all-clusters") {
		return runFleetUpdate(ctx, cmd)
	}
	flags, err := readUpdateAMIFlags(cmd)
	if err != nil {
		return err
	}

	// --wait-timeout <= 0 means no limit, here and in the monitor (not a 60s fallback).
	ctx, cancel, awsCfg, err := runner.SetupAWSWithDeadline(ctx, cmd, runner.WaitTimeout(cmd, "timeout"))
	if err != nil {
		return err
	}
	defer cancel()
	// The run deadline is --wait-timeout: a timeout names it.
	defer runner.WaitDeadlineHint(&err)

	requestedCluster, nodegroupPattern := updateClusterAndNodegroupPatterns(cmd)
	// -o json/yaml never prompts for a partial cluster name.
	clusterName, err := runner.ResolveClusterName(ctx, awsCfg, requestedCluster, cmd.String("format"))
	if err != nil {
		return err
	}
	eksClient := factory.NewEKSClient(awsCfg)

	// EKS refuses a second update while one runs, but only after the prompts:
	// say so first. A dry run or --health-only changes nothing, so it goes on.
	if !flags.dryRun && !flags.healthOnly {
		if busy := updateBusyChanges(ctx, eksClient, clusterName, nodegroupPattern); len(busy) > 0 {
			return runner.BusyExit(clusterName, busy)
		}
	}

	summary, done, err := preflightHealthCheck(ctx, awsCfg, eksClient, clusterName, nodegroupPattern, flags)
	if err != nil || done {
		return finishAtHealthGate(newUpdateRun(clusterName, awsCfg.Region), summary, flags, err)
	}

	selectedNodegroups, err := selectNodegroupsForUpdate(ctx, eksClient, clusterName, nodegroupPattern, flags)
	if err != nil {
		return err
	}

	// Pre-flight: a roll launches replacement nodes, so warn if an instance type
	// isn't offered in one of a nodegroup's AZs. Best-effort, non-blocking. (REF-143)
	if !flags.quiet {
		ngSvc := factory.NewNodegroupService(awsCfg, false, nil)
		for _, ng := range selectedNodegroups {
			warnInstanceTypeAvailabilityTo(ctx, flags.noticeOut(), ngSvc, clusterName, ng)
		}
	}

	if flags.dryRun {
		return runUpdateDryRun(ctx, awsCfg, eksClient, clusterName, selectedNodegroups, flags)
	}

	run, verifyFailed, monErr := executeUpdates(ctx, awsCfg, eksClient, clusterName, awsCfg.Region, selectedNodegroups, flags)
	doc := newUpdateDocument(run, summary)

	if flags.machine() {
		if _, err := runner.EncodeStdout(flags.format, doc); err != nil {
			return err
		}
	} else if !flags.quiet {
		printRunSummary(run, clusterName, flags.noWait)
	}
	runner.WriteFailures(flags.format, os.Stdout, ui.Stderr, doc.Failures)
	return runner.UnlessInterrupted(ctx, updateExit(run, doc.Failures, monErr, verifyFailed))
}

// finishAtHealthGate ends a run the health gate stopped, or a --health-only
// run. With -o json/yaml, --health-only prints the verdict, and a run the
// gate stopped prints the (empty) run document with the verdict, so a CI
// consumer can see which checks stopped it. The gate's error (and exit code)
// stands; the reads the checks could not make are named on stderr.
func finishAtHealthGate(run updateRun, summary *health.HealthSummary, flags updateAMIFlags, gateErr error) error {
	if summary == nil {
		return gateErr
	}
	if flags.machine() {
		var doc apidoc.Document = newUpdateDocument(run, summary)
		if flags.healthOnly {
			doc = summary
		}
		if _, err := runner.EncodeStdout(flags.format, doc); err != nil {
			return err
		}
	}
	runner.WriteFailures(flags.format, os.Stdout, ui.Stderr, healthFailures(run, summary))
	return gateErr
}

// runUpdateDryRun previews the selected nodegroups: the plan document with
// -o json/yaml, else the human preview. A nodegroup the preview could not
// describe is a failure (exit 4). The preview runs the PDB drain gate too,
// and exits 3 where the real run would refuse.
func runUpdateDryRun(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, selected []string, flags updateAMIFlags) error {
	var fs []diag.Failure
	var toRoll []string
	var blockers drainBlockers
	if flags.machine() {
		plan, err := dryRunDocument(ctx, awsCfg, eksClient, clusterName, selected, flags)
		if err != nil {
			return err
		}
		if _, err := runner.EncodeStdout(flags.format, plan); err != nil {
			return err
		}
		fs = plan.Failures
		toRoll, blockers = plan.drainBlockers()
	} else {
		result, err := dryrun.PerformDryRun(ctx, awsCfg, eksClient, clusterName, selected, flags.dryRunOptions())
		if err != nil {
			return err
		}
		toRoll = dryRunToRoll(result.UpdatesNeeded)
		var checked bool
		blockers, checked = drainGate(ctx, awsCfg, eksClient, clusterName, toRoll, flags, !flags.quiet)
		if checked {
			printDrainGate(os.Stdout, toRoll, blockers, flags.force)
		}
		if !flags.quiet {
			printChangelogsForNodegroups(ctx, awsCfg, eksClient, clusterName, selected, flags.changelog)
		}
		fs = dryRunFailures(clusterName, awsCfg.Region, result.Unreadable)
	}
	runner.WriteFailures(flags.format, os.Stdout, ui.Stderr, fs)
	if len(blockers) > 0 && !flags.force {
		return runner.UnlessInterrupted(ctx, drainBlockedExit(clusterName, toRoll, blockers))
	}
	return runner.UnlessInterrupted(ctx, runner.IncompleteExit(fs))
}

// dryRunToRoll names the nodegroups a preview would roll.
func dryRunToRoll(updates []dryrun.NodegroupUpdate) []string {
	var out []string
	for _, u := range updates {
		if u.Action == refreshTypes.ActionUpdate || u.Action == refreshTypes.ActionForceUpdate {
			out = append(out, u.Name)
		}
	}
	return out
}

// printRunSummary prints the human end of a single-cluster run: nothing
// started, the --no-wait hint, or the post-roll verification.
func printRunSummary(run updateRun, clusterName string, noWait bool) {
	switch started := run.started(); {
	case len(started) == 0:
		render.Notef(os.Stdout, render.Warn, "No nodegroup updates were started")
	case noWait:
		render.Notef(os.Stdout, render.Progress, "Started %d nodegroup update(s). Use 'refresh nodegroup list %s' to check status.",
			len(started), clusterName)
	case run.verification != nil:
		printVerification(*run.verification)
	}
}

// executeUpdates runs the mutating part of an update for one cluster: snapshot
// (for verification), start updates, monitor to completion, then verify. It
// returns the per-nodegroup outcomes, whether verification failed, and any
// monitoring error. Output/exit-code decisions are left to the caller so this
// is reusable by both the single-cluster and fleet paths.
func executeUpdates(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName, region string, selected []string, flags updateAMIFlags) (updateRun, bool, error) {
	verify := !flags.skipVerify && !flags.noWait
	var verifyClient kubernetes.Interface
	var preroll pendingPodSet
	var prerollOK bool
	if verify {
		verifyClient, _ = resolveHealthKubeClient(ctx, eksClient, awsCfg.Region, clusterName, flags.kubeconfig, flags.kubeContext, false)
		preroll, prerollOK = snapshotPendingPods(ctx, verifyClient)
	}

	gate := func(toRoll []string) drainBlockers {
		blockers, _ := drainGate(ctx, awsCfg, eksClient, clusterName, toRoll, flags, false)
		return blockers
	}
	updates, run := startNodegroupUpdates(ctx, awsCfg, clusterName, region, selected, flags, gate)
	if len(updates) == 0 || flags.noWait {
		return run, false, nil
	}

	// -o json/yaml keeps the monitor silent: stdout carries only the summary.
	quiet := flags.quiet || flags.machine()
	monitor := &refreshTypes.ProgressMonitor{
		Updates:   updates,
		StartTime: time.Now(),
	}
	config := refreshTypes.MonitorConfig{
		PollInterval: flags.pollInterval,
		Quiet:        quiet,
		Timeout:      flags.timeout,
	}
	// Live per-node roll view: the DEFAULT for an interactive single-nodegroup
	// roll (nodes draining/joining/terminating, pod eviction, warnings), where
	// interactive means stdout is a terminal with color on (see
	// showLivePanel). Piped/CI and NO_COLOR runs keep the monitor's progress
	// lines unless --live asks for the panel. Purely
	// visual: it runs alongside the EKS DescribeUpdate monitor, which stays
	// authoritative for the result and stops the panel once the update is
	// terminal (a failed roll never converges, so the panel can't be the gate).
	// The monitor is quiet only while the panel draws; if the panel has nothing
	// to show (unreachable cluster API, no labelled nodes, baseline failure) or
	// stops early, the monitor's normal progress output takes over. The kube
	// client is resolved quietly by default; --live makes the fallback reason
	// explicit when the cluster can't be reached. (REF-126)
	var livePanel func(context.Context)
	if showLivePanel(len(updates), quiet, flags.live, render.DetectLevel(os.Stdout) != render.ColorNone) {
		kube := verifyClient
		if kube == nil {
			kube, _ = resolveHealthKubeClient(ctx, eksClient, awsCfg.Region, clusterName, flags.kubeconfig, flags.kubeContext, flags.live)
		}
		if kube != nil {
			ng := updates[0].NodegroupName
			livePanel = func(pctx context.Context) {
				rollview.LiveRollForUpdate(pctx, kube, ng, flags.timeout, flags.pollInterval)
			}
		}
	}

	var monErr error
	heldBack := false
	if livePanel == nil {
		monErr = monitoring.MonitorUpdates(ctx, eksClient, monitor, config)
	} else {
		heldBack, monErr = monitorAlongsidePanel(ctx, livePanel, flags.timeout, func(mctx context.Context, q bool) error {
			config.Quiet = q
			return monitoring.MonitorUpdates(mctx, eksClient, monitor, config)
		})
	}
	if heldBack {
		// The panel has stopped: print what the quiet monitor held back.
		config.Quiet = false
		if monitoring.AllComplete(monitor) {
			monErr = monitoring.DisplayCompletionSummary(monitor, config)
		} else {
			monitoring.DisplayStopped(monitor, config, monErr)
		}
	}
	run.applyMonitorResult(monitor.Updates, monErr)

	verifyFailed := false
	// Verification is cluster-wide (new stuck pods), so it can't be scoped to
	// the updates that completed while an unmonitored roll may still be
	// running. Skip it, and say why.
	if verify && errors.Is(monErr, monitoring.ErrUnmonitored) && !quiet {
		render.Notef(os.Stdout, render.Warn, "Post-roll verification skipped: the outcome of one or more updates is unknown.")
	}
	if verify && shouldVerifyPostRoll(ctx, monErr) && len(run.started()) > 0 {
		result, readFailures := verifyPostRoll(ctx, eksClient, verifyClient, clusterName, run.started(), preroll, prerollOK)
		run.verification = &result
		for _, f := range readFailures {
			run.readFailures = append(run.readFailures, *run.withCluster(f))
		}
		verifyFailed = !result.OK()
	}
	return run, verifyFailed, monErr
}

// showLivePanel decides whether to draw the live roll panel. It only covers a
// single-nodegroup roll and never runs in quiet/JSON mode. By default it needs
// an interactive stdout: a terminal with color enabled (the render theme's
// level is ColorNone when stdout is not a terminal, NO_COLOR is set, or
// --no-color is given). Otherwise the panel would append a full frame to logs
// on every tick and hide the monitor's progress lines. --live overrides the
// terminal check; off a terminal the panel then appends throttled snapshots.
func showLivePanel(updates int, quiet, live, interactive bool) bool {
	if updates != 1 || quiet {
		return false
	}
	return live || interactive
}

// shouldVerifyPostRoll reports whether post-roll verification can run. It is
// skipped when monitoring failed (a Failed/Cancelled update, a timeout, or a
// user interrupt) or ctx is done: after Ctrl+C every call would fail with
// "context canceled" and report false issues.
func shouldVerifyPostRoll(ctx context.Context, monErr error) bool {
	return monErr == nil && ctx.Err() == nil
}

// printVerification renders the post-roll verification block.
func printVerification(v PostRollVerification) {
	for _, l := range verificationLines(render.Default(os.Stdout), v) {
		fmt.Println(l)
	}
}

// updateExit maps a single-cluster update run to the exit-code contract, in
// this order: exit 3 when the PDB drain gate refused the run; exit 1 for an interrupt, a monitoring timeout, or a started
// update that ended Failed or Cancelled or could not be monitored; exit 4
// for the other failures (a nodegroup that could not be read, an update that
// could not start); exit 5 when post-roll verification found issues. The
// failures themselves are named already (runner.WriteFailures), so the
// messages only count them.
func updateExit(run updateRun, fs []diag.Failure, monErr error, verifyFailed bool) error {
	check := fmt.Sprintf("check with 'refresh nodegroup list %s'", run.cluster)
	switch {
	case run.drainBlocked():
		// Nothing started: the refusal is the result, also when a
		// nodegroup could not be read (its failure is on stderr).
		return run.drainBlockedExit()
	case errors.Is(monErr, monitoring.ErrCancelled):
		return fmt.Errorf("%w; %s", monErr, check)
	case errors.Is(monErr, monitoring.ErrMonitorTimeout):
		return cli.Exit("monitoring timed out before every update finished (--wait-timeout); the EKS update(s) may still be running; "+check, runner.ExitError)
	case run.rollFailed():
		return cli.Exit(fmt.Sprintf("%d nodegroup update(s) did not succeed", run.rollFailures()), runner.ExitError)
	case len(fs) > 0:
		return runner.IncompleteExit(fs)
	case verifyFailed:
		return cli.Exit("update completed but post-roll verification found issues", runner.ExitVerifyFailed)
	}
	return nil
}

// preflightHealthCheck runs the pre-update health checks. Returns done=true if
// the caller should stop here: a block decision, a warning with
// --require-healthy or with no way to prompt, a user cancel, or --health-only.
//
// summary is the verdict whenever a check ran (nil when it was skipped), for
// the -o json/yaml document: the single-cluster path encodes it, and the
// fleet path stores it per cluster, so stdout gets one document per run.
// With -o json/yaml the report goes to stderr unless --quiet or --health-only
// (where the verdict is the document), and with --health-only err carries the
// verdict's exit code (0/2/3).
func preflightHealthCheck(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName, nodegroupPattern string, flags updateAMIFlags) (summary *health.HealthSummary, done bool, err error) {
	// Only --skip-health-check and --dry-run disable the health gate. --force is
	// deliberately NOT here: it only sets UpdateNodegroupVersion.Force (forcing
	// PDB-drain eviction) and must not silently bypass the pre-flight checks.
	if flags.skipHealthCheck || flags.dryRun {
		if flags.healthOnly {
			// No check runs, so there is no verdict to encode. Fail instead
			// of breaking the one-document contract of -o json/yaml.
			if flags.machine() {
				return nil, true, fmt.Errorf("--health-only with --skip-health-check or --dry-run runs no health check, so there is no -o %s result", flags.format)
			}
			render.Notef(os.Stdout, render.Warn, "Health check skipped due to --skip-health-check or --dry-run flags")
			return nil, true, nil
		}
		return nil, false, nil
	}

	// The human view prints the banner, spinner, and report on stdout. With
	// -o json/yaml stdout is pure data: the report goes to stderr instead, so
	// a CI log still shows why a run stopped. The kube-client diagnostics
	// always go to stderr.
	humanOutput := !flags.quiet && !flags.machine()

	if humanOutput {
		healthview.WriteStart(os.Stdout, clusterName)
	}
	k8sClient, kubeSel := resolveHealthKubeClient(ctx, eksClient, awsCfg.Region, clusterName, flags.kubeconfig, flags.kubeContext, !flags.quiet)
	checker := health.NewCheckerForConfig(awsCfg, k8sClient, nil)
	// Attach metrics-server (best-effort) for live CPU+memory drain headroom; the
	// utilization check skips cleanly if it isn't installed. (REF-142)
	if k8sClient != nil {
		if m, mErr := health.BuildMetricsClient(kubeSel); mErr == nil {
			checker.SetNodeMetrics(m)
		}
	}
	// Scope the PDB drain-blocker check to the nodegroups this run may roll, so
	// a PDB whose pods live only on other nodegroups or Fargate doesn't warn.
	// Best-effort: if the list fails the check stays cluster-wide.
	if k8sClient != nil {
		checker.SetTargetNodegroups(healthTargetNodegroups(ctx, eksClient, clusterName, nodegroupPattern))
	}

	spinner := ui.NewFunSpinnerForCategory("health")
	if humanOutput {
		if err := spinner.Start(); err != nil {
			return nil, false, err
		}
		defer spinner.Stop()
	}
	result := checker.RunAllChecks(ctx, clusterName)
	if humanOutput {
		render.SpinnerDone(spinner, "Health checks complete")
		healthview.WriteReport(os.Stdout, result)
	}
	// With --health-only the verdict is the document itself.
	if flags.machine() && !flags.healthOnly && !flags.quiet {
		healthview.WriteReport(ui.Stderr, result)
	}

	if flags.machineHealthOutput() {
		return &result, true, healthExitError(result)
	}

	done, err = applyHealthDecision(ctx, result, flags)
	return &result, done, err
}

// healthExitError maps a health verdict to the --health-only exit-code
// contract: 0 = pass, 2 = warnings, 3 = blocked, and 4 for a pass whose
// checks could not read everything (summary.Failures). Messages go to
// stderr via urfave/cli, keeping stdout pure data for JSON/YAML output.
func healthExitError(summary health.HealthSummary) error {
	switch summary.Decision {
	case health.DecisionBlock:
		return cli.Exit("pre-flight health checks failed: "+healthProblems(summary), 3)
	case health.DecisionWarn:
		return cli.Exit("health checks completed with warnings: "+healthProblems(summary), 2)
	default:
		return runner.IncompleteExit(summary.Failures)
	}
}

// healthProblems names the checks behind a verdict, for error messages and
// notices: the blocking failures for BLOCK, else the checks that warned or
// failed. Each is "Name: message", joined with "; ".
func healthProblems(summary health.HealthSummary) string {
	var out []string
	for _, r := range summary.Results {
		if r.Skipped {
			continue
		}
		failed := r.Status == health.StatusFail
		if summary.Decision == health.DecisionBlock && (!failed || !r.IsBlocking) {
			continue
		}
		if !failed && r.Status != health.StatusWarn {
			continue
		}
		out = append(out, r.Name+": "+r.Message)
	}
	if len(out) == 0 {
		// No per-check results: fall back to the summary's messages.
		out = append(append(out, summary.Errors...), summary.Warnings...)
	}
	if len(out) == 0 {
		return "no check details reported"
	}
	return strings.Join(out, "; ")
}

// applyHealthDecision interprets a health summary against the run flags. It
// returns done=true when the caller should stop (Block decision, user
// cancelled, or --health-only).
//
// --health-only means "just show me the verdict": the success banner is
// printed regardless of --quiet because the verdict IS the requested result.
// --quiet only suppresses the verbose banner for the non-health-only flow.
//
// With --health-only the exit code encodes the verdict so CI can gate on it
// without parsing output: 0 = pass, 2 = warnings, 3 = blocked.
//
// With -o json/yaml the human banners are not printed (the returned error
// reaches stderr) and a warning never prompts: proceeding needs --yes.
func applyHealthDecision(ctx context.Context, summary health.HealthSummary, flags updateAMIFlags) (done bool, err error) {
	human := !flags.machine()
	switch summary.Decision {
	case health.DecisionBlock:
		if human {
			healthview.WriteVerdict(os.Stdout, summary.Decision, flags.healthOnly)
		}
		if flags.healthOnly {
			return true, healthExitError(summary)
		}
		// Exit 3 on a blocked gate, as documented, for the human and
		// -o json/yaml paths alike.
		return true, healthExitError(summary)
	case health.DecisionWarn:
		if flags.healthOnly {
			if human {
				healthview.WriteVerdict(os.Stdout, summary.Decision, flags.healthOnly)
			}
			return true, healthExitError(summary)
		}
		// --require-healthy turns warnings into a hard stop (the strict-pipeline
		// knob) instead of a prompt.
		if flags.requireHealthy {
			if human {
				healthview.WriteVerdict(os.Stdout, summary.Decision, flags.healthOnly)
			}
			return true, cli.Exit("health checks reported warnings and --require-healthy is set: "+healthProblems(summary), 2)
		}
		// --yes proceeds past warnings without prompting.
		if flags.yes {
			// Surface the auto-accepted warnings so an operator (e.g. a fleet
			// update that auto-accepts) sees them instead of proceeding
			// silently. With -o json/yaml this goes to stderr.
			if !flags.quiet {
				flags.notice(render.Warn, "Health check reported warnings; proceeding: %s", healthProblems(summary))
			}
			return false, nil
		}
		// Without a TTY (CI/cron), with -o json/yaml, or with --quiet, and
		// without --yes, stop rather than prompt. --quiet hides the health
		// report, so it must never accept the warnings on the user's behalf.
		if !flags.canPrompt() {
			return true, fmt.Errorf("health checks reported warnings (%s); re-run with --yes to proceed or --require-healthy to fail (%s)", healthProblems(summary), flags.noPromptReason())
		}
		if !ui.PromptContinueWithWarnings(ctx, summary.Warnings) {
			render.Notef(os.Stdout, render.Warn, "Update cancelled by user")
			return true, fmt.Errorf("update cancelled")
		}
	case health.DecisionProceed:
		if human && (flags.healthOnly || !flags.quiet) {
			healthview.WriteVerdict(os.Stdout, summary.Decision, flags.healthOnly)
		}
		if flags.healthOnly {
			return true, healthExitError(summary)
		}
	}
	return false, nil
}

// listNodegroupNames returns every managed nodegroup name in the cluster.
func listNodegroupNames(ctx context.Context, eksClient *eks.Client, clusterName string) ([]string, error) {
	return awsinternal.ListAllPages(ctx, "listing nodegroups",
		func(rc context.Context, token *string) (*eks.ListNodegroupsOutput, error) {
			return eksClient.ListNodegroups(rc, &eks.ListNodegroupsInput{ClusterName: aws.String(clusterName), NextToken: token})
		},
		func(out *eks.ListNodegroupsOutput) ([]string, *string) { return out.Nodegroups, out.NextToken },
	)
}

// healthTargetNodegroups returns the nodegroups matching pattern, before any
// interactive narrowing, for scoping the pre-flight PDB check. It is a superset
// of the final selection, so no real blocker is hidden. Returns nil on error,
// which leaves the check cluster-wide.
func healthTargetNodegroups(ctx context.Context, eksClient *eks.Client, clusterName, pattern string) []string {
	names, err := listNodegroupNames(ctx, eksClient, clusterName)
	if err != nil {
		return nil
	}
	return awsinternal.MatchingNodegroups(names, pattern)
}

// selectNodegroupsForUpdate lists nodegroups matching pattern and confirms the
// selection when the pattern is not an exact nodegroup name. It returns errors
// without printing them: main (or the fleet summary) reports each error once,
// on stderr.
func selectNodegroupsForUpdate(ctx context.Context, eksClient *eks.Client, clusterName, pattern string, flags updateAMIFlags) ([]string, error) {
	names, err := listNodegroupNames(ctx, eksClient, clusterName)
	if err != nil {
		return nil, &listNodegroupsError{err: err}
	}
	matches := awsinternal.MatchingNodegroups(names, pattern)
	if len(matches) == 0 {
		return nil, &noMatchError{cluster: clusterName, pattern: pattern}
	}
	// A pattern that is not an exact name (one substring match, or several
	// matches) normally prompts. --yes accepts the matches; without a TTY or
	// with -o json/yaml, and without --yes, fail instead of hanging on a
	// prompt or rolling a nodegroup nobody named.
	if awsinternal.NodegroupPatternNeedsConfirmation(matches, pattern) {
		if flags.yes {
			if len(matches) == 1 {
				render.Notef(ui.Stderr, render.Warn, "No nodegroup named %q in %s; using the only partial match %q (--yes)", pattern, clusterName, matches[0])
			}
			return matches, nil
		}
		if !flags.canPrompt() {
			return nil, nodegroupPatternError(clusterName, pattern, matches, flags.noPromptReason())
		}
	}
	return awsinternal.ConfirmNodegroupSelection(ctx, matches, pattern)
}

// nodegroupPatternError explains why a non-exact pattern was not accepted
// without a prompt, naming the candidates and the pattern.
func nodegroupPatternError(clusterName, pattern string, matches []string, reason string) error {
	if len(matches) == 1 {
		return fmt.Errorf("no nodegroup named %q in %s (partial match: %s); re-run with --yes to update it, or pass the exact name (%s)", pattern, clusterName, matches[0], reason)
	}
	return fmt.Errorf("pattern %q matched %d nodegroups (%s); re-run with --yes to update all, or a more specific name (%s)", pattern, len(matches), strings.Join(matches, ", "), reason)
}

// dryRunNodegroup is one nodegroup's previewed action in a -o json/yaml
// dry-run document.
type dryRunNodegroup struct {
	Name string `json:"name" yaml:"name"`
	// Action is Update, ForceUpdate, SkipUpdating, SkipLatest,
	// SkipCustom, or Unknown (the nodegroup could not be read; see
	// Failure).
	Action     dryrun.Action `json:"action" yaml:"action"`
	CurrentAMI string        `json:"currentAmi,omitempty" yaml:"currentAmi,omitempty"`
	LatestAMI  string        `json:"latestAmi,omitempty" yaml:"latestAmi,omitempty"`
	Reason     string        `json:"reason" yaml:"reason"`
	// Failure is set when the action is unknown. The same failure is in the
	// document's failures.
	Failure *diag.Failure `json:"failure,omitempty" yaml:"failure,omitempty"`
	// DrainBlockers names what would stop EKS draining a nodegroup the run
	// would roll. Without --force the real run refuses (exit 3).
	DrainBlockers []string `json:"drainBlockers,omitempty" yaml:"drainBlockers,omitempty"`
}

// dryRunPlan is the -o json/yaml document for `nodegroup update --dry-run`.
type dryRunPlan struct {
	Cluster    string            `json:"cluster" yaml:"cluster"`
	DryRun     bool              `json:"dryRun" yaml:"dryRun"`
	Force      bool              `json:"force" yaml:"force"`
	Reroll     bool              `json:"reroll,omitempty" yaml:"reroll,omitempty"`
	Nodegroups []dryRunNodegroup `json:"nodegroups" yaml:"nodegroups"`
	Failures   diag.List         `json:"failures" yaml:"failures"`
}

// DocumentKind is NodegroupUpdatePlan.
func (dryRunPlan) DocumentKind() apidoc.Kind { return apidoc.KindNodegroupUpdatePlan }

// drainBlockers returns the nodegroups the plan would roll and their drain
// blockers.
func (p dryRunPlan) drainBlockers() ([]string, drainBlockers) {
	var toRoll []string
	blockers := drainBlockers{}
	for _, ng := range p.Nodegroups {
		if ng.Action != dryrun.ActionUpdate && ng.Action != dryrun.ActionForceUpdate {
			continue
		}
		toRoll = append(toRoll, ng.Name)
		if len(ng.DrainBlockers) > 0 {
			blockers[ng.Name] = ng.DrainBlockers
		}
	}
	return toRoll, blockers
}

// drainBlocked reports whether the real run would refuse the plan.
func (p dryRunPlan) drainBlocked() bool {
	_, blockers := p.drainBlockers()
	return len(blockers) > 0 && !p.Force
}

// dryRunFailure is the failure of a nodegroup the preview could not
// describe.
func dryRunFailure(cluster, region string, u dryrun.NodegroupUpdate) diag.Failure {
	err := u.Err
	if err == nil {
		err = errors.New(u.Reason)
	}
	f := diag.FromError(diag.KindNodegroup, u.Name, diag.OpDescribeNodegroup, err)
	f.Cluster, f.Region = cluster, region
	return f
}

// dryRunFailures returns the sorted failures of the unreadable nodegroups of
// a preview.
func dryRunFailures(cluster, region string, unreadable []dryrun.NodegroupUpdate) diag.List {
	var fs diag.List
	for _, u := range unreadable {
		fs = append(fs, dryRunFailure(cluster, region, u))
	}
	diag.Sort(fs)
	return fs
}

// dryRunDocument previews the selected nodegroups without printing, for
// -o json/yaml. A nodegroup that could not be described has the action
// unknown and a failure.
func dryRunDocument(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, selected []string, flags updateAMIFlags) (dryRunPlan, error) {
	updates, err := dryrun.Preview(ctx, awsCfg, eksClient, clusterName, selected, flags.dryRunOptions())
	if err != nil {
		return dryRunPlan{}, err
	}
	plan := dryRunPlan{Cluster: clusterName, DryRun: true, Force: flags.force, Reroll: flags.reroll, Nodegroups: make([]dryRunNodegroup, 0, len(updates))}
	var unreadable []dryrun.NodegroupUpdate
	for _, u := range updates {
		ng := dryRunNodegroup{
			Name:       u.Name,
			Action:     dryrun.ActionName(u.Action),
			CurrentAMI: u.CurrentAMI,
			LatestAMI:  u.LatestAMI,
			Reason:     u.Reason,
		}
		if u.Action == refreshTypes.ActionUnknown {
			f := dryRunFailure(clusterName, awsCfg.Region, u)
			ng.Failure = &f
			unreadable = append(unreadable, u)
		}
		plan.Nodegroups = append(plan.Nodegroups, ng)
	}
	plan.Failures = dryRunFailures(clusterName, awsCfg.Region, unreadable)
	blockers, _ := drainGate(ctx, awsCfg, eksClient, clusterName, dryRunToRoll(updates), flags, !flags.quiet)
	for i := range plan.Nodegroups {
		plan.Nodegroups[i].DrainBlockers = blockers[plan.Nodegroups[i].Name]
	}
	return plan, nil
}

// startNodegroupUpdates starts a version update, through the nodegroup
// service, for each selected nodegroup that isn't already updating or already
// on the latest AMI, returning successful update progress entries and one
// result per nodegroup. A nodegroup that can't be described or whose update
// can't start is recorded as Failed with its failure (exit 4), and the loop
// moves on to the next nodegroup. The caller reports the failures.
//
// Each nodegroup's action comes from nodegroupsvc.DecideAMIUpdate, the table
// the dry-run preview uses too, so the real run does what `--dry-run`
// promised. Every nodegroup is decided before any update starts, so gate
// sees the whole roll: when it returns blockers (and --force is not set),
// nothing starts and the nodegroups to roll are DrainBlocked. A nil gate
// passes.
func startNodegroupUpdates(ctx context.Context, awsCfg aws.Config, clusterName, region string, nodegroups []string, flags updateAMIFlags, gate func(toRoll []string) drainBlockers) ([]refreshTypes.UpdateProgress, updateRun) {
	// Progress lines are human-only. Skip notices always print: to stderr
	// with -o json/yaml (see noticeOut).
	human := !flags.quiet && !flags.machine()

	ngSvc := factory.NewNodegroupService(awsCfg, false, nil)
	decide := ngSvc.AMIUpdateDecider(clusterName, nodegroupsvc.AMIUpdateOptions{Force: flags.force, Reroll: flags.reroll})
	run := newUpdateRun(clusterName, region)
	updates := make([]refreshTypes.UpdateProgress, 0, len(nodegroups))

	// Decide. A nodegroup to roll holds its place in run.nodegroups (in
	// selection order) until the start below fills it in.
	var toRoll []string
	slot := map[string]int{}
	for _, ng := range nodegroups {
		if ctx.Err() != nil {
			run.notAttempted(ctx, ng)
			continue
		}
		nodegroup, err := ngSvc.DescribeNodegroup(ctx, clusterName, ng)
		if err != nil {
			run.fail(ng, diag.OpDescribeNodegroup, err)
			continue
		}
		switch decide(ctx, nodegroup).Action {
		case refreshTypes.ActionSkipCustom:
			// EKS doesn't manage the AMI (it lives in the user's launch
			// template), so UpdateNodegroupVersion can't pick a recommended
			// AMI. Skip with clear guidance instead of mis-rolling.
			flags.notice(render.Warn, "Nodegroup %s uses a custom AMI (AmiType=CUSTOM); refresh can't select a recommended AMI.", ng)
			flags.noticeDetail("Publish a new launch template version with the new AMI, then point the nodegroup at it (e.g. `aws eks update-nodegroup-version --launch-template name=<lt>,version=<n>`).")
			run.skip(ng, skipCustomAMI)
			continue
		case refreshTypes.ActionSkipUpdating:
			flags.notice(render.Progress, "Nodegroup %s is already UPDATING. Skipping update.", ng)
			run.skip(ng, skipAlreadyUpdating)
			continue
		case refreshTypes.ActionSkipLatest:
			flags.notice(render.Healthy, "Nodegroup %s is already on the latest AMI. Skipping (use --reroll to roll it anyway).", ng)
			run.skip(ng, skipAlreadyLatest)
			continue
		}
		slot[ng] = len(run.nodegroups)
		run.nodegroups = append(run.nodegroups, nodegroupResult{Name: ng})
		toRoll = append(toRoll, ng)
	}

	if gate != nil && ctx.Err() == nil {
		if blockers := gate(toRoll); len(blockers) > 0 && !flags.force {
			for _, ng := range toRoll {
				run.nodegroups[slot[ng]] = nodegroupResult{Name: ng, Status: ngDrainBlocked, DrainBlockers: blockers[ng]}
			}
			run.drainBlockers = blockers
			return updates, run
		}
	}

	// Start.
	for _, ng := range toRoll {
		i := slot[ng]
		if ctx.Err() != nil {
			run.nodegroups[i] = run.notAttemptedResult(ctx, ng)
			continue
		}
		if human {
			render.Notef(os.Stdout, render.Progress, "Starting update for nodegroup %s...", ng)
		}

		// The service pins one ClientRequestToken across its retries, so a
		// throttled or dropped request can't start a second roll. A new run or
		// a fleet revisit sends a new request; the UPDATING skip above is what
		// keeps it from rolling the nodegroup again.
		update, err := ngSvc.StartVersionUpdate(ctx, clusterName, ng, nodegroupsvc.VersionUpdateOptions{Force: flags.force})
		if err != nil {
			run.nodegroups[i] = run.failed(ng, diag.OpUpdateNodegroupVersion, err)
			continue
		}
		if update == nil || update.Id == nil {
			run.nodegroups[i] = run.failed(ng, diag.OpUpdateNodegroupVersion, errors.New("UpdateNodegroupVersion returned no update ID"))
			continue
		}

		now := time.Now()
		updates = append(updates, refreshTypes.UpdateProgress{
			NodegroupName: ng,
			UpdateID:      *update.Id,
			ClusterName:   clusterName,
			Status:        update.Status,
			StartTime:     now,
			LastChecked:   now,
		})
		run.nodegroups[i] = nodegroupResult{Name: ng, Status: ngStarted, UpdateID: *update.Id}
		if human {
			render.Notef(os.Stdout, render.Healthy, "Update started for nodegroup %s (ID: %s)", ng, *update.Id)
		}
	}
	return updates, run
}

// clusterEnvVar supplies the cluster for `nodegroup update` when the command
// line does not name one. No other command reads it.
const clusterEnvVar = "EKS_CLUSTER_NAME"

// clusterEnvNoteOut receives the note printed when the cluster comes from
// clusterEnvVar. It is a var so tests can capture it.
var clusterEnvNoteOut io.Writer = ui.Stderr

// updateClusterAndNodegroupPatterns resolves the (cluster, nodegroup) slots
// from flags, positionals and EKS_CLUSTER_NAME:
//
//   - --cluster/-c on the command line wins.
//   - A positional cluster wins over EKS_CLUSTER_NAME when it can only be the
//     cluster: --nodegroup/-n is set, or there are two positionals.
//   - Otherwise EKS_CLUSTER_NAME is the cluster and a lone positional is the
//     nodegroup pattern (the behavior before the env var stopped being a
//     --cluster flag source). A note on stderr names the cluster.
//
// The env var is read here and not as a flag source because urfave/cli
// reports an env-sourced flag as set, which let it override
// `nodegroup update prod --nodegroup ng-a` and roll ng-a on the env cluster.
func updateClusterAndNodegroupPatterns(cmd *cli.Command) (string, string) {
	env := strings.TrimSpace(os.Getenv(clusterEnvVar))
	args := cmd.Args().Slice()
	positionalIsCluster := len(args) >= 2 || (len(args) == 1 && cmd.IsSet("nodegroup"))
	if env == "" || cmd.IsSet("cluster") || positionalIsCluster {
		return runner.RequestedCluster(cmd), runner.PositionalSlot(cmd, "nodegroup", "cluster")
	}

	render.Notef(clusterEnvNoteOut, render.Neutral, "Using cluster %s from %s", env, clusterEnvVar)
	nodegroupPattern := strings.TrimSpace(cmd.String("nodegroup"))
	if !cmd.IsSet("nodegroup") && len(args) == 1 {
		nodegroupPattern = args[0]
	}
	return env, nodegroupPattern
}
