package nodegroup

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/runner"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/dryrun"
	"github.com/dantech2000/refresh/internal/services/common"
)

// clusterTarget is a cluster to update plus the region-scoped AWS config to
// reach it.
type clusterTarget struct {
	cluster string
	region  string
	awsCfg  aws.Config
}

// clusterUpdateResult is one cluster's outcome within a fleet run.
type clusterUpdateResult struct {
	Cluster       string         `json:"cluster" yaml:"cluster"`
	Region        string         `json:"region" yaml:"region"`
	Outcomes      updateOutcomes `json:"outcomes" yaml:"outcomes"`
	HealthBlocked bool           `json:"healthBlocked" yaml:"healthBlocked"`
	VerifyFailed  bool           `json:"verifyFailed" yaml:"verifyFailed"`
	Error         string         `json:"error,omitempty" yaml:"error,omitempty"`
}

// runFleetUpdate is "patch Tuesday": discover clusters across regions and roll
// matching nodegroups serially (blast-radius control), with one batch
// confirmation, an aggregate summary, and a worst-outcome exit code.
func runFleetUpdate(ctx context.Context, cmd *cli.Command) error {
	// No overall deadline: clusters roll serially, so one --timeout across the
	// whole fleet would starve later clusters. --timeout applies per cluster
	// (see updateOneClusterInFleet); the run stays signal-cancellable.
	ctx, cancel, awsCfg, err := runner.SetupAWSWithDeadline(ctx, cmd, 0)
	if err != nil {
		return err
	}
	defer cancel()

	flags := readUpdateAMIFlags(cmd)
	nodegroupPattern := cmd.String("nodegroup")
	jsonOut := flags.format == "json" && !flags.healthOnly

	regions := resolveUpdateRegions(cmd, awsCfg)
	// Discovery is bounded by --timeout so a stalled region can't hang an
	// unattended run.
	discoverCtx, cancelDiscover := fleetClusterContext(ctx, flags.timeout)
	targets, err := discoverFleetTargets(discoverCtx, awsCfg, regions)
	cancelDiscover()
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		color.Yellow("No clusters found across %d region(s)", len(regions))
		return nil
	}

	if flags.dryRun {
		return fleetDryRun(ctx, targets, nodegroupPattern, flags)
	}

	// One confirmation for the whole batch (or --yes); without a TTY, require
	// --yes rather than hang.
	if !flags.yes {
		if !isInteractive() {
			return fmt.Errorf("fleet update would modify %d cluster(s); re-run with --yes (no interactive terminal for confirmation)", len(targets))
		}
		if !promptYesNo(fmt.Sprintf("Update matching nodegroups across %d cluster(s) in %d region(s)?", len(targets), len(regions))) {
			color.Yellow("Fleet update cancelled")
			return fmt.Errorf("fleet update cancelled")
		}
	}

	// Per-cluster confirmations are suppressed — the batch was already confirmed.
	cflags := flags
	cflags.yes = true

	results := make([]clusterUpdateResult, 0, len(targets))
	for _, tgt := range targets {
		if ctx.Err() != nil {
			color.Yellow("Interrupted: %d of %d cluster(s) not started", len(targets)-len(results), len(targets))
			break
		}
		if !flags.quiet && !jsonOut {
			color.Cyan("\n=== %s (%s) ===", tgt.cluster, tgt.region)
		}
		results = append(results, updateOneClusterInFleet(ctx, tgt, nodegroupPattern, cflags))
	}

	if jsonOut {
		if _, err := runner.EncodeStdout("json", map[string]any{"clusters": results}); err != nil {
			return err
		}
	} else {
		printFleetSummary(results)
	}
	return fleetExit(results)
}

// updateOneClusterInFleet runs the per-cluster pipeline (health gate → select →
// roll → verify) and captures the outcome instead of exiting, so the fleet loop
// can aggregate.
func updateOneClusterInFleet(ctx context.Context, tgt clusterTarget, nodegroupPattern string, flags updateAMIFlags) clusterUpdateResult {
	res := clusterUpdateResult{Cluster: tgt.cluster, Region: tgt.region}
	eksClient := eks.NewFromConfig(tgt.awsCfg)

	ctx, cancel := fleetClusterContext(ctx, flags.timeout)
	defer cancel()

	done, err := preflightHealthCheck(ctx, tgt.awsCfg, eksClient, tgt.cluster, flags)
	if err != nil {
		// Block (or, in unattended mode, a warn-level hard stop).
		res.HealthBlocked = true
		res.Error = err.Error()
		return res
	}
	if done {
		return res
	}

	selected, err := selectNodegroupsForUpdate(ctx, eksClient, tgt.cluster, nodegroupPattern, true)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	outcomes, verifyFailed, monErr := executeUpdates(ctx, tgt.awsCfg, eksClient, tgt.cluster, selected, flags)
	res.Outcomes = outcomes
	res.VerifyFailed = verifyFailed
	if monErr != nil {
		res.Error = monErr.Error()
	}
	return res
}

// fleetClusterContext scopes --timeout to a single cluster in a fleet run
// (health gate + roll + verify). timeout <= 0 means no per-cluster limit.
func fleetClusterContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

// resolveUpdateRegions picks the regions to sweep for --all-clusters: explicit
// --region wins, then REFRESH_EKS_REGIONS, else the partition's EKS regions.
func resolveUpdateRegions(cmd *cli.Command, awsCfg aws.Config) []string {
	if r := cmd.StringSlice("region"); len(r) > 0 {
		return r
	}
	if env := appconfig.RegionsFromEnv(); len(env) > 0 {
		return env
	}
	return appconfig.GetRegionsForPartition(awsCfg.Region)
}

// discoverFleetTargets lists clusters in each region (bounded concurrency) and
// returns one target per cluster with a region-scoped config.
func discoverFleetTargets(ctx context.Context, baseCfg aws.Config, regions []string) ([]clusterTarget, error) {
	perRegion := common.ForEachParallel(ctx, regions, common.DefaultItemConcurrency,
		func(fctx context.Context, region string) []clusterTarget {
			cfg := baseCfg.Copy()
			cfg.Region = region
			eksClient := eks.NewFromConfig(cfg)
			names, err := awsinternal.ListAllPages(fctx, fmt.Sprintf("listing clusters in %s", region),
				func(rc context.Context, token *string) (*eks.ListClustersOutput, error) {
					return eksClient.ListClusters(rc, &eks.ListClustersInput{NextToken: token})
				},
				func(out *eks.ListClustersOutput) ([]string, *string) { return out.Clusters, out.NextToken },
			)
			if err != nil {
				return nil
			}
			targets := make([]clusterTarget, 0, len(names))
			for _, n := range names {
				targets = append(targets, clusterTarget{cluster: n, region: region, awsCfg: cfg})
			}
			return targets
		})

	var all []clusterTarget
	for _, ts := range perRegion {
		all = append(all, ts...)
	}
	return all, nil
}

// fleetDryRun prints the per-cluster plan without mutating anything.
func fleetDryRun(ctx context.Context, targets []clusterTarget, nodegroupPattern string, flags updateAMIFlags) error {
	color.Cyan("Fleet dry-run: %d cluster(s)", len(targets))
	for _, tgt := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		color.Cyan("\n=== %s (%s) ===", tgt.cluster, tgt.region)
		fleetDryRunCluster(ctx, tgt, nodegroupPattern, flags)
	}
	return nil
}

// fleetDryRunCluster previews one cluster under a per-cluster --timeout.
func fleetDryRunCluster(ctx context.Context, tgt clusterTarget, nodegroupPattern string, flags updateAMIFlags) {
	ctx, cancel := fleetClusterContext(ctx, flags.timeout)
	defer cancel()
	eksClient := eks.NewFromConfig(tgt.awsCfg)
	selected, err := selectNodegroupsForUpdate(ctx, eksClient, tgt.cluster, nodegroupPattern, true)
	if err != nil {
		color.Red("  %v", err)
		return
	}
	if err := dryrun.PerformDryRun(ctx, tgt.awsCfg, eksClient, tgt.cluster, selected, flags.force, flags.quiet); err != nil {
		color.Red("  %v", err)
	}
	if !flags.quiet {
		printChangelogsForNodegroups(ctx, tgt.awsCfg, eksClient, tgt.cluster, selected, flags.changelog)
	}
}

// printFleetSummary renders the end-of-run aggregate.
func printFleetSummary(results []clusterUpdateResult) {
	color.Cyan("\nFleet summary (%d cluster(s)):", len(results))
	for _, r := range results {
		status := summarizeClusterResult(r)
		fmt.Printf("  %-28s %s\n", r.Cluster+" ("+r.Region+")", status)
	}
}

func summarizeClusterResult(r clusterUpdateResult) string {
	switch {
	case r.HealthBlocked:
		return color.RedString("health-blocked (%s)", r.Error)
	case r.Error != "":
		return color.RedString("failed: %s", r.Error)
	case len(r.Outcomes.Failed) > 0:
		return color.RedString("%d update(s) failed", len(r.Outcomes.Failed))
	case r.VerifyFailed:
		return color.YellowString("updated %d, verification issues", len(r.Outcomes.Started))
	case len(r.Outcomes.Started) > 0:
		return color.GreenString("updated %d, skipped %d, custom %d",
			len(r.Outcomes.Started), len(r.Outcomes.Skipped), len(r.Outcomes.Custom))
	default:
		return color.GreenString("nothing to update (skipped %d, custom %d)",
			len(r.Outcomes.Skipped), len(r.Outcomes.Custom))
	}
}

// fleetExit returns the worst (highest) per-cluster exit code: 5 verification,
// 4 update-failed, 3 health-blocked, else 0.
func fleetExit(results []clusterUpdateResult) error {
	worst := 0
	bump := func(code int) {
		if code > worst {
			worst = code
		}
	}
	for _, r := range results {
		switch {
		case r.HealthBlocked:
			bump(3)
		case r.Error != "" || len(r.Outcomes.Failed) > 0:
			bump(4)
		case r.VerifyFailed:
			bump(5)
		}
	}
	if worst == 0 {
		return nil
	}
	return cli.Exit(fmt.Sprintf("fleet update finished with issues (worst exit code %d)", worst), worst)
}

// promptYesNo asks a yes/no question on the terminal; defaults to no.
func promptYesNo(question string) bool {
	fmt.Printf("%s [y/N]: ", question)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
