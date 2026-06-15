package nodegroup

import (
	"context"
	"fmt"
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
	"github.com/dantech2000/refresh/internal/services/common"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
)

// updateAMIFlags collects the flags that govern runUpdateAMI's behavior.
type updateAMIFlags struct {
	force, dryRun, noWait, quiet, skipHealthCheck, healthOnly bool
	yes, requireHealthy, skipVerify, changelog, live          bool
	timeout, pollInterval                                     time.Duration
	format                                                    string
	kubeconfig                                                string
}

func readUpdateAMIFlags(cmd *cli.Command) updateAMIFlags {
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
	}
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

// machineHealthOutput reports whether the health verdict should be emitted as
// JSON/YAML instead of the human table (only meaningful with --health-only).
func (f updateAMIFlags) machineHealthOutput() bool {
	return f.healthOnly && (f.format == "json" || f.format == "yaml")
}

func runUpdateAMI(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsTableJSON); err != nil {
		return err
	}
	if cmd.Bool("simulate") {
		return runSimulatedRoll(ctx, cmd.String("nodegroup"))
	}
	if cmd.Bool("all-clusters") {
		return runFleetUpdate(ctx, cmd)
	}

	ctx, cancel, awsCfg, err := runner.SetupAWSWithTimeout(ctx, cmd, 60*time.Second)
	if err != nil {
		return err
	}
	defer cancel()

	requestedCluster, nodegroupPattern := updateClusterAndNodegroupPatterns(cmd)
	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, requestedCluster)
	if err != nil {
		color.Red("%v", err)
		return err
	}
	eksClient := eks.NewFromConfig(awsCfg)
	flags := readUpdateAMIFlags(cmd)

	done, err := preflightHealthCheck(ctx, awsCfg, eksClient, clusterName, flags)
	if err != nil || done {
		return err
	}

	selectedNodegroups, err := selectNodegroupsForUpdate(ctx, eksClient, clusterName, nodegroupPattern, flags.yes)
	if err != nil {
		return err
	}

	// Pre-flight: a roll launches replacement nodes, so warn if an instance type
	// isn't offered in one of a nodegroup's AZs. Best-effort, non-blocking. (REF-143)
	if !flags.quiet {
		ngSvc := factory.NewNodegroupService(awsCfg, false, nil)
		for _, ng := range selectedNodegroups {
			warnInstanceTypeAvailability(ctx, ngSvc, clusterName, ng)
		}
	}

	if flags.dryRun {
		if derr := dryrun.PerformDryRun(ctx, awsCfg, eksClient, clusterName, selectedNodegroups, flags.force, flags.quiet); derr != nil {
			return derr
		}
		if !flags.quiet {
			printChangelogsForNodegroups(ctx, awsCfg, eksClient, clusterName, selectedNodegroups, flags.changelog)
		}
		return nil
	}

	jsonOut := flags.format == "json" && !flags.healthOnly
	quiet := flags.quiet || jsonOut

	outcomes, verifyFailed, monErr := executeUpdates(ctx, awsCfg, eksClient, clusterName, selectedNodegroups, flags)

	if jsonOut {
		if _, err := runner.EncodeStdout("json", outcomes); err != nil {
			return err
		}
		return updateExit(outcomes, monErr, verifyFailed)
	}
	switch {
	case len(outcomes.Started) == 0:
		if !quiet {
			color.Yellow("No nodegroup updates were started")
		}
	case flags.noWait:
		if !quiet {
			fmt.Printf("Started %d nodegroup update(s). Use 'refresh list --cluster %s' to check status.\n",
				len(outcomes.Started), clusterName)
		}
	default:
		if !quiet && outcomes.Verification != nil {
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
	if verify {
		verifyClient, _ = health.GetKubernetesClient()
		preroll = snapshotPendingPods(ctx, verifyClient)
	}

	updates, outcomes := startNodegroupUpdates(ctx, awsCfg, eksClient, clusterName, selected, flags)
	if len(updates) == 0 || flags.noWait {
		return outcomes, false, nil
	}

	quiet := flags.quiet || (flags.format == "json" && !flags.healthOnly)
	monitor := &refreshTypes.ProgressMonitor{
		Updates:   updates,
		StartTime: time.Now(),
		Quiet:     quiet,
		NoWait:    flags.noWait,
		Timeout:   flags.timeout,
	}
	config := refreshTypes.MonitorConfig{
		PollInterval:    flags.pollInterval,
		MaxRetries:      3,
		BackoffMultiple: 2.0,
		Quiet:           quiet,
		NoWait:          flags.noWait,
		Timeout:         flags.timeout,
	}
	// Live per-node roll view (opt-in, single nodegroup, interactive). Purely
	// visual: it shows nodes draining/joining/terminating during the wait, then
	// EKS DescribeUpdate (below) stays authoritative for the result. Any failure
	// to observe degrades silently to the standard monitor output.
	if flags.live && len(updates) == 1 && !quiet {
		if kube := resolveHealthKubeClient(ctx, flags.kubeconfig, true); kube != nil {
			runLiveRollForUpdate(ctx, kube, updates[0].NodegroupName, flags.timeout, flags.pollInterval)
			monitor.Quiet, config.Quiet = true, true
		}
	}

	monErr := monitoring.MonitorUpdates(ctx, eksClient, monitor, config)

	verifyFailed := false
	if verify && monErr == nil && len(outcomes.Started) > 0 {
		result := verifyPostRoll(ctx, eksClient, verifyClient, clusterName, outcomes.Started, preroll)
		outcomes.Verification = &result
		verifyFailed = !result.OK()
	}
	return outcomes, verifyFailed, monErr
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
// post-roll verification found issues yields exit 5, otherwise success.
func updateExit(o updateOutcomes, monErr error, verifyFailed bool) error {
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
func preflightHealthCheck(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, flags updateAMIFlags) (done bool, err error) {
	if flags.skipHealthCheck || flags.dryRun || flags.force {
		if flags.healthOnly {
			color.Yellow("Health check skipped due to --skip-health-check, --dry-run, or --force flags")
			return true, nil
		}
		return false, nil
	}

	// Machine-readable verdicts suppress all human chrome so stdout is pure
	// data; the exit code still encodes the decision (0/2/3).
	humanOutput := !flags.quiet && !flags.machineHealthOutput()

	if humanOutput {
		ui.DisplayHealthCheckStart(clusterName)
	}
	cwClient := cloudwatch.NewFromConfig(awsCfg)
	asgClient := autoscaling.NewFromConfig(awsCfg)
	k8sClient := resolveHealthKubeClient(ctx, flags.kubeconfig, humanOutput)
	checker := health.NewChecker(eksClient, k8sClient, cwClient, asgClient)
	// Attach metrics-server (best-effort) for live CPU+memory drain headroom; the
	// utilization check skips cleanly if it isn't installed. (REF-142)
	if k8sClient != nil {
		if m, mErr := health.BuildMetricsClient(flags.kubeconfig); mErr == nil {
			checker.SetNodeMetrics(m)
		}
	}
	// EC2 vCPU quota headroom — a roll surges new nodes against the account
	// quota; the check skips cleanly if it can't read the limit/usage. (REF-144)
	checker.SetServiceQuotas(servicequotas.NewFromConfig(awsCfg))

	spinner := ui.NewFunSpinnerForCategory("health")
	if humanOutput {
		if err := spinner.Start(); err != nil {
			return false, err
		}
		defer spinner.Stop()
	}
	summary := checker.RunAllChecks(ctx, clusterName)
	if humanOutput {
		spinner.Success("Health validation complete!")
		ui.DisplayHealthResults(summary)
	}

	if flags.machineHealthOutput() {
		if _, err := runner.EncodeStdout(flags.format, summary); err != nil {
			return true, err
		}
		return true, healthExitError(summary.Decision)
	}

	return applyHealthDecision(summary, flags)
}

// healthExitError maps a health decision to the --health-only exit-code
// contract: 0 = pass, 2 = warnings, 3 = blocked. Messages go to stderr via
// urfave/cli, keeping stdout pure data for JSON/YAML output.
func healthExitError(decision health.Decision) error {
	switch decision {
	case health.DecisionBlock:
		return cli.Exit("pre-flight health checks failed", 3)
	case health.DecisionWarn:
		return cli.Exit("health checks completed with warnings", 2)
	default:
		return nil
	}
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
func applyHealthDecision(summary health.HealthSummary, flags updateAMIFlags) (done bool, err error) {
	switch summary.Decision {
	case health.DecisionBlock:
		ui.DisplayHealthCheckComplete(summary.Decision)
		if flags.healthOnly {
			return true, cli.Exit("pre-flight health checks failed", 3)
		}
		return true, fmt.Errorf("pre-flight health checks failed")
	case health.DecisionWarn:
		if flags.healthOnly {
			ui.DisplayHealthCheckComplete(summary.Decision)
			return true, cli.Exit("health checks completed with warnings", 2)
		}
		// --require-healthy turns warnings into a hard stop (the strict-pipeline
		// knob) instead of a prompt.
		if flags.requireHealthy {
			ui.DisplayHealthCheckComplete(summary.Decision)
			return true, cli.Exit("health checks reported warnings and --require-healthy is set", 2)
		}
		// --yes proceeds past warnings without prompting.
		if flags.yes {
			return false, nil
		}
		// Without a TTY (CI/cron) and without --yes, fail fast rather than block
		// on a prompt that can never be answered.
		if !isInteractive() {
			return true, fmt.Errorf("health checks reported warnings; re-run with --yes to proceed or --require-healthy to fail (no interactive terminal for confirmation)")
		}
		if !flags.quiet && !ui.PromptContinueWithWarnings(summary.Warnings) {
			color.Yellow("Update cancelled by user")
			return true, fmt.Errorf("update cancelled")
		}
	case health.DecisionProceed:
		if flags.healthOnly || !flags.quiet {
			ui.DisplayHealthCheckComplete(summary.Decision)
		}
		if flags.healthOnly {
			return true, nil
		}
	}
	return false, nil
}

// selectNodegroupsForUpdate lists nodegroups matching pattern and confirms the
// selection interactively when ambiguous.
func selectNodegroupsForUpdate(ctx context.Context, eksClient *eks.Client, clusterName, pattern string, yes bool) ([]string, error) {
	names, err := awsinternal.ListAllPages(ctx, "listing nodegroups",
		func(rc context.Context, token *string) (*eks.ListNodegroupsOutput, error) {
			return eksClient.ListNodegroups(rc, &eks.ListNodegroupsInput{ClusterName: aws.String(clusterName), NextToken: token})
		},
		func(out *eks.ListNodegroupsOutput) ([]string, *string) { return out.Nodegroups, out.NextToken },
	)
	if err != nil {
		color.Red("Failed to list nodegroups: %v", err)
		return nil, err
	}
	matches := awsinternal.MatchingNodegroups(names, pattern)
	// An ambiguous pattern (multiple matches) normally prompts. In unattended
	// mode --yes selects them all; without a TTY and without --yes, fail fast
	// instead of hanging on a prompt.
	if len(matches) > 1 && pattern != "" {
		if yes {
			return matches, nil
		}
		if !isInteractive() {
			return nil, fmt.Errorf("pattern %q matched %d nodegroups; re-run with --yes to update all, or a more specific name (no interactive terminal for selection)", pattern, len(matches))
		}
	}
	selected, err := awsinternal.ConfirmNodegroupSelection(matches, pattern)
	if err != nil {
		color.Red("%v", err)
		return nil, err
	}
	return selected, nil
}

// startNodegroupUpdates issues UpdateNodegroupVersion for each selected
// nodegroup that isn't already updating or already on the latest AMI,
// returning successful update progress entries. Per-nodegroup failures are
// logged and skipped, matching the original best-effort behavior.
//
// The already-on-latest skip mirrors the dry-run preview (ActionSkipLatest) so
// the real run matches what `--dry-run` promised; `--force` bypasses it.
// updateOutcomes records the per-nodegroup disposition of an update run, used
// for the JSON summary (-o json) and the exit-code contract.
type updateOutcomes struct {
	Cluster      string                `json:"cluster"`
	Started      []string              `json:"started"`
	Skipped      []string              `json:"skipped"`         // already on latest, or already updating
	Custom       []string              `json:"customUnmanaged"` // custom-AMI nodegroups (managed via LT)
	Failed       []string              `json:"failed"`          // describe or UpdateNodegroupVersion failed
	Verification *PostRollVerification `json:"verification,omitempty"`
}

func startNodegroupUpdates(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, nodegroups []string, flags updateAMIFlags) ([]refreshTypes.UpdateProgress, updateOutcomes) {
	skipLatest := newLatestAMISkipChecker(ctx, awsCfg, eksClient, clusterName, flags)
	human := !flags.quiet && flags.format != "json"

	outcomes := updateOutcomes{Cluster: clusterName}
	updates := make([]refreshTypes.UpdateProgress, 0, len(nodegroups))
	for _, ng := range nodegroups {
		desc, err := eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(ng),
		})
		if err != nil {
			color.Red("Failed to describe nodegroup %s: %v", ng, err)
			outcomes.Failed = append(outcomes.Failed, ng)
			continue
		}
		if desc.Nodegroup == nil {
			color.Red("Failed to describe nodegroup %s: empty response", ng)
			outcomes.Failed = append(outcomes.Failed, ng)
			continue
		}
		// Custom-AMI nodegroups: EKS doesn't manage the AMI (it lives in the
		// user's launch template), so UpdateNodegroupVersion can't pick a
		// recommended AMI. Skip with clear guidance instead of mis-rolling.
		if desc.Nodegroup.AmiType == ekstypes.AMITypesCustom {
			color.Yellow("Nodegroup %s uses a custom AMI (AmiType=CUSTOM); refresh can't select a recommended AMI.", ng)
			color.Yellow("  Publish a new launch template version with your AMI and roll it (e.g. update the LT, then `nodegroup update --force`).")
			outcomes.Custom = append(outcomes.Custom, ng)
			continue
		}
		if desc.Nodegroup.Status == ekstypes.NodegroupStatusUpdating {
			color.Yellow("Nodegroup %s is already UPDATING. Skipping update.", ng)
			outcomes.Skipped = append(outcomes.Skipped, ng)
			continue
		}
		if skipLatest(desc.Nodegroup) {
			color.Green("Nodegroup %s is already on the latest AMI. Skipping (use --force to update anyway).", ng)
			outcomes.Skipped = append(outcomes.Skipped, ng)
			continue
		}
		if human {
			color.Cyan("Starting update for nodegroup %s...", ng)
		}

		// ClientRequestToken makes the mutating call idempotent: a retry (or a
		// fleet run that revisits a cluster) won't trigger a second AMI rollout.
		resp, err := eksClient.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
			ClusterName:        aws.String(clusterName),
			NodegroupName:      aws.String(ng),
			Force:              flags.force,
			ClientRequestToken: aws.String(common.IdempotencyToken()),
		})
		if err != nil {
			color.Red("Failed to update nodegroup %s: %v", ng, err)
			outcomes.Failed = append(outcomes.Failed, ng)
			continue
		}
		if resp.Update == nil || resp.Update.Id == nil {
			color.Red("Update for nodegroup %s returned no update ID", ng)
			outcomes.Failed = append(outcomes.Failed, ng)
			continue
		}

		now := time.Now()
		updates = append(updates, refreshTypes.UpdateProgress{
			NodegroupName: ng,
			UpdateID:      *resp.Update.Id,
			ClusterName:   clusterName,
			Status:        resp.Update.Status,
			StartTime:     now,
			LastChecked:   now,
		})
		outcomes.Started = append(outcomes.Started, ng)
		if human {
			color.Green("Update started for nodegroup %s (ID: %s)", ng, *resp.Update.Id)
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
	ssmClient := ssm.NewFromConfig(awsCfg)
	latestByType := make(map[ekstypes.AMITypes]string)

	return func(ng *ekstypes.Nodegroup) bool {
		latest, ok := latestByType[ng.AmiType]
		if !ok {
			latest = awsinternal.LatestAmiIDForType(ctx, ssmClient, k8sVersion, ng.AmiType)
			latestByType[ng.AmiType] = latest
		}
		if latest == "" {
			return false
		}
		current := awsinternal.CurrentAmiID(ctx, ng, ec2Client, asgClient)
		return current != "" && current == latest
	}
}

// updateClusterAndNodegroupPatterns resolves the (cluster, nodegroup) slots
// from flags and positionals via the shared runner helpers.
func updateClusterAndNodegroupPatterns(cmd *cli.Command) (string, string) {
	clusterPattern := runner.RequestedCluster(cmd)
	nodegroupPattern := runner.PositionalSlot(cmd, "nodegroup", "cluster")
	return clusterPattern, nodegroupPattern
}
