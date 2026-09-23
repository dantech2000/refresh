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
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/fatih/color"
	"github.com/urfave/cli/v3"
	"k8s.io/client-go/kubernetes"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/dryrun"
	"github.com/dantech2000/refresh/internal/health"
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
	yes, requireHealthy, skipVerify, changelog, live          bool
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
	// Flags placed after positional args (e.g. `update-ami my-cluster
	// --health-only`) are parsed natively by urfave/cli v3.
	return updateAMIFlags{
		force:           cmd.Bool("force"),
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
		timeout:         cmd.Duration("timeout"),
		pollInterval:    cmd.Duration("poll-interval"),
		format:          strings.ToLower(cmd.String("format")),
		kubeconfig:      cmd.String("kubeconfig"),
		kubeContext:     cmd.String("kube-context"),
	}, nil
}

// isInteractive reports whether stdin is a terminal, so unattended runs (CI,
// cron) fail fast instead of blocking on a prompt that can never be answered.
func isInteractive() bool {
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
// can't without a TTY, and it doesn't with -o json/yaml.
func (f updateAMIFlags) canPrompt() bool {
	return !f.machine() && isInteractive()
}

// noPromptReason explains, in an error message, why no prompt was shown.
func (f updateAMIFlags) noPromptReason() string {
	if f.machine() {
		return "-o " + f.format + " does not prompt"
	}
	return "no interactive terminal for confirmation"
}

// noticeOut is where per-nodegroup notices (skips, failures, warnings) go:
// stdout in the human view, stderr with -o json/yaml so stdout stays one
// document.
func (f updateAMIFlags) noticeOut() io.Writer {
	if f.machine() {
		return os.Stderr
	}
	return os.Stdout
}

// notice writes one colored line to noticeOut.
func (f updateAMIFlags) notice(attr color.Attribute, format string, args ...any) {
	_, _ = color.New(attr).Fprintf(f.noticeOut(), format+"\n", args...)
}

func runUpdateAMI(ctx context.Context, cmd *cli.Command) error {
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

	// --timeout <= 0 means no limit, here and in the monitor (not a 60s fallback).
	ctx, cancel, awsCfg, err := runner.SetupAWSWithDeadline(ctx, cmd, cmd.Duration("timeout"))
	if err != nil {
		return err
	}
	defer cancel()

	requestedCluster, nodegroupPattern := updateClusterAndNodegroupPatterns(cmd)
	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, requestedCluster)
	if err != nil {
		return err
	}
	eksClient := eks.NewFromConfig(awsCfg)

	summary, done, err := preflightHealthCheck(ctx, awsCfg, eksClient, clusterName, nodegroupPattern, flags)
	if err != nil || done {
		// -o json/yaml: --health-only prints the verdict; a run the health
		// gate stopped prints the (empty) run summary with the verdict, so
		// a CI consumer can see which checks stopped it.
		if flags.machine() && summary != nil {
			var doc any = updateDocument{updateOutcomes: updateOutcomes{Cluster: clusterName}, Health: summary}
			if flags.healthOnly {
				doc = summary
			}
			if _, eerr := runner.EncodeStdout(flags.format, doc); eerr != nil {
				return eerr
			}
		}
		return err
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
		if flags.machine() {
			plan, perr := dryRunDocument(ctx, awsCfg, eksClient, clusterName, selectedNodegroups, flags.force)
			if perr != nil {
				return perr
			}
			_, perr = runner.EncodeStdout(flags.format, plan)
			return perr
		}
		if derr := dryrun.PerformDryRun(ctx, awsCfg, eksClient, clusterName, selectedNodegroups, flags.force, flags.quiet); derr != nil {
			return derr
		}
		if !flags.quiet {
			printChangelogsForNodegroups(ctx, awsCfg, eksClient, clusterName, selectedNodegroups, flags.changelog)
		}
		return nil
	}

	outcomes, verifyFailed, monErr := executeUpdates(ctx, awsCfg, eksClient, clusterName, selectedNodegroups, flags)

	if flags.machine() {
		if _, err := runner.EncodeStdout(flags.format, updateDocument{updateOutcomes: outcomes, Health: summary}); err != nil {
			return err
		}
		return updateExit(outcomes, monErr, verifyFailed)
	}
	switch {
	case len(outcomes.Started) == 0:
		if !flags.quiet {
			color.Yellow("No nodegroup updates were started")
		}
	case flags.noWait:
		if !flags.quiet {
			fmt.Printf("Started %d nodegroup update(s). Use 'refresh nodegroup list %s' to check status.\n",
				len(outcomes.Started), clusterName)
		}
	default:
		if !flags.quiet && outcomes.Verification != nil {
			printVerification(*outcomes.Verification)
		}
	}
	return updateExit(outcomes, monErr, verifyFailed)
}

// executeUpdates runs the mutating part of an update for one cluster: snapshot
// (for verification), start updates, monitor to completion, then verify. It
// returns the per-nodegroup outcomes, whether verification failed, and any
// monitoring error. Output/exit-code decisions are left to the caller so this
// is reusable by both the single-cluster and fleet paths.
func executeUpdates(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, selected []string, flags updateAMIFlags) (updateOutcomes, bool, error) {
	verify := !flags.skipVerify && !flags.noWait
	var verifyClient kubernetes.Interface
	var preroll pendingPodSet
	var prerollOK bool
	if verify {
		verifyClient, _ = resolveHealthKubeClient(ctx, eksClient, awsCfg.Region, clusterName, flags.kubeconfig, flags.kubeContext, false)
		preroll, prerollOK = snapshotPendingPods(ctx, verifyClient)
	}

	updates, outcomes := startNodegroupUpdates(ctx, awsCfg, eksClient, clusterName, selected, flags)
	if len(updates) == 0 || flags.noWait {
		return outcomes, false, nil
	}

	// -o json/yaml keeps the monitor silent: stdout carries only the summary.
	quiet := flags.quiet || flags.machine()
	monitor := &refreshTypes.ProgressMonitor{
		Updates:   updates,
		StartTime: time.Now(),
		Quiet:     quiet,
		Timeout:   flags.timeout,
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
			monitor.Quiet, config.Quiet = q, q
			return monitoring.MonitorUpdates(mctx, eksClient, monitor, config)
		})
	}
	if heldBack {
		// The panel has stopped: print what the quiet monitor held back.
		monitor.Quiet, config.Quiet = false, false
		if monitoring.AllComplete(monitor) {
			monErr = monitoring.DisplayCompletionSummary(monitor, config)
		} else {
			monitoring.DisplayStopped(monitor, config, monErr)
		}
	}

	verifyFailed := false
	// Verification is cluster-wide (new stuck pods), so it can't be scoped to
	// the updates that completed while an unmonitored roll may still be
	// running. Skip it, and say why.
	if verify && errors.Is(monErr, monitoring.ErrUnmonitored) && !quiet {
		color.Yellow("Post-roll verification skipped: the outcome of one or more updates is unknown.")
	}
	if verify && shouldVerifyPostRoll(ctx, monErr) && len(outcomes.Started) > 0 {
		result := verifyPostRoll(ctx, eksClient, verifyClient, clusterName, outcomes.Started, preroll, prerollOK)
		outcomes.Verification = &result
		verifyFailed = !result.OK()
	}
	return outcomes, verifyFailed, monErr
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
	if v.OK() {
		color.Green("Post-roll verification passed:")
		for _, c := range v.Checks {
			fmt.Printf("  ✓ %s\n", c)
		}
		return
	}
	color.Red("Post-roll verification found issues:")
	for _, issue := range v.Issues {
		fmt.Printf("  ✖ %s\n", issue)
	}
	for _, c := range v.Checks {
		fmt.Printf("  ✓ %s\n", c)
	}
}

// updateExit maps an update run to the exit-code contract: monitoring failures
// propagate (exit 1), start failures yield exit 4, a successful roll whose
// post-roll verification found issues yields exit 5, otherwise success. A user
// interrupt exits 1 with a hint that the EKS update keeps running.
func updateExit(o updateOutcomes, monErr error, verifyFailed bool) error {
	if errors.Is(monErr, monitoring.ErrCancelled) {
		return fmt.Errorf("%w; check with 'refresh nodegroup list %s'", monErr, o.Cluster)
	}
	if monErr != nil {
		return monErr
	}
	if len(o.Failed) > 0 {
		return cli.Exit(fmt.Sprintf("%d nodegroup update(s) failed to start", len(o.Failed)), 4)
	}
	if verifyFailed {
		return cli.Exit("update completed but post-roll verification found issues", 5)
	}
	return nil
}

// preflightHealthCheck runs the pre-update health checks. Returns done=true if
// the caller should stop here (block decision, user cancelled, or --health-only).
//
// summary is the verdict whenever a check ran (nil when it was skipped), for
// the -o json/yaml document: the single-cluster path encodes it, and the
// fleet path stores it per cluster, so stdout gets one document per run.
// With -o json/yaml the report goes to stderr (unless --quiet), and with
// --health-only err carries the verdict's exit code (0/2/3).
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
			color.Yellow("Health check skipped due to --skip-health-check or --dry-run flags")
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
		ui.DisplayHealthCheckStart(clusterName)
	}
	cwClient := cloudwatch.NewFromConfig(awsCfg)
	asgClient := autoscaling.NewFromConfig(awsCfg)
	k8sClient, kubeSel := resolveHealthKubeClient(ctx, eksClient, awsCfg.Region, clusterName, flags.kubeconfig, flags.kubeContext, !flags.quiet)
	checker := health.NewChecker(eksClient, k8sClient, cwClient, asgClient)
	// Attach metrics-server (best-effort) for live CPU+memory drain headroom; the
	// utilization check skips cleanly if it isn't installed. (REF-142)
	if k8sClient != nil {
		if m, mErr := health.BuildMetricsClient(kubeSel); mErr == nil {
			checker.SetNodeMetrics(m)
		}
	}
	// EC2 vCPU quota headroom — a roll surges new nodes against the account
	// quota; the check skips cleanly if it can't read the limit/usage. (REF-144)
	checker.SetServiceQuotas(servicequotas.NewFromConfig(awsCfg))
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
		spinner.Success("Health validation complete!")
		ui.DisplayHealthResults(result)
	}
	// With --health-only the verdict is the document itself.
	if flags.machine() && !flags.healthOnly && !flags.quiet {
		ui.WriteHealthResults(os.Stderr, result)
	}

	if flags.machineHealthOutput() {
		return &result, true, healthExitError(result)
	}

	done, err = applyHealthDecision(ctx, result, flags)
	return &result, done, err
}

// healthExitError maps a health verdict to the --health-only exit-code
// contract: 0 = pass, 2 = warnings, 3 = blocked. Messages go to stderr via
// urfave/cli, keeping stdout pure data for JSON/YAML output.
func healthExitError(summary health.HealthSummary) error {
	switch summary.Decision {
	case health.DecisionBlock:
		return cli.Exit("pre-flight health checks failed: "+healthProblems(summary), 3)
	case health.DecisionWarn:
		return cli.Exit("health checks completed with warnings: "+healthProblems(summary), 2)
	default:
		return nil
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
		if summary.Decision == health.DecisionBlock && !(failed && r.IsBlocking) {
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
			ui.DisplayHealthCheckComplete(summary.Decision)
		}
		if flags.healthOnly {
			return true, healthExitError(summary)
		}
		return true, fmt.Errorf("pre-flight health checks failed: %s", healthProblems(summary))
	case health.DecisionWarn:
		if flags.healthOnly {
			if human {
				ui.DisplayHealthCheckComplete(summary.Decision)
			}
			return true, healthExitError(summary)
		}
		// --require-healthy turns warnings into a hard stop (the strict-pipeline
		// knob) instead of a prompt.
		if flags.requireHealthy {
			if human {
				ui.DisplayHealthCheckComplete(summary.Decision)
			}
			return true, cli.Exit("health checks reported warnings and --require-healthy is set: "+healthProblems(summary), 2)
		}
		// --yes proceeds past warnings without prompting.
		if flags.yes {
			// Surface the auto-accepted warnings so an operator (e.g. a fleet
			// update that auto-accepts) sees them instead of proceeding
			// silently. With -o json/yaml this goes to stderr.
			if !flags.quiet {
				flags.notice(color.FgYellow, "Health check reported warnings; proceeding: %s", healthProblems(summary))
			}
			return false, nil
		}
		// Without a TTY (CI/cron), or with -o json/yaml, and without --yes,
		// fail fast rather than block on a prompt that can't be answered.
		if !flags.canPrompt() {
			return true, fmt.Errorf("health checks reported warnings (%s); re-run with --yes to proceed or --require-healthy to fail (%s)", healthProblems(summary), flags.noPromptReason())
		}
		if !flags.quiet && !ui.PromptContinueWithWarnings(ctx, summary.Warnings) {
			color.Yellow("Update cancelled by user")
			return true, fmt.Errorf("update cancelled")
		}
	case health.DecisionProceed:
		if human && (flags.healthOnly || !flags.quiet) {
			ui.DisplayHealthCheckComplete(summary.Decision)
		}
		if flags.healthOnly {
			return true, nil
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
// selection interactively when ambiguous. It returns errors without printing
// them: main (or the fleet summary) reports each error once, on stderr.
func selectNodegroupsForUpdate(ctx context.Context, eksClient *eks.Client, clusterName, pattern string, flags updateAMIFlags) ([]string, error) {
	names, err := listNodegroupNames(ctx, eksClient, clusterName)
	if err != nil {
		return nil, err
	}
	matches := awsinternal.MatchingNodegroups(names, pattern)
	// An ambiguous pattern (multiple matches) normally prompts. In unattended
	// mode --yes selects them all; without a TTY or with -o json/yaml, and
	// without --yes, fail fast instead of hanging on a prompt.
	if len(matches) > 1 && pattern != "" {
		if flags.yes {
			return matches, nil
		}
		if !flags.canPrompt() {
			return nil, fmt.Errorf("pattern %q matched %d nodegroups; re-run with --yes to update all, or a more specific name (%s)", pattern, len(matches), flags.noPromptReason())
		}
	}
	return awsinternal.ConfirmNodegroupSelection(ctx, matches, pattern)
}

// updateOutcomes records the per-nodegroup disposition of an update run, used
// for the run summary (-o json/yaml) and the exit-code contract.
type updateOutcomes struct {
	Cluster      string                `json:"cluster" yaml:"cluster"`
	Started      []string              `json:"started" yaml:"started"`
	Skipped      []string              `json:"skipped" yaml:"skipped"`                 // already on latest, or already updating
	Custom       []string              `json:"customUnmanaged" yaml:"customUnmanaged"` // custom-AMI nodegroups (managed via LT)
	Failed       []string              `json:"failed" yaml:"failed"`                   // describe or UpdateNodegroupVersion failed
	Verification *PostRollVerification `json:"verification,omitempty" yaml:"verification,omitempty"`
}

// updateDocument is the -o json/yaml document of a single-cluster update: the
// run summary's fields plus the pre-flight verdict when a check ran.
type updateDocument struct {
	updateOutcomes
	Health *health.HealthSummary `json:"health,omitempty" yaml:"health,omitempty"`
}

// dryRunNodegroup is one nodegroup's previewed action in a -o json/yaml
// dry-run document.
type dryRunNodegroup struct {
	Name string `json:"name" yaml:"name"`
	// Action is update, force-update, skip-updating, or skip-latest.
	Action     string `json:"action" yaml:"action"`
	CurrentAMI string `json:"currentAmi,omitempty" yaml:"currentAmi,omitempty"`
	LatestAMI  string `json:"latestAmi,omitempty" yaml:"latestAmi,omitempty"`
	Reason     string `json:"reason" yaml:"reason"`
}

// dryRunPlan is the -o json/yaml document for `nodegroup update --dry-run`.
type dryRunPlan struct {
	Cluster    string            `json:"cluster" yaml:"cluster"`
	DryRun     bool              `json:"dryRun" yaml:"dryRun"`
	Force      bool              `json:"force" yaml:"force"`
	Nodegroups []dryRunNodegroup `json:"nodegroups" yaml:"nodegroups"`
}

// dryRunDocument previews the selected nodegroups without printing, for
// -o json/yaml.
func dryRunDocument(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, selected []string, force bool) (dryRunPlan, error) {
	updates, err := dryrun.Preview(ctx, awsCfg, eksClient, clusterName, selected, force)
	if err != nil {
		return dryRunPlan{}, err
	}
	plan := dryRunPlan{Cluster: clusterName, DryRun: true, Force: force, Nodegroups: make([]dryRunNodegroup, 0, len(updates))}
	for _, u := range updates {
		plan.Nodegroups = append(plan.Nodegroups, dryRunNodegroup{
			Name:       u.Name,
			Action:     dryrun.ActionName(u.Action),
			CurrentAMI: u.CurrentAMI,
			LatestAMI:  u.LatestAMI,
			Reason:     u.Reason,
		})
	}
	return plan, nil
}

// startNodegroupUpdates starts a version update, through the nodegroup
// service, for each selected nodegroup that isn't already updating or already
// on the latest AMI, returning successful update progress entries.
// Per-nodegroup failures are logged and skipped, matching the original
// best-effort behavior.
//
// The already-on-latest skip mirrors the dry-run preview (ActionSkipLatest) so
// the real run matches what `--dry-run` promised; `--force` bypasses it.
func startNodegroupUpdates(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, nodegroups []string, flags updateAMIFlags) ([]refreshTypes.UpdateProgress, updateOutcomes) {
	skipLatest := newLatestAMISkipChecker(ctx, awsCfg, eksClient, clusterName, flags)
	// Progress lines are human-only. Skip and failure notices always print:
	// to stderr with -o json/yaml (see noticeOut).
	human := !flags.quiet && !flags.machine()

	ngSvc := factory.NewNodegroupService(awsCfg, false, nil)
	outcomes := updateOutcomes{Cluster: clusterName}
	updates := make([]refreshTypes.UpdateProgress, 0, len(nodegroups))
	for _, ng := range nodegroups {
		nodegroup, err := ngSvc.DescribeNodegroup(ctx, clusterName, ng)
		if err != nil {
			flags.notice(color.FgRed, "Failed to describe nodegroup %s: %v", ng, err)
			outcomes.Failed = append(outcomes.Failed, ng)
			continue
		}
		// Custom-AMI nodegroups: EKS doesn't manage the AMI (it lives in the
		// user's launch template), so UpdateNodegroupVersion can't pick a
		// recommended AMI. Skip with clear guidance instead of mis-rolling.
		if nodegroup.AmiType == ekstypes.AMITypesCustom {
			flags.notice(color.FgYellow, "Nodegroup %s uses a custom AMI (AmiType=CUSTOM); refresh can't select a recommended AMI.", ng)
			flags.notice(color.FgYellow, "  Publish a new launch template version with your AMI and roll it (e.g. update the LT, then `nodegroup update --force`).")
			outcomes.Custom = append(outcomes.Custom, ng)
			continue
		}
		if nodegroup.Status == ekstypes.NodegroupStatusUpdating {
			flags.notice(color.FgYellow, "Nodegroup %s is already UPDATING. Skipping update.", ng)
			outcomes.Skipped = append(outcomes.Skipped, ng)
			continue
		}
		if skipLatest(nodegroup) {
			flags.notice(color.FgGreen, "Nodegroup %s is already on the latest AMI. Skipping (use --force to update anyway).", ng)
			outcomes.Skipped = append(outcomes.Skipped, ng)
			continue
		}
		if human {
			color.Cyan("Starting update for nodegroup %s...", ng)
		}

		// The service pins one ClientRequestToken across its retries, so a
		// throttled or dropped request can't start a second roll. A new run or
		// a fleet revisit sends a new request; the UPDATING skip above is what
		// keeps it from rolling the nodegroup again.
		update, err := ngSvc.StartVersionUpdate(ctx, clusterName, ng, nodegroupsvc.VersionUpdateOptions{Force: flags.force})
		if err != nil {
			flags.notice(color.FgRed, "Failed to update nodegroup %s: %v", ng, err)
			outcomes.Failed = append(outcomes.Failed, ng)
			continue
		}
		if update == nil || update.Id == nil {
			flags.notice(color.FgRed, "Update for nodegroup %s returned no update ID", ng)
			outcomes.Failed = append(outcomes.Failed, ng)
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
		outcomes.Started = append(outcomes.Started, ng)
		if human {
			color.Green("Update started for nodegroup %s (ID: %s)", ng, *update.Id)
		}
	}
	return updates, outcomes
}

// newLatestAMISkipChecker returns a predicate reporting whether a nodegroup is
// already on the latest recommended AMI for its type and should be skipped.
// With --force it always returns false. AMI resolution is best-effort: when
// the current or latest AMI can't be determined the nodegroup is NOT skipped
// (same as the dry-run preview's "AMI status unknown, update recommended").
func newLatestAMISkipChecker(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, flags updateAMIFlags) func(*ekstypes.Nodegroup) bool {
	if flags.force {
		return func(*ekstypes.Nodegroup) bool { return false }
	}

	clusterOut, err := eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
	if err != nil || clusterOut.Cluster == nil || clusterOut.Cluster.Version == nil {
		return func(*ekstypes.Nodegroup) bool { return false }
	}
	k8sVersion := *clusterOut.Cluster.Version

	ec2Client := ec2.NewFromConfig(awsCfg)
	asgClient := autoscaling.NewFromConfig(awsCfg)
	return latestAMISkipPredicate(ctx, k8sVersion,
		awsinternal.NewLatestAMIIDCache(ssm.NewFromConfig(awsCfg)),
		func(ctx context.Context, ng *ekstypes.Nodegroup) string {
			return awsinternal.CurrentAmiID(ctx, ng, ec2Client, asgClient)
		})
}

// latestAMISkipPredicate compares each nodegroup's current AMI against the
// latest AMI for the nodegroup's own Kubernetes version (clusterVersion only
// as a fallback). UpdateNodegroupVersion is called without a Version, so it
// stays on the nodegroup's minor; comparing against the cluster's minor would
// never skip a nodegroup that lags the control plane.
func latestAMISkipPredicate(ctx context.Context, clusterVersion string, latestAMI *awsinternal.LatestAMICache, currentAMI func(context.Context, *ekstypes.Nodegroup) string) func(*ekstypes.Nodegroup) bool {
	return func(ng *ekstypes.Nodegroup) bool {
		latest, err := latestAMI.ForNodegroup(ctx, ng, clusterVersion)
		if err != nil || latest == "" {
			return false
		}
		current := currentAMI(ctx, ng)
		return current != "" && current == latest
	}
}

// clusterEnvVar supplies the cluster for `nodegroup update` when the command
// line does not name one. No other command reads it.
const clusterEnvVar = "EKS_CLUSTER_NAME"

// clusterEnvNoteOut receives the note printed when the cluster comes from
// clusterEnvVar. It is a var so tests can capture it.
var clusterEnvNoteOut io.Writer = os.Stderr

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

	_, _ = color.New(color.FgYellow).Fprintf(clusterEnvNoteOut, "Using cluster %s from %s\n", env, clusterEnvVar)
	nodegroupPattern := strings.TrimSpace(cmd.String("nodegroup"))
	if !cmd.IsSet("nodegroup") && len(args) == 1 {
		nodegroupPattern = args[0]
	}
	return env, nodegroupPattern
}
