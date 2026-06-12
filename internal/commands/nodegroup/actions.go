package nodegroup

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
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
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
)

func runList(ctx context.Context, cmd *cli.Command) error {
	// Each --watch iteration performs the full setup+fetch+render cycle so a
	// fresh service (and cache) is used every time.
	return runner.Watch(cmd, func() error { return listNodegroupsOnce(ctx, cmd) })
}

func listNodegroupsOnce(ctx context.Context, cmd *cli.Command) error {
	ctx, cancel, awsCfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, awsCfg, cmd)
	if err != nil || listed {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := factory.NewNodegroupService(awsCfg, false, logger)

	filters := runner.ParseFilters(cmd.StringSlice("filter"))
	opts := nodegroupsvc.ListOptions{
		ShowCosts:       cmd.Bool("show-costs"),
		ShowUtilization: cmd.Bool("show-utilization"),
		Filters:         filters,
		Timeframe:       cmd.String("timeframe"),
	}

	var items []nodegroupsvc.NodegroupSummary
	start := time.Now()
	if err := runner.WithSpinner("nodegroup", "Nodegroup information gathered!", func() error {
		var lerr error
		items, lerr = svc.List(ctx, clusterName, opts)
		return lerr
	}); err != nil {
		return err
	}

	format := strings.ToLower(cmd.String("format"))
	if format == "table" || format == "plain" || format == "" {
		items = sortNodegroupSummaries(items, cmd.String("sort"), cmd.Bool("desc"))
	}

	payload := map[string]any{"cluster": clusterName, "nodegroups": items, "count": len(items)}
	if handled, err := runner.EncodeStdout(cmd.String("format"), payload); handled {
		return err
	}
	return outputNodegroupsTable(clusterName, cmd.String("timeframe"), items, time.Since(start), opts)
}

func runDescribe(ctx context.Context, cmd *cli.Command) error {
	ctx, cancel, awsCfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, awsCfg, cmd)
	if err != nil || listed {
		return err
	}

	ngName := runner.PositionalSlot(cmd, "nodegroup", "cluster")
	if ngName == "" {
		return fmt.Errorf("missing nodegroup name; pass as second argument or --nodegroup <name>")
	}

	logger := factory.NewDefaultLogger(nil)
	svc := factory.NewNodegroupService(awsCfg, false, logger)

	opts := nodegroupsvc.DescribeOptions{
		ShowInstances:   cmd.Bool("show-instances"),
		ShowUtilization: cmd.Bool("show-utilization"),
		ShowWorkloads:   cmd.Bool("show-workloads"),
		ShowCosts:       cmd.Bool("show-costs"),
		Timeframe:       cmd.String("timeframe"),
	}

	var details *nodegroupsvc.NodegroupDetails
	start := time.Now()
	if err := runner.WithSpinner("nodegroup", "Nodegroup details gathered!", func() error {
		var derr error
		details, derr = svc.Describe(ctx, clusterName, ngName, opts)
		return derr
	}); err != nil {
		return err
	}

	if handled, err := runner.EncodeStdout(cmd.String("format"), details); handled {
		return err
	}
	return outputNodegroupDetailsTable(details, time.Since(start))
}

func runScale(ctx context.Context, cmd *cli.Command) error {
	ctx, cancel, awsCfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, cmd.String("cluster"))
	if err != nil {
		return err
	}

	logger := factory.NewDefaultLogger(nil)
	withHealth := cmd.Bool("health-check") || cmd.Bool("check-pdbs") || cmd.Bool("wait")
	svc := factory.NewNodegroupService(awsCfg, withHealth, logger)

	opts := nodegroupsvc.ScaleOptions{
		HealthCheck: cmd.Bool("health-check"),
		CheckPDBs:   cmd.Bool("check-pdbs"),
		Wait:        cmd.Bool("wait"),
		Timeout:     cmd.Duration("op-timeout"),
		DryRun:      cmd.Bool("dry-run"),
	}

	desired, minSize, maxSize := int32PtrIfSet(cmd, "desired"), int32PtrIfSet(cmd, "min"), int32PtrIfSet(cmd, "max")

	if opts.DryRun {
		return printScaleDryRun(ctx, eks.NewFromConfig(awsCfg), clusterName, cmd.String("nodegroup"), desired, minSize, maxSize)
	}

	return runner.WithSpinner("nodegroup", "Scaling request submitted", func() error {
		return svc.Scale(ctx, clusterName, cmd.String("nodegroup"), desired, minSize, maxSize, opts)
	})
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
	fmt.Println("\nNo changes were made. Re-run without --dry-run to execute.")
	return nil
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

// updateAMIFlags collects the flags that govern runUpdateAMI's behavior.
type updateAMIFlags struct {
	force, dryRun, noWait, quiet, skipHealthCheck, healthOnly bool
	yes, requireHealthy, skipVerify                           bool
	timeout, pollInterval                                     time.Duration
	format                                                    string
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
		timeout:         cmd.Duration("timeout"),
		pollInterval:    cmd.Duration("poll-interval"),
		format:          strings.ToLower(cmd.String("format")),
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

	if flags.dryRun {
		return dryrun.PerformDryRun(ctx, awsCfg, eksClient, clusterName, selectedNodegroups, flags.force, flags.quiet)
	}

	jsonOut := flags.format == "json" && !flags.healthOnly
	quiet := flags.quiet || jsonOut

	// Snapshot Pending pods before the roll so post-roll verification can tell
	// pre-existing stuck pods from ones this roll left behind (best-effort kube).
	verify := !flags.skipVerify && !flags.noWait
	var verifyClient kubernetes.Interface
	var preroll pendingPodSet
	if verify {
		verifyClient, _ = health.GetKubernetesClient()
		preroll = snapshotPendingPods(ctx, verifyClient)
	}

	updates, outcomes := startNodegroupUpdates(ctx, awsCfg, eksClient, clusterName, selectedNodegroups, flags)

	if len(updates) == 0 {
		if jsonOut {
			if _, err := runner.EncodeStdout("json", outcomes); err != nil {
				return err
			}
		} else if !quiet {
			color.Yellow("No nodegroup updates were started")
		}
		return updateExit(outcomes, nil, false)
	}
	if flags.noWait {
		if jsonOut {
			if _, err := runner.EncodeStdout("json", outcomes); err != nil {
				return err
			}
		} else if !quiet {
			fmt.Printf("Started %d nodegroup update(s). Use 'refresh list --cluster %s' to check status.\n",
				len(updates), clusterName)
		}
		return updateExit(outcomes, nil, false)
	}

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
	monErr := monitoring.MonitorUpdates(ctx, eksClient, monitor, config)

	verifyFailed := false
	if verify && monErr == nil && len(outcomes.Started) > 0 {
		result := verifyPostRoll(ctx, eksClient, verifyClient, clusterName, outcomes.Started, preroll)
		outcomes.Verification = &result
		verifyFailed = !result.OK()
		if !jsonOut && !flags.quiet {
			printVerification(result)
		}
	}

	if jsonOut {
		if _, err := runner.EncodeStdout("json", outcomes); err != nil {
			return err
		}
	}
	return updateExit(outcomes, monErr, verifyFailed)
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
	k8sClient, k8sErr := health.GetKubernetesClient()
	if k8sClient == nil && humanOutput {
		color.Yellow("Warning: Kubernetes client not available (%v)", k8sErr)
		color.Yellow("Health checks will be limited to AWS-only validations")
	}
	checker := health.NewChecker(eksClient, k8sClient, cwClient, asgClient)

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
