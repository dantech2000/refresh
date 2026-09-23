package addon

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/ui"
)

func runList(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsStandard); err != nil {
		return err
	}
	// Each --watch iteration performs the full setup+fetch+render cycle, so
	// every iteration reads fresh data.
	return runner.Watch(ctx, cmd, func() error { return listAddonsOnce(ctx, cmd) })
}

func listAddonsOnce(ctx context.Context, cmd *cli.Command) error {
	ctx, cancel, cfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, cfg, cmd)
	if err != nil || listed {
		return err
	}

	addonSvc := factory.NewAddonService(cfg, nil)

	var res addons.ListResult
	start := time.Now()
	if err := runner.WithSpinner("addon", "Add-on information gathered!", func() error {
		var ferr error
		res, ferr = addonSvc.ListDetailed(ctx, clusterName, addons.ListOptions{ShowHealth: cmd.Bool("show-health")})
		return ferr
	}); err != nil {
		return err
	}

	if err := writeAddonList(cmd.String("format"), clusterName, res, time.Since(start)); err != nil {
		return err
	}
	return reportListFailures(ui.Stderr, clusterName, res.Failures)
}

// writeAddonList prints what was gathered in the requested format. When some
// add-ons could not be described, JSON/YAML carry them under "failures" so
// "count" is never mistaken for the full add-on count.
func writeAddonList(format, clusterName string, res addons.ListResult, elapsed time.Duration) error {
	payload := map[string]any{"cluster": clusterName, "addons": res.Summaries, "count": len(res.Summaries)}
	if len(res.Failures) > 0 {
		payload["failures"] = res.Failures
	}
	if handled, err := runner.EncodeStdout(format, payload); handled {
		return err
	}
	return outputAddonsTable(clusterName, res.Summaries, elapsed)
}

// reportListFailures names each add-on that could not be described on w, one
// per line, and returns an error so the command exits non-zero.
func reportListFailures(w io.Writer, clusterName string, failures []string) error {
	if len(failures) == 0 {
		return nil
	}
	yellow := ui.StderrColor(color.FgYellow)
	for _, f := range failures {
		_, _ = yellow.Fprintf(w, "warning: add-on %s\n", f)
	}
	return fmt.Errorf("listing add-ons for cluster %s: %d add-on(s) could not be described; the list is incomplete", clusterName, len(failures))
}

func runDescribe(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsStandard); err != nil {
		return err
	}
	ctx, cancel, cfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, cfg, cmd)
	if err != nil || listed {
		return err
	}

	addonName := runner.PositionalSlot(cmd, "addon", "cluster")
	if addonName == "" {
		return fmt.Errorf("missing add-on name; pass as second argument or --addon <name>")
	}

	addonSvc := factory.NewAddonService(cfg, nil)
	addonName, _, err = resolveAddonName(ctx, addonSvc, clusterName, addonName)
	if err != nil {
		return err
	}

	details, err := addonSvc.Describe(ctx, clusterName, addonName, addons.DescribeOptions{ShowConfiguration: true})
	if err != nil {
		return awsinternal.FormatAWSError(err, "describing add-on")
	}

	if handled, err := runner.EncodeStdout(cmd.String("format"), details); handled {
		return err
	}
	return outputAddonDetailsTable(clusterName, details)
}

// addonNameLister lists a cluster's installed add-on names (the addons
// service), extracted so resolveAddonName is testable.
type addonNameLister interface {
	ListAddonNames(ctx context.Context, clusterName string) ([]string, error)
}

// resolveAddonName matches a user-supplied add-on name against the cluster's
// installed add-ons: an exact match first, then a case-insensitive exact
// match, then a unique case-insensitive substring (partial is then true).
// Several substring matches are an error that lists them; no match is a
// not-found error that lists the installed add-ons.
func resolveAddonName(ctx context.Context, lister addonNameLister, clusterName, addonName string) (name string, partial bool, err error) {
	names, err := lister.ListAddonNames(ctx, clusterName)
	if err != nil {
		return "", false, err
	}
	for _, n := range names {
		if n == addonName {
			return n, false, nil
		}
	}
	for _, n := range names {
		if strings.EqualFold(n, addonName) {
			return n, false, nil
		}
	}
	lower := strings.ToLower(addonName)
	var matches []string
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), lower) {
			matches = append(matches, n)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], true, nil
	case 0:
		return "", false, fmt.Errorf("invalid add-on name '%s'. Available: %s", addonName, strings.Join(names, ", "))
	default:
		return "", false, fmt.Errorf("add-on name '%s' is ambiguous; it matches %s. Pass the full name", addonName, strings.Join(matches, ", "))
	}
}

// stdinIsTerminal and promptLine are vars so tests can simulate a TTY and an
// answer.
var (
	stdinIsTerminal = func() bool {
		return ui.IsTerminal(os.Stdin)
	}
	promptLine = ui.ReadLine
)

// confirmPartialAddon decides whether `addon update` may act on match, a
// substring match for the requested name. --yes accepts it with a note on
// stderr; otherwise a terminal user is asked, and without a terminal the
// command fails and names the candidate.
func confirmPartialAddon(ctx context.Context, requested, match string, yes bool) error {
	yellow := ui.StderrColor(color.FgYellow)
	if yes {
		_, _ = yellow.Fprintf(ui.Stderr, "No add-on named %q; using the partial match %q (--yes)\n", requested, match)
		return nil
	}
	if !stdinIsTerminal() {
		return fmt.Errorf("no add-on named %q (partial match: %s); pass the exact name, or --yes to accept the match (no interactive terminal for confirmation)", requested, match)
	}
	_, _ = yellow.Fprintf(ui.Stderr, "No add-on named %q. Use %q? [y/N]: ", requested, match)
	answer, err := promptLine(ctx)
	if err != nil {
		return ui.PromptError(err)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	default:
		return fmt.Errorf("operation cancelled by user")
	}
}

// rejectAllWithTarget fails `addon update --all` when an add-on name or
// version is also given: --all updates every add-on to its latest version,
// so the named target would be silently ignored.
func rejectAllWithTarget(cmd *cli.Command) error {
	var extra []string
	if cmd.IsSet("addon") {
		extra = append(extra, "--addon "+cmd.String("addon"))
	}
	if cmd.IsSet("version") {
		extra = append(extra, "--version "+cmd.String("version"))
	}
	// The first positional is the cluster unless --cluster names it.
	args := cmd.Args().Slice()
	if !cmd.IsSet("cluster") && len(args) > 0 {
		args = args[1:]
	}
	extra = append(extra, args...)
	if len(extra) == 0 {
		return nil
	}
	return fmt.Errorf("--all updates every add-on to its latest version and cannot be combined with an add-on name or version (got %s); drop --all to update one add-on", strings.Join(extra, " "))
}

// warnAllOnlyFlags warns when flags that only apply to `addon update --all`
// are passed on the single-addon path, where they're silently inert. (REF-55)
func warnAllOnlyFlags(cmd *cli.Command) {
	var set []string
	if cmd.Bool("parallel") {
		set = append(set, "--parallel")
	}
	if len(cmd.StringSlice("skip")) > 0 {
		set = append(set, "--skip")
	}
	if cmd.Bool("dependency-order") {
		set = append(set, "--dependency-order")
	}
	if len(set) > 0 {
		_, _ = fmt.Fprintf(ui.Stderr, "warning: %s only apply with --all and are ignored for a single add-on update\n",
			strings.Join(set, ", "))
	}
}

func runUpdate(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsStandard); err != nil {
		return err
	}
	warnAllOnlyFlags(cmd)
	// With --wait, --timeout covers the API calls and --wait-timeout the wait,
	// so a long --wait-timeout isn't cut short by --timeout.
	setupTimeout := cmd.Duration("timeout")
	if cmd.Bool("wait") && setupTimeout > 0 && cmd.Duration("wait-timeout") > 0 {
		setupTimeout += cmd.Duration("wait-timeout")
	}
	ctx, cancel, cfg, err := runner.SetupAWSWithDeadline(ctx, cmd, setupTimeout)
	if err != nil {
		return err
	}
	defer cancel()

	// Mutating: no cluster list on empty input, and no kubeconfig fallback.
	clusterName, err := runner.ResolveCluster(ctx, cfg, cmd)
	if err != nil {
		return err
	}

	addonName := runner.PositionalSlot(cmd, "addon", "cluster")
	if addonName == "" {
		return fmt.Errorf("missing add-on name; pass as second argument or --addon <name>")
	}

	// Route through the addons service so single-addon updates get the same
	// version resolution, compatibility validation, optional health checks,
	// and optional wait behavior as `update --all`.
	addonSvc := factory.NewAddonService(cfg, nil)
	requested := addonName
	addonName, partial, err := resolveAddonName(ctx, addonSvc, clusterName, addonName)
	if err != nil {
		return err
	}
	if partial {
		if err := confirmPartialAddon(ctx, requested, addonName, cmd.Bool("yes")); err != nil {
			return err
		}
	}

	// version slot is third positional after (cluster, addon). PositionalSlot
	// shifts the expected index down by 1 for each prior flag that was set, so
	// `--addon=foo my-cluster v1.2.3` correctly picks up v1.2.3.
	version := runner.PositionalSlot(cmd, "version", "cluster", "addon")
	if version == "" {
		version = "latest"
	}

	result, updateErr := addonSvc.Update(ctx, clusterName, addonName, addons.UpdateOptions{
		Version:     version,
		DryRun:      cmd.Bool("dry-run"),
		HealthCheck: cmd.Bool("health-check"),
		Wait:        cmd.Bool("wait"),
		WaitTimeout: cmd.Duration("wait-timeout"),
	})
	if result == nil {
		return updateErr
	}
	if result.Warning != "" {
		_, _ = ui.StderrColor(color.FgYellow).Fprintf(ui.Stderr, "warning: %s\n", result.Warning)
	}

	// The result is printed even when the wait failed, so the update ID and
	// the reason reach every output format; updateExitError then sets the
	// exit code. Honor -o json|yaml for the single-addon result (REF-55);
	// -o plain gets the same one-row TSV as `update --all`, and table falls
	// through to the human-readable summary below.
	if handled, encErr := runner.EncodeStdout(cmd.String("format"), result); handled {
		if encErr != nil {
			return encErr
		}
		return updateExitError(result, updateErr)
	}
	if ui.PlainOutput() {
		results := []addons.AddonUpdateResult{*result}
		writeUpdateIssues(ui.Stderr, results)
		addonUpdatePlain(results).Render()
		return updateExitError(result, updateErr)
	}

	switch result.Status {
	case addons.StatusDryRun:
		color.Cyan("DRY RUN: Would update add-on %s from %s to %s on cluster %s",
			addonName, result.PreviousVersion, result.NewVersion, clusterName)
	case addons.StatusUpToDate:
		color.Green("Add-on %s is already at %s; nothing to update", addonName, result.PreviousVersion)
	case addons.StatusInProgress:
		color.Yellow("Add-on %s is already being updated to %s; no new update was submitted. Use --wait to wait for it.", addonName, result.NewVersion)
	case addons.StatusCompleted:
		color.Green("Add-on %s updated to %s (was %s)", addonName, result.NewVersion, result.PreviousVersion)
	case addons.StatusCompletedWithIssues:
		color.Yellow("Add-on %s updated to %s, but the post-update health check found issues: %s",
			addonName, result.NewVersion, result.HealthIssues)
	case addons.StatusWaitFailed:
		color.Red("Update %s for add-on %s did not complete", result.UpdateID, addonName)
	default:
		color.Green("Update started for add-on %s (ID: %s)", addonName, result.UpdateID)
		color.White("Use AWS Console or 'refresh addon describe %s --addon %s' to check status.", clusterName, addonName)
	}
	return updateExitError(result, updateErr)
}

// exitNeedsAttention is the exit code for an update that landed but whose
// post-update health check found issues (COMPLETED_WITH_ISSUES).
const exitNeedsAttention = 2

// updateExitError maps a single add-on update to the command's error: the
// update's own error (exit 1), exit 2 for COMPLETED_WITH_ISSUES, or nil.
func updateExitError(result *addons.AddonUpdateResult, err error) error {
	if err != nil {
		return err
	}
	if result.Status == addons.StatusCompletedWithIssues {
		return cli.Exit(fmt.Sprintf("add-on %s was updated, but the post-update health check found issues", result.AddonName), exitNeedsAttention)
	}
	return nil
}

func runUpdateAll(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsStandard); err != nil {
		return err
	}
	if err := rejectAllWithTarget(cmd); err != nil {
		return err
	}
	// The overall deadline depends on how many add-ons get updated, which is
	// only known after listing them. With --wait, the setup context gets no
	// deadline here; the service applies --timeout plus --wait-timeout per
	// add-on (addons.UpdateAllOptions.Timeout).
	timeout := cmd.Duration("timeout")
	setupTimeout := timeout
	if cmd.Bool("wait") {
		setupTimeout = 0
	}
	ctx, cancel, cfg, err := runner.SetupAWSWithDeadline(ctx, cmd, setupTimeout)
	if err != nil {
		return err
	}
	defer cancel()

	resolveCtx, cancelResolve := ctx, context.CancelFunc(func() {})
	if timeout > 0 {
		resolveCtx, cancelResolve = context.WithTimeout(ctx, timeout)
	}
	clusterName, err := runner.ResolveCluster(resolveCtx, cfg, cmd)
	cancelResolve()
	if err != nil {
		return err
	}

	if cmd.Bool("parallel") && cmd.Bool("dependency-order") {
		return fmt.Errorf("--parallel and --dependency-order cannot be used together: parallel execution defeats dependency ordering")
	}

	addonSvc := factory.NewAddonService(cfg, nil)

	options := addons.UpdateAllOptions{
		DryRun:          cmd.Bool("dry-run"),
		Parallel:        cmd.Bool("parallel"),
		Wait:            cmd.Bool("wait"),
		WaitTimeout:     cmd.Duration("wait-timeout"),
		SkipAddons:      cmd.StringSlice("skip"),
		DependencyOrder: cmd.Bool("dependency-order"),
		HealthCheck:     cmd.Bool("health-check"),
		Timeout:         timeout,
	}

	var results []addons.AddonUpdateResult
	if err := runner.WithSpinner("addon", "Addon updates processed!", func() error {
		var rerr error
		results, rerr = addonSvc.UpdateAll(ctx, clusterName, options)
		return rerr
	}); err != nil {
		return err
	}

	payload := map[string]any{
		"cluster": clusterName,
		"dryRun":  options.DryRun,
		"results": results,
	}
	if handled, err := runner.EncodeStdout(cmd.String("format"), payload); handled {
		if err != nil {
			return err
		}
		return updateAllFailureError(results)
	}
	if err := outputUpdateAllResults(clusterName, results, options.DryRun); err != nil {
		return err
	}
	return updateAllFailureError(results)
}

// updateAllFailureError returns a non-nil error when any addon update failed,
// so `addon update --all` exits non-zero and scripts can detect failure: exit
// 1 when an update failed, or exit 2 when every update landed but at least
// one post-update health check found issues.
func updateAllFailureError(results []addons.AddonUpdateResult) error {
	failed, issues := 0, 0
	for _, r := range results {
		switch {
		case strings.HasPrefix(r.Status, "FAILED"), r.Status == addons.StatusWaitFailed:
			failed++
		case r.Status == addons.StatusCompletedWithIssues:
			issues++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d addon update(s) failed", failed, len(results))
	}
	if issues > 0 {
		return cli.Exit(fmt.Sprintf("%d of %d add-on(s) were updated, but their post-update health check found issues", issues, len(results)), exitNeedsAttention)
	}
	return nil
}

// healthBadge converts the addons service's plain health vocabulary
// (PASS/FAIL/IN_PROGRESS/UNKNOWN) into the shared colored badges.
func healthBadge(health string) string {
	switch health {
	case "":
		return ""
	case "PASS":
		return ui.BadgePass()
	case "FAIL":
		return ui.BadgeFail()
	case "IN_PROGRESS":
		return ui.BadgeInProgress()
	default:
		return ui.BadgeUnknown()
	}
}
