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
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/dryrun"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/monitoring"
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

	filters := make(map[string]string)
	for _, f := range c.StringSlice("filter") {
		if parts := strings.SplitN(f, "=", 2); len(parts) == 2 {
			filters[parts[0]] = parts[1]
		}
	}
	opts := nodegroupsvc.ListOptions{
		ShowHealth:      c.Bool("show-health"),
		ShowCosts:       c.Bool("show-costs"),
		ShowUtilization: c.Bool("show-utilization"),
		ShowInstances:   c.Bool("show-instances"),
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

	ngName := runner.SecondPositional(c, "nodegroup")
	if ngName == "" {
		return fmt.Errorf("missing nodegroup name; pass as second argument or --nodegroup <name>")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	svc := factory.NewNodegroupService(awsCfg, false, logger)

	opts := nodegroupsvc.DescribeOptions{
		ShowInstances:    c.Bool("show-instances"),
		ShowUtilization:  c.Bool("show-utilization"),
		ShowWorkloads:    c.Bool("show-workloads"),
		ShowCosts:        c.Bool("show-costs"),
		ShowOptimization: c.Bool("show-optimization"),
		Timeframe:        c.String("timeframe"),
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

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	withHealth := c.Bool("health-check") || c.Bool("check-pdbs") || c.Bool("wait")
	svc := factory.NewNodegroupService(awsCfg, withHealth, logger)

	opts := nodegroupsvc.ScaleOptions{
		HealthCheck: c.Bool("health-check"),
		CheckPDBs:   c.Bool("check-pdbs"),
		Wait:        c.Bool("wait"),
		Timeout:     c.Duration("op-timeout"),
		DryRun:      c.Bool("dry-run"),
	}

	return runner.WithSpinner("nodegroup", "Scaling request submitted", func() error {
		return svc.Scale(
			ctx,
			clusterName,
			c.String("nodegroup"),
			int32PtrIfSet(c, "desired"),
			int32PtrIfSet(c, "min"),
			int32PtrIfSet(c, "max"),
			opts,
		)
	})
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
	return updateAMIFlags{
		force:           c.Bool("force"),
		dryRun:          c.Bool("dry-run"),
		noWait:          c.Bool("no-wait"),
		quiet:           c.Bool("quiet"),
		skipHealthCheck: c.Bool("skip-health-check"),
		healthOnly:      updateBoolFlag(c, "health-only", "H"),
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
		return dryrun.PerformDryRun(ctx, eksClient, clusterName, selectedNodegroups, flags.force, flags.quiet)
	}

	updates := startNodegroupUpdates(ctx, eksClient, clusterName, selectedNodegroups, flags)
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

	switch summary.Decision {
	case health.DecisionBlock:
		ui.DisplayHealthCheckComplete(summary.Decision)
		return true, fmt.Errorf("pre-flight health checks failed")
	case health.DecisionWarn:
		if flags.healthOnly {
			return true, nil
		}
		if !flags.quiet && !ui.PromptContinueWithWarnings(summary.Warnings) {
			color.Yellow("Update cancelled by user")
			return true, fmt.Errorf("update cancelled")
		}
	case health.DecisionProceed:
		if !flags.quiet {
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
	out, err := eksClient.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String(clusterName)})
	if err != nil {
		color.Red("Failed to list nodegroups: %v", err)
		return nil, err
	}
	matches := awsinternal.MatchingNodegroups(out.Nodegroups, pattern)
	selected, err := awsinternal.ConfirmNodegroupSelection(matches, pattern)
	if err != nil {
		color.Red("%v", err)
		return nil, err
	}
	return selected, nil
}

// startNodegroupUpdates issues UpdateNodegroupVersion for each selected
// nodegroup that isn't already updating, returning successful update progress
// entries. Per-nodegroup failures are logged and skipped, matching the
// original best-effort behavior.
func startNodegroupUpdates(ctx context.Context, eksClient *eks.Client, clusterName string, nodegroups []string, flags updateAMIFlags) []refreshTypes.UpdateProgress {
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

func updateClusterAndNodegroupPatterns(c *cli.Context) (string, string) {
	clusterPattern := strings.TrimSpace(c.String("cluster"))
	nodegroupPattern := strings.TrimSpace(c.String("nodegroup"))
	nonFlags := nonFlagArgs(c)

	if clusterPattern == "" && len(nonFlags) > 0 {
		clusterPattern = nonFlags[0]
	}

	if nodegroupPattern == "" {
		nodegroupArgIndex := 1
		if c.String("cluster") != "" {
			nodegroupArgIndex = 0
		}
		if len(nonFlags) > nodegroupArgIndex {
			nodegroupPattern = nonFlags[nodegroupArgIndex]
		}
	}

	return clusterPattern, nodegroupPattern
}

func nonFlagArgs(c *cli.Context) []string {
	if c == nil {
		return nil
	}
	args := c.Args().Slice()
	nonFlags := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		nonFlags = append(nonFlags, arg)
	}
	return nonFlags
}

func updateBoolFlag(c *cli.Context, name string, aliases ...string) bool {
	if c == nil {
		return false
	}
	if c.Bool(name) {
		return true
	}
	for _, arg := range c.Args().Slice() {
		if arg == "--"+name {
			return true
		}
		for _, alias := range aliases {
			if arg == "-"+alias {
				return true
			}
		}
	}
	return false
}
