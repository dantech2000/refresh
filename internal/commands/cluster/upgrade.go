package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/flagcanon"
	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/rollview"
	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/ui"
)

// upgradeDefaultTimeout bounds a full orchestrated upgrade. Control-plane
// hops run ~10m each and nodegroup rolls ~10-20m per group, so multi-hop
// upgrades legitimately run for hours.
const upgradeDefaultTimeout = 4 * time.Hour

func upgradeCommand() *cli.Command {
	return &cli.Command{
		Name:      "upgrade",
		Usage:     "Orchestrate a cluster upgrade: control plane → addons → nodegroups, with gates",
		ArgsUsage: "[cluster]",
		Description: `Plan and execute a full EKS cluster upgrade to a target Kubernetes version.

EKS upgrades one minor version at a time, so a multi-minor upgrade expands
into sequential hops. Each hop runs: readiness (cluster insights + kubelet
version skew) → control plane → addons (dependency order, versions compatible
with the hop target) → nodegroup rolls, with a health gate after every phase.

Before each control-plane step, refresh asks EKS to re-evaluate Cluster
Insights (up to 5m) and blocks on ERROR or UNKNOWN insights, or when EKS has
not evaluated the hop version yet. EKS itself no longer enforces insights on a
version update, so this is the only deprecated-API check; --skip-insights-check
turns it off. --dry-run starts no refresh: it reads existing insights, and
missing ones are a notice instead of a blocker.

Before each nodegroup roll, pre-flight health checks run, including
PodDisruptionBudgets that would block the drain (they need Kubernetes access
via kubeconfig, or --kubeconfig/--kube-context; without it the PDB check is
skipped with a warning). A drain
blocker stops the roll unless --force; health warnings need --yes or a
confirmation. --skip-health-check turns these checks off.

The plan is re-derived from live cluster state on every run, so rerunning the
same command after a failure (or Ctrl+C) resumes where it left off, and
rerunning after success is a no-op.

Examples:
   # Print the plan only (exits 3 if anything blocks the upgrade)
   refresh cluster upgrade -c prod-east --to 1.33 --dry-run

   # Execute, confirming each mutating phase
   refresh cluster upgrade -c prod-east --to 1.33

   # Non-interactive (CI) run
   refresh cluster upgrade -c prod-east --to 1.33 --yes

   # Machine-readable run: one JSON document {plan, report, failures} on
   # stdout, progress on stderr (-o json/yaml never prompts, so it needs --yes)
   refresh cluster upgrade -c prod-east --to 1.33 --yes -o json`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.StringFlag{Name: "to", Usage: "Target Kubernetes version (e.g. 1.33)", Required: true},
			runner.DryRunFlag("Print the full ordered plan without mutating anything"),
			runner.YesFlag("Skip per-phase confirmation prompts (required with -o json/yaml or without a terminal)"),
			&cli.BoolFlag{Name: "force", Usage: "Force nodegroup rolls when pods can't be drained due to PDBs"},
			&cli.BoolFlag{Name: "skip-insights-check", Usage: "Upgrade without the EKS Cluster Insights readiness check (deprecated APIs, kubelet skew of nodes outside managed nodegroups). Risky: EKS does not block the upgrade itself"},
			&cli.BoolFlag{Name: "skip-health-check", Usage: "Roll nodegroups without the pre-flight PDB drain-blocker and health checks (not recommended)"},
			runner.KubeconfigFlag("the PDB drain-blocker checks and the live roll panel"),
			runner.KubeContextFlag(),
			&cli.StringSliceFlag{Name: "skip", Usage: "Addon name to skip, exact and case-insensitive (repeatable; for addons managed via Helm/GitOps)"},
			&cli.StringSliceFlag{Name: "skip-nodegroup", Usage: "Nodegroup name pattern to skip (repeatable)"},
			&cli.BoolFlag{Name: "quiet", Aliases: []string{"q"}, Usage: "Suppress progress output"},
			runner.WaitTimeoutFlag("How long to wait for the whole upgrade to finish (0 = no limit; not read from REFRESH_TIMEOUT, which only sets API timeouts)", upgradeDefaultTimeout),
			// Deprecated in 0.11.0: the local --timeout/-t meant the wait
			// timeout and clashed with the global API --timeout. Kept hidden
			// for one release; the global --timeout before the subcommand
			// still sets the API timeout.
			flagcanon.DeprecatedDuration("timeout", "wait-timeout", "t"),
			&cli.DurationFlag{Name: "poll-interval", Usage: "How often to poll in-flight updates", Value: appconfig.DefaultPollInterval},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain). json/yaml print one document: the plan with --dry-run or when blocked, else {plan, report, failures} after the run (requires --yes)", Value: "table"},
		},
		Action: runUpgrade,
	}
}

// upgradeResult is the -o json/yaml document of an executed upgrade: the plan
// the run started from, the engine's report of what it did, and every
// failure of the run: the plan's, and the one that stopped it.
type upgradeResult struct {
	Plan     *upgrade.Plan   `json:"plan" yaml:"plan"`
	Report   *upgrade.Report `json:"report" yaml:"report"`
	Failures diag.List       `json:"failures" yaml:"failures"`
}

// DocumentKind is UpgradeRun.
func (upgradeResult) DocumentKind() apidoc.Kind { return apidoc.KindUpgradeRun }

// runFailures is the plan's failures plus the one that stopped the run,
// sorted.
func runFailures(plan *upgrade.Plan, report *upgrade.Report) diag.List {
	fs := append(diag.List(nil), plan.Failures...)
	if report != nil && report.Failure != nil && !slices.Contains(fs, *report.Failure) {
		fs = append(fs, *report.Failure)
	}
	diag.Sort(fs)
	return fs
}

// setRegion sets region on each failure that has none: the upgrade service
// does not know the region.
func setRegion(fs []diag.Failure, region string) {
	for i := range fs {
		if fs[i].Region == "" {
			fs[i].Region = region
		}
	}
}

// planExit is the exit code of a plan that is printed and not run: 3 when it
// has a blocker (nothing changed), else 4 when the planner could not read
// something, else 0.
func planExit(plan *upgrade.Plan, blocked error) error {
	if plan.Blocked() {
		return blocked
	}
	return runner.IncompleteExit(plan.Failures)
}

func runUpgrade(ctx context.Context, cmd *cli.Command) (err error) {
	format := cmd.String("format")
	if err := runner.ValidateFormat(format, runner.FormatsStandard); err != nil {
		return err
	}
	// -o json/yaml keeps stdout to one document, so a run can't stop to ask
	// before each phase: executing needs --yes. Checked before any AWS call.
	if runner.IsMachineFormat(format) && !cmd.Bool("dry-run") && !cmd.Bool("yes") {
		return fmt.Errorf("cluster upgrade -o %s does not prompt before each phase; add --yes to execute, or --dry-run to print the plan only", strings.ToLower(format))
	}
	// Without a terminal, the per-phase prompts can't be answered: fail now,
	// before any AWS call, instead of declining the first phase.
	if err := runner.RequireYesUnattended(cmd); err != nil {
		return err
	}
	pollInterval := cmd.Duration("poll-interval")
	if pollInterval <= 0 {
		return fmt.Errorf("--poll-interval must be greater than 0 (got %s)", pollInterval)
	}
	// The run's deadline is --wait-timeout (the whole upgrade), not the
	// global API --timeout.
	waitTimeout := runner.WaitTimeout(cmd, "timeout")
	ctx, cancel, awsCfg, err := runner.SetupAWSWithDeadline(ctx, cmd, waitTimeout)
	if err != nil {
		return err
	}
	defer cancel()
	defer runner.WaitDeadlineHint(&err)

	// Mutating: no cluster list on empty input, and no kubeconfig fallback.
	// -o json/yaml runs are unattended, so a partial name fails with the
	// candidate instead of prompting, even on a TTY.
	clusterName, err := runner.ResolveCluster(ctx, awsCfg, cmd)
	if err != nil {
		return err
	}

	svc := upgrade.NewService(factory.NewEKSClient(awsCfg), factory.NewDefaultLogger(nil))
	svc.PollInterval = pollInterval

	planOpts := upgrade.PlanOptions{
		SkipAddons:        cmd.StringSlice("skip"),
		SkipNodegroups:    cmd.StringSlice("skip-nodegroup"),
		SkipInsightsCheck: cmd.Bool("skip-insights-check"),
		// A dry run starts no insights refresh (a write API).
		Preview: cmd.Bool("dry-run"),
	}
	healthGate := newNodegroupHealthGate(cmd, awsCfg, clusterName)

	plan, err := buildUpgradePlan(ctx, cmd, svc, clusterName, planOpts)
	if err != nil {
		return err
	}
	setRegion(plan.Failures, awsCfg.Region)

	if runner.IsMachineFormat(format) {
		return runUpgradeMachine(ctx, cmd, svc, plan, clusterName, awsCfg.Region, format, healthGate)
	}
	// Switches the UI into plain mode for -o plain.
	if _, eerr := runner.EncodeStdout(format, plan); eerr != nil {
		return eerr
	}
	// out receives everything after the plan. With -o plain, stdout is only
	// the plan's TSV rows, so the rest (progress, prompts, the report) goes to
	// stderr.
	out := io.Writer(os.Stdout)
	if ui.PlainOutput() {
		writeUpgradePlanPlain(os.Stdout, ui.Stderr, plan)
		out = ui.Stderr
	} else {
		renderPlan(plan)
	}
	// The reads the planner could not make, named once, before any prompt.
	runner.WriteFailures(format, os.Stdout, ui.Stderr, plan.Failures)

	// A plan with blockers prints and exits 3 (blocked) without mutating.
	// A plan that is only printed exits 4 when the planner could not read
	// something.
	if plan.Blocked() || cmd.Bool("dry-run") {
		return planExit(plan, cli.Exit("Upgrade blocked — resolve the blockers above and re-run.", runner.ExitBlocked))
	}
	if plan.PendingSteps() == 0 {
		writeUpgradeOutcome(out, clusterName, plan, false)
		return runner.IncompleteExit(plan.Failures)
	}

	progress := func(format string, args ...any) {
		if !cmd.Bool("quiet") {
			_, _ = fmt.Fprintf(out, "  "+format+"\n", args...)
		}
	}

	// Live per-node roll panel during each nodegroup phase, when interactive and
	// the cluster API is reachable (resolved quietly — best-effort). Falls back to
	// text progress otherwise. Rendering stays in this view layer; the
	// orchestrator only invokes the injected observer. (REF-126)
	// The panel draws on stdout, so -o plain skips it, and so do piped and
	// NO_COLOR runs: there it would append a frame per tick and hold back
	// the progress lines.
	var ngObserver upgrade.RollObserver
	if !cmd.Bool("quiet") && !ui.PlainOutput() && rollview.Interactive(os.Stdout) {
		if kube, _ := resolveReadinessKubeClient(ctx, factory.NewEKSClient(awsCfg), awsCfg.Region, clusterName, cmd.String("kubeconfig"), cmd.String("kube-context"), false); kube != nil {
			poll := cmd.Duration("poll-interval")
			ngObserver = func(octx context.Context, ng string) {
				rollview.LiveRollForUpdate(octx, kube, ng, waitTimeout, poll)
			}
		}
	}

	opts := executeOptions(cmd, healthGate)
	opts.Confirm = func(label string) bool { return promptPhase(ctx, out, label) }
	opts.Progress = progress
	opts.PhaseStart = phaseStart(out, cmd.Bool("quiet"))
	healthGate.confirm, healthGate.progress = opts.Confirm, progress
	opts.NodegroupObserver = ngObserver
	report, err := svc.Execute(ctx, plan, opts)

	renderReport(out, report)
	if report != nil && report.Failure != nil {
		// The plan's failures are on stderr already; add the one that
		// stopped the run.
		stop := []diag.Failure{*report.Failure}
		setRegion(stop, awsCfg.Region)
		runner.WriteFailures(format, os.Stdout, ui.Stderr, stop)
	}
	if err != nil {
		th := render.Default(out)
		_, _ = fmt.Fprintf(out, "\nResume with: %s\n", th.Paint(th.Pal.Sky, resumeCommand(cmd, clusterName, plan)))
		return err
	}

	writeUpgradeOutcome(out, clusterName, plan, true)
	return runner.IncompleteExit(plan.Failures)
}

// buildUpgradePlan builds the plan behind a spinner. The insights refresh can
// take minutes, so its progress lines stop the spinner and go to stderr
// (unless --quiet). Ctrl+C or --wait-timeout while planning is reported as an
// interrupt, not as a blocked plan.
func buildUpgradePlan(ctx context.Context, cmd *cli.Command, svc *upgrade.Service, clusterName string, opts upgrade.PlanOptions) (*upgrade.Plan, error) {
	spinner := ui.NewFunSpinnerForCategory("cluster")
	if err := spinner.Start(); err != nil {
		return nil, fmt.Errorf("failed to start spinner: %w", err)
	}
	defer spinner.Stop()
	if !cmd.Bool("quiet") {
		opts.Progress = func(format string, args ...any) {
			spinner.Stop()
			_, _ = fmt.Fprintf(ui.Stderr, "  "+format+"\n", args...)
		}
	}
	plan, err := svc.BuildPlan(ctx, clusterName, cmd.String("to"), opts)
	if err != nil {
		if ctx.Err() != nil {
			how := "interrupted"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				how = "timed out"
			}
			return nil, cli.Exit(fmt.Sprintf("upgrade %s before it started; nothing was changed: %v", how, err), runner.ExitError)
		}
		return nil, err
	}
	render.SpinnerDone(spinner, "Upgrade plan computed")
	return plan, nil
}

// executeOptions maps the command's flags to the engine options, with gate
// as the pre-roll health gate. The caller adds the confirm, progress, and
// observer hooks.
func executeOptions(cmd *cli.Command, gate *nodegroupHealthGate) upgrade.ExecuteOptions {
	return upgrade.ExecuteOptions{
		Yes:               cmd.Bool("yes"),
		SkipAddons:        cmd.StringSlice("skip"),
		SkipNodegroups:    cmd.StringSlice("skip-nodegroup"),
		Force:             cmd.Bool("force"),
		SkipInsightsCheck: cmd.Bool("skip-insights-check"),
		NodegroupGate:     gate.check,
	}
}

// resumeCommand is the command that resumes an interrupted or failed run. It
// repeats every flag that decides what is mutated and where: the root
// --profile/--region (placed before the subcommand), the resolved cluster
// name, the target, --skip, --skip-nodegroup, --kubeconfig, --kube-context,
// --wait-timeout, --force, --skip-insights-check, --skip-health-check, and
// --yes. Only flags the user set are included, so
// an unattended run's command stays unattended and an attended one still
// confirms each phase. Values are shell-quoted.
func resumeCommand(cmd *cli.Command, clusterName string, plan *upgrade.Plan) string {
	parts := []string{"refresh"}
	for _, name := range []string{"profile", "region"} {
		if cmd.IsSet(name) {
			if v := strings.TrimSpace(cmd.String(name)); v != "" {
				parts = append(parts, "--"+name, shellQuote(v))
			}
		}
	}
	parts = append(parts, "cluster", "upgrade", "-c", shellQuote(clusterName), "--to", shellQuote(plan.TargetVersion))
	for _, name := range []string{"skip", "skip-nodegroup"} {
		for _, v := range cmd.StringSlice(name) {
			parts = append(parts, "--"+name, shellQuote(v))
		}
	}
	for _, name := range []string{"kubeconfig", "kube-context"} {
		if v := strings.TrimSpace(cmd.String(name)); v != "" {
			parts = append(parts, "--"+name, shellQuote(v))
		}
	}
	// A wait timeout the user chose (also through the deprecated local
	// --timeout) carries over as --wait-timeout.
	if cmd.IsSet("wait-timeout") || flagcanon.LocalIsSet(cmd, "timeout") {
		parts = append(parts, "--wait-timeout", shortDuration(runner.WaitTimeout(cmd, "timeout")))
	}
	for _, name := range []string{"force", "skip-insights-check", "skip-health-check", "yes"} {
		if cmd.Bool(name) {
			parts = append(parts, "--"+name)
		}
	}
	return strings.Join(parts, " ")
}

// shortDuration formats d without trailing zero units: 1h, 1h30m, 45m, 90s
// stays 1m30s. The result is a valid Go duration and needs no shell quoting.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// shellQuote returns s as one POSIX shell word: unchanged when it holds only
// safe characters, else single-quoted.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("-_./:=,@%+", r) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// runUpgradeMachine is the -o json/yaml path. Stdout gets exactly one
// document: the bare plan for --dry-run or a blocked plan, else
// {plan, report, failures} once execution ends, successful or not. Progress
// goes to stderr (or nowhere with --quiet); nothing prompts (runUpgrade
// requires --yes) and there is no live roll panel. Exit codes match the
// human path.
func runUpgradeMachine(ctx context.Context, cmd *cli.Command, svc *upgrade.Service, plan *upgrade.Plan, clusterName, region, format string, healthGate *nodegroupHealthGate) error {
	if plan.Blocked() || cmd.Bool("dry-run") {
		if _, err := runner.EncodeStdout(format, plan); err != nil {
			return err
		}
		runner.ReportFailures(ui.Stderr, plan.Failures)
		return planExit(plan, cli.Exit("upgrade blocked: resolve the blockers in the plan and re-run", runner.ExitBlocked))
	}

	report := upgrade.NewReport()
	var err error
	if plan.PendingSteps() > 0 {
		opts := executeOptions(cmd, healthGate)
		opts.Progress = func(format string, args ...any) {
			if !cmd.Bool("quiet") {
				_, _ = fmt.Fprintf(ui.Stderr, "  "+format+"\n", args...)
			}
		}
		opts.PhaseStart = phaseStart(ui.Stderr, cmd.Bool("quiet"))
		healthGate.progress = opts.Progress
		var r *upgrade.Report
		r, err = svc.Execute(ctx, plan, opts)
		if r != nil {
			report = r
		}
		if report.Failure != nil && report.Failure.Region == "" {
			report.Failure.Region = region
		}
	}

	fs := runFailures(plan, report)
	if _, eerr := runner.EncodeStdout(format, upgradeResult{Plan: plan, Report: report, Failures: fs}); eerr != nil {
		return eerr
	}
	runner.ReportFailures(ui.Stderr, fs)
	if err != nil {
		return fmt.Errorf("%w (resume with: %s)", err, resumeCommand(cmd, clusterName, plan))
	}
	return runner.IncompleteExit(plan.Failures)
}

// promptPhase asks for confirmation before a mutating phase. Bare Enter, a
// read error, or Ctrl+C declines (safe default). Answers come from the shared
// stdin reader, so piped input for several phases is not lost between prompts.
func promptPhase(ctx context.Context, w io.Writer, label string) bool {
	_, _ = fmt.Fprintf(w, "\nProceed with %s? (y/N): ", label)
	return ui.Confirm(ctx)
}
