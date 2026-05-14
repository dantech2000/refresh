package nodegroup

import (
	"context"
	"encoding/json"
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
	"gopkg.in/yaml.v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/awsconfig"
	clustercmd "github.com/dantech2000/refresh/internal/commands/cluster"
	"github.com/dantech2000/refresh/internal/commands/factory"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/dryrun"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/monitoring"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
)

func runList(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	awsCfg, err := awsconfig.Load(ctx, c)
	if err != nil {
		color.Red("Failed to load AWS config: %v", err)
		return err
	}
	if err := awsinternal.CheckAWSCredentials(ctx, awsCfg); err != nil {
		return err
	}

	requested := c.Args().First()
	if requested == "" {
		requested = c.String("cluster")
	}
	if strings.TrimSpace(requested) == "" {
		ui.Outln("No cluster specified. Available clusters:")
		ui.Outln()
		start := time.Now()
		svcList := factory.NewClusterService(awsCfg, false, nil)
		summaries, err := svcList.List(ctx, clustersvc.ListOptions{})
		if err != nil {
			return err
		}
		return clustercmd.OutputClustersTable(summaries, time.Since(start), false, false)
	}
	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, requested)
	if err != nil {
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

	spinner := ui.NewFunSpinnerForCategory("nodegroup")
	if err := spinner.Start(); err != nil {
		return err
	}
	defer spinner.Stop()

	start := time.Now()
	items, err := svc.List(ctx, clusterName, opts)
	spinner.Success("Nodegroup information gathered!")
	if err != nil {
		return err
	}

	if strings.ToLower(c.String("format")) == "table" || c.String("format") == "" {
		items = sortNodegroupSummaries(items, c.String("sort"), c.Bool("desc"))
	}

	switch strings.ToLower(c.String("format")) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"cluster": clusterName, "nodegroups": items, "count": len(items)})
	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(map[string]any{"cluster": clusterName, "nodegroups": items, "count": len(items)})
	default:
		return outputNodegroupsTable(clusterName, c.String("timeframe"), items, time.Since(start), opts)
	}
}

func runDescribe(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	awsCfg, err := awsconfig.Load(ctx, c)
	if err != nil {
		color.Red("Failed to load AWS config: %v", err)
		return err
	}
	if err := awsinternal.CheckAWSCredentials(ctx, awsCfg); err != nil {
		return err
	}

	requested := c.Args().First()
	if requested == "" {
		requested = c.String("cluster")
	}
	if strings.TrimSpace(requested) == "" {
		ui.Outln("No cluster specified. Available clusters:")
		ui.Outln()
		svc := factory.NewClusterService(awsCfg, false, nil)
		start := time.Now()
		summaries, err := svc.List(ctx, clustersvc.ListOptions{})
		if err != nil {
			return err
		}
		return clustercmd.OutputClustersTable(summaries, time.Since(start), false, false)
	}
	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, requested)
	if err != nil {
		return err
	}

	ngName := c.String("nodegroup")
	if strings.TrimSpace(ngName) == "" {
		var nonFlags []string
		for _, tok := range c.Args().Slice() {
			if strings.HasPrefix(tok, "-") {
				continue
			}
			nonFlags = append(nonFlags, tok)
		}
		if len(nonFlags) >= 2 {
			ngName = nonFlags[1]
		}
	}
	if strings.TrimSpace(ngName) == "" {
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

	spinner := ui.NewFunSpinnerForCategory("nodegroup")
	if err := spinner.Start(); err != nil {
		return err
	}
	defer spinner.Stop()

	start := time.Now()
	details, err := svc.Describe(ctx, clusterName, ngName, opts)
	spinner.Success("Nodegroup details gathered!")
	if err != nil {
		return err
	}

	switch strings.ToLower(c.String("format")) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(details)
	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(details)
	default:
		return outputNodegroupDetailsTable(details, time.Since(start))
	}
}

func runScale(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	awsCfg, err := awsconfig.Load(ctx, c)
	if err != nil {
		color.Red("Failed to load AWS config: %v", err)
		return err
	}
	if err := awsinternal.CheckAWSCredentials(ctx, awsCfg); err != nil {
		return err
	}

	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, c.String("cluster"))
	if err != nil {
		return err
	}

	ngName := c.String("nodegroup")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	withHealth := c.Bool("health-check") || c.Bool("check-pdbs") || c.Bool("wait")
	svc := factory.NewNodegroupService(awsCfg, withHealth, logger)

	var desiredPtr, minPtr, maxPtr *int32
	if c.IsSet("desired") {
		v := int32(c.Int("desired"))
		desiredPtr = &v
	}
	if c.IsSet("min") {
		v := int32(c.Int("min"))
		minPtr = &v
	}
	if c.IsSet("max") {
		v := int32(c.Int("max"))
		maxPtr = &v
	}

	opts := nodegroupsvc.ScaleOptions{
		HealthCheck: c.Bool("health-check"),
		CheckPDBs:   c.Bool("check-pdbs"),
		Wait:        c.Bool("wait"),
		Timeout:     c.Duration("op-timeout"),
		DryRun:      c.Bool("dry-run"),
	}

	spinner := ui.NewFunSpinnerForCategory("nodegroup")
	if err := spinner.Start(); err != nil {
		return err
	}
	defer spinner.Stop()

	if err := svc.Scale(ctx, clusterName, ngName, desiredPtr, minPtr, maxPtr, opts); err != nil {
		spinner.Fail("Scaling failed")
		return err
	}
	spinner.Success("Scaling request submitted")
	return nil
}

func runUpdateAMI(c *cli.Context) error {
	globalTimeout := c.Duration("timeout")
	if globalTimeout == 0 {
		globalTimeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), globalTimeout)
	defer cancel()

	awsCfg, err := awsconfig.Load(ctx, c)
	if err != nil {
		color.Red("Failed to load AWS config: %v", err)
		return err
	}
	if err := awsinternal.CheckAWSCredentials(ctx, awsCfg); err != nil {
		return err
	}

	requestedCluster, nodegroupPattern := updateClusterAndNodegroupPatterns(c)
	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, requestedCluster)
	if err != nil {
		color.Red("%v", err)
		return err
	}
	eksClient := eks.NewFromConfig(awsCfg)

	force := c.Bool("force")
	dryRun := c.Bool("dry-run")
	noWait := c.Bool("no-wait")
	quiet := c.Bool("quiet")
	timeout := c.Duration("timeout")
	pollInterval := c.Duration("poll-interval")
	skipHealthCheck := c.Bool("skip-health-check")
	healthOnly := updateBoolFlag(c, "health-only", "H")

	if !skipHealthCheck && !dryRun && !force {
		if !quiet {
			ui.DisplayHealthCheckStart(clusterName)
		}
		cwClient := cloudwatch.NewFromConfig(awsCfg)
		asgClient := autoscaling.NewFromConfig(awsCfg)
		k8sClient, k8sErr := health.GetKubernetesClient()
		if k8sClient == nil && !quiet {
			color.Yellow("Warning: Kubernetes client not available (%v)", k8sErr)
			color.Yellow("Health checks will be limited to AWS-only validations")
		}
		healthChecker := health.NewChecker(eksClient, k8sClient, cwClient, asgClient)

		spinner := ui.NewFunSpinnerForCategory("health")
		if !quiet {
			if err := spinner.Start(); err != nil {
				return err
			}
			defer spinner.Stop()
		}
		summary := healthChecker.RunAllChecks(ctx, clusterName)
		if !quiet {
			spinner.Success("Health validation complete!")
		}
		if !quiet {
			ui.DisplayHealthResults(summary)
		}

		switch summary.Decision {
		case health.DecisionBlock:
			ui.DisplayHealthCheckComplete(summary.Decision)
			return fmt.Errorf("pre-flight health checks failed")
		case health.DecisionWarn:
			if healthOnly {
				return nil
			}
			if !quiet && !ui.PromptContinueWithWarnings(summary.Warnings) {
				color.Yellow("Update cancelled by user")
				return fmt.Errorf("update cancelled")
			}
		case health.DecisionProceed:
			if healthOnly {
				ui.DisplayHealthCheckComplete(summary.Decision)
				return nil
			}
			if !quiet {
				ui.DisplayHealthCheckComplete(summary.Decision)
			}
		}
	} else if healthOnly {
		color.Yellow("Health check skipped due to --skip-health-check, --dry-run, or --force flags")
		return nil
	}

	ngOut, err := eksClient.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String(clusterName)})
	if err != nil {
		color.Red("Failed to list nodegroups: %v", err)
		return err
	}

	matches := awsinternal.MatchingNodegroups(ngOut.Nodegroups, nodegroupPattern)
	selectedNodegroups, err := awsinternal.ConfirmNodegroupSelection(matches, nodegroupPattern)
	if err != nil {
		color.Red("%v", err)
		return err
	}

	config := refreshTypes.MonitorConfig{
		PollInterval:    pollInterval,
		MaxRetries:      3,
		BackoffMultiple: 2.0,
		Quiet:           quiet,
		NoWait:          noWait,
		Timeout:         timeout,
	}
	monitor := &refreshTypes.ProgressMonitor{
		Updates:   make([]refreshTypes.UpdateProgress, 0),
		StartTime: time.Now(),
		Quiet:     quiet,
		NoWait:    noWait,
		Timeout:   timeout,
	}

	if dryRun {
		return dryrun.PerformDryRun(ctx, eksClient, clusterName, selectedNodegroups, force, quiet)
	}

	for _, ng := range selectedNodegroups {
		ngDesc, err := eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(ng),
		})
		if err != nil {
			color.Red("Failed to describe nodegroup %s: %v", ng, err)
			continue
		}
		if ngDesc.Nodegroup.Status == ekstypes.NodegroupStatusUpdating {
			color.Yellow("Nodegroup %s is already UPDATING. Skipping update.", ng)
			continue
		}
		if !quiet {
			color.Cyan("Starting update for nodegroup %s...", ng)
		}

		updateResp, err := eksClient.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(ng),
			Force:         force,
		})
		if err != nil {
			color.Red("Failed to update nodegroup %s: %v", ng, err)
			continue
		}

		monitor.Updates = append(monitor.Updates, refreshTypes.UpdateProgress{
			NodegroupName: ng,
			UpdateID:      *updateResp.Update.Id,
			ClusterName:   clusterName,
			Status:        updateResp.Update.Status,
			StartTime:     time.Now(),
			LastChecked:   time.Now(),
		})
		if !quiet {
			color.Green("Update started for nodegroup %s (ID: %s)", ng, *updateResp.Update.Id)
		}
	}

	if len(monitor.Updates) == 0 {
		color.Yellow("No nodegroup updates were started")
		return nil
	}
	if noWait {
		if !quiet {
			fmt.Printf("Started %d nodegroup update(s). Use 'refresh list --cluster %s' to check status.\n",
				len(monitor.Updates), clusterName)
		}
		return nil
	}
	return monitoring.MonitorUpdates(ctx, eksClient, monitor, config)
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
