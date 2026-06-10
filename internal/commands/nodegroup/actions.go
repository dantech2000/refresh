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
	"github.com/urfave/cli/v2"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/dryrun"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/monitoring"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
)

func runList(c *cli.Context) error {
	ctx, cancel, awsCfg, err := runner.SetupAWS(c)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, awsCfg, c)
	if err != nil || listed {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := factory.NewNodegroupService(awsCfg, false, logger)

	filters := runner.ParseFilters(c.StringSlice("filter"))
	opts := nodegroupsvc.ListOptions{
		ShowCosts:       c.Bool("show-costs"),
		ShowUtilization: c.Bool("show-utilization"),
		Filters:         filters,
		Timeframe:       c.String("timeframe"),
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

	format := strings.ToLower(c.String("format"))
	if format == "table" || format == "" {
		items = sortNodegroupSummaries(items, c.String("sort"), c.Bool("desc"))
	}

	payload := map[string]any{"cluster": clusterName, "nodegroups": items, "count": len(items)}
	if handled, err := runner.EncodeStdout(c.String("format"), payload); handled {
		return err
	}
	return outputNodegroupsTable(clusterName, c.String("timeframe"), items, time.Since(start), opts)
}

func runDescribe(c *cli.Context) error {
	ctx, cancel, awsCfg, err := runner.SetupAWS(c)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, awsCfg, c)
	if err != nil || listed {
		return err
	}

	ngName := runner.PositionalSlot(c, "nodegroup", "cluster")
	if ngName == "" {
		return fmt.Errorf("missing nodegroup name; pass as second argument or --nodegroup <name>")
	}

	logger := factory.NewDefaultLogger(nil)
	svc := factory.NewNodegroupService(awsCfg, false, logger)

	opts := nodegroupsvc.DescribeOptions{
		ShowInstances:   c.Bool("show-instances"),
		ShowUtilization: c.Bool("show-utilization"),
		ShowWorkloads:   c.Bool("show-workloads"),
		ShowCosts:       c.Bool("show-costs"),
		Timeframe:       c.String("timeframe"),
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

	if handled, err := runner.EncodeStdout(c.String("format"), details); handled {
		return err
	}
	return outputNodegroupDetailsTable(details, time.Since(start))
}

func runScale(c *cli.Context) error {
	ctx, cancel, awsCfg, err := runner.SetupAWS(c)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, c.String("cluster"))
	if err != nil {
		return err
	}

	logger := factory.NewDefaultLogger(nil)
	withHealth := c.Bool("health-check") || c.Bool("check-pdbs") || c.Bool("wait")
	svc := factory.NewNodegroupService(awsCfg, withHealth, logger)

	opts := nodegroupsvc.ScaleOptions{
		HealthCheck: c.Bool("health-check"),
		CheckPDBs:   c.Bool("check-pdbs"),
		Wait:        c.Bool("wait"),
		Timeout:     c.Duration("op-timeout"),
		DryRun:      c.Bool("dry-run"),
	}

	desired, minSize, maxSize := int32PtrIfSet(c, "desired"), int32PtrIfSet(c, "min"), int32PtrIfSet(c, "max")

	if opts.DryRun {
		return printScaleDryRun(ctx, eks.NewFromConfig(awsCfg), clusterName, c.String("nodegroup"), desired, minSize, maxSize)
	}

	return runner.WithSpinner("nodegroup", "Scaling request submitted", func() error {
		return svc.Scale(ctx, clusterName, c.String("nodegroup"), desired, minSize, maxSize, opts)
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

// int32PtrIfSet returns &v for c.Int(name) when the flag was explicitly set,
// otherwise nil.
func int32PtrIfSet(c *cli.Context, name string) *int32 {
	if !c.IsSet(name) {
		return nil
	}
	v := int32(c.Int(name))
	return &v
}

// updateAMIFlags collects the flags that govern runUpdateAMI's behavior.
type updateAMIFlags struct {
	force, dryRun, noWait, quiet, skipHealthCheck, healthOnly bool
	timeout, pollInterval                                     time.Duration
}

func readUpdateAMIFlags(c *cli.Context) updateAMIFlags {
	// Flags placed after positional args (e.g. `update-ami my-cluster
	// --health-only`) are not parsed by urfave/cli; apply them first.
	runner.ApplyTrailingFlags(c)
	return updateAMIFlags{
		force:           c.Bool("force"),
		dryRun:          c.Bool("dry-run"),
		noWait:          c.Bool("no-wait"),
		quiet:           c.Bool("quiet"),
		skipHealthCheck: c.Bool("skip-health-check"),
		healthOnly:      c.Bool("health-only"),
		timeout:         c.Duration("timeout"),
		pollInterval:    c.Duration("poll-interval"),
	}
}

func runUpdateAMI(c *cli.Context) error {
	ctx, cancel, awsCfg, err := runner.SetupAWSWithTimeout(c, 60*time.Second)
	if err != nil {
		return err
	}
	defer cancel()

	requestedCluster, nodegroupPattern := updateClusterAndNodegroupPatterns(c)
	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, requestedCluster)
	if err != nil {
		color.Red("%v", err)
		return err
	}
	eksClient := eks.NewFromConfig(awsCfg)
	flags := readUpdateAMIFlags(c)

	done, err := preflightHealthCheck(ctx, awsCfg, eksClient, clusterName, flags)
	if err != nil || done {
		return err
	}

	selectedNodegroups, err := selectNodegroupsForUpdate(ctx, eksClient, clusterName, nodegroupPattern)
	if err != nil {
		return err
	}

	if flags.dryRun {
		return dryrun.PerformDryRun(ctx, awsCfg, eksClient, clusterName, selectedNodegroups, flags.force, flags.quiet)
	}

	updates := startNodegroupUpdates(ctx, awsCfg, eksClient, clusterName, selectedNodegroups, flags)
	if len(updates) == 0 {
		color.Yellow("No nodegroup updates were started")
		return nil
	}
	if flags.noWait {
		if !flags.quiet {
			fmt.Printf("Started %d nodegroup update(s). Use 'refresh list --cluster %s' to check status.\n",
				len(updates), clusterName)
		}
		return nil
	}

	monitor := &refreshTypes.ProgressMonitor{
		Updates:   updates,
		StartTime: time.Now(),
		Quiet:     flags.quiet,
		NoWait:    flags.noWait,
		Timeout:   flags.timeout,
	}
	config := refreshTypes.MonitorConfig{
		PollInterval:    flags.pollInterval,
		MaxRetries:      3,
		BackoffMultiple: 2.0,
		Quiet:           flags.quiet,
		NoWait:          flags.noWait,
		Timeout:         flags.timeout,
	}
	return monitoring.MonitorUpdates(ctx, eksClient, monitor, config)
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

	if !flags.quiet {
		ui.DisplayHealthCheckStart(clusterName)
	}
	cwClient := cloudwatch.NewFromConfig(awsCfg)
	asgClient := autoscaling.NewFromConfig(awsCfg)
	k8sClient, k8sErr := health.GetKubernetesClient()
	if k8sClient == nil && !flags.quiet {
		color.Yellow("Warning: Kubernetes client not available (%v)", k8sErr)
		color.Yellow("Health checks will be limited to AWS-only validations")
	}
	checker := health.NewChecker(eksClient, k8sClient, cwClient, asgClient)

	spinner := ui.NewFunSpinnerForCategory("health")
	if !flags.quiet {
		if err := spinner.Start(); err != nil {
			return false, err
		}
		defer spinner.Stop()
	}
	summary := checker.RunAllChecks(ctx, clusterName)
	if !flags.quiet {
		spinner.Success("Health validation complete!")
		ui.DisplayHealthResults(summary)
	}

	return applyHealthDecision(summary, flags)
}

// applyHealthDecision interprets a health summary against the run flags. It
// returns done=true when the caller should stop (Block decision, user
// cancelled, or --health-only).
//
// --health-only means "just show me the verdict": the success banner is
// printed regardless of --quiet because the verdict IS the requested result.
// --quiet only suppresses the verbose banner for the non-health-only flow.
func applyHealthDecision(summary health.HealthSummary, flags updateAMIFlags) (done bool, err error) {
	switch summary.Decision {
	case health.DecisionBlock:
		ui.DisplayHealthCheckComplete(summary.Decision)
		return true, fmt.Errorf("pre-flight health checks failed")
	case health.DecisionWarn:
		if flags.healthOnly {
			ui.DisplayHealthCheckComplete(summary.Decision)
			return true, nil
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
func selectNodegroupsForUpdate(ctx context.Context, eksClient *eks.Client, clusterName, pattern string) ([]string, error) {
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
func startNodegroupUpdates(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, nodegroups []string, flags updateAMIFlags) []refreshTypes.UpdateProgress {
	skipLatest := newLatestAMISkipChecker(ctx, awsCfg, eksClient, clusterName, flags)

	updates := make([]refreshTypes.UpdateProgress, 0, len(nodegroups))
	for _, ng := range nodegroups {
		desc, err := eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(ng),
		})
		if err != nil {
			color.Red("Failed to describe nodegroup %s: %v", ng, err)
			continue
		}
		if desc.Nodegroup.Status == ekstypes.NodegroupStatusUpdating {
			color.Yellow("Nodegroup %s is already UPDATING. Skipping update.", ng)
			continue
		}
		if skipLatest(desc.Nodegroup) {
			color.Green("Nodegroup %s is already on the latest AMI. Skipping (use --force to update anyway).", ng)
			continue
		}
		if !flags.quiet {
			color.Cyan("Starting update for nodegroup %s...", ng)
		}

		resp, err := eksClient.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(ng),
			Force:         flags.force,
		})
		if err != nil {
			color.Red("Failed to update nodegroup %s: %v", ng, err)
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
		if !flags.quiet {
			color.Green("Update started for nodegroup %s (ID: %s)", ng, *resp.Update.Id)
		}
	}
	return updates
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
// from flags and positionals via the shared runner helpers, which also apply
// any flags that appear after the positional arguments.
func updateClusterAndNodegroupPatterns(c *cli.Context) (string, string) {
	clusterPattern := runner.RequestedCluster(c)
	nodegroupPattern := runner.PositionalSlot(c, "nodegroup", "cluster")
	return clusterPattern, nodegroupPattern
}
