package nodegroup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/commands/runner"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/dryrun"
	"github.com/dantech2000/refresh/internal/monitoring"
	"github.com/dantech2000/refresh/internal/services/common"
	"github.com/dantech2000/refresh/internal/ui"
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
	// Interrupted means the user stopped the run (Ctrl+C / SIGTERM) while this
	// cluster was in progress; any started EKS update keeps running in AWS.
	Interrupted bool `json:"interrupted,omitempty" yaml:"interrupted,omitempty"`
	// TimedOut means monitoring hit the per-cluster --timeout before the
	// updates were terminal; they may still be running in AWS.
	TimedOut bool   `json:"timedOut,omitempty" yaml:"timedOut,omitempty"`
	Error    string `json:"error,omitempty" yaml:"error,omitempty"`
}

// regionDiscoveryError is a region whose cluster listing failed during fleet
// discovery. Its clusters are missing from the run, so the run can't pass.
type regionDiscoveryError struct {
	Region string `json:"region" yaml:"region"`
	Error  string `json:"error" yaml:"error"`
}

// listClustersFunc lists the EKS cluster names reachable with cfg (a
// region-scoped config). A seam so discovery is testable without AWS.
type listClustersFunc func(ctx context.Context, cfg aws.Config) ([]string, error)

// fleetStderr receives per-region discovery warnings. A seam for tests.
var fleetStderr io.Writer = os.Stderr

// validateFleetFlags rejects flag combinations that fleet mode can't honour.
// It runs before any AWS call.
//
//   - A positional arg or --cluster names one cluster, but fleet mode targets
//     every discovered cluster (and reads the nodegroup only from -n). Silently
//     ignoring them would roll far more than asked.
//   - --kube-context is trusted without an endpoint match, so every cluster's
//     PDB gate, metrics, live panel and verification would read that one
//     context. Fleet mode relies on matching each cluster by endpoint.
//
// EKS_CLUSTER_NAME in the environment is not an error: only a --cluster given
// on the command line counts.
func validateFleetFlags(cmd *cli.Command) error {
	if args := cmd.Args().Slice(); len(args) > 0 {
		return fmt.Errorf("--all-clusters does not take positional arguments (got %q); select nodegroups with -n/--nodegroup", strings.Join(args, " "))
	}
	if flagSetOnCommandLine(cmd, "cluster") {
		return fmt.Errorf("--all-clusters cannot be combined with --cluster; drop one of them (scope the fleet with -r)")
	}
	if cmd.String("kube-context") != "" {
		return fmt.Errorf("--all-clusters cannot be combined with --kube-context: fleet mode matches each cluster to a kubeconfig context by endpoint, and one explicit context would be used for every cluster")
	}
	return nil
}

// flagSetOnCommandLine reports whether the string flag name was set on the
// command line rather than only through its env var source. urfave/cli applies
// an env source only when the flag was not given, so a value that differs from
// the env value came from the command line. A command-line value equal to the
// env value is indistinguishable and treated as coming from the env.
func flagSetOnCommandLine(cmd *cli.Command, name string) bool {
	if !cmd.IsSet(name) {
		return false
	}
	for _, f := range cmd.Flags {
		sf, ok := f.(*cli.StringFlag)
		if !ok || !slices.Contains(sf.Names(), name) {
			continue
		}
		if v, _, found := sf.Sources.LookupWithSource(); found && v == cmd.String(name) {
			return false
		}
	}
	return true
}

// runFleetUpdate is "patch Tuesday": discover clusters across regions and roll
// matching nodegroups serially (blast-radius control), with one batch
// confirmation, an aggregate summary, and a worst-outcome exit code.
func runFleetUpdate(ctx context.Context, cmd *cli.Command) error {
	if err := validateFleetFlags(cmd); err != nil {
		return err
	}
	flags, err := readUpdateAMIFlags(cmd)
	if err != nil {
		return err
	}
	// No overall deadline: clusters roll serially, so one --timeout across the
	// whole fleet would starve later clusters. --timeout applies per cluster
	// (see updateOneClusterInFleet); the run stays signal-cancellable.
	ctx, cancel, awsCfg, err := runner.SetupAWSWithDeadline(ctx, cmd, 0)
	if err != nil {
		return err
	}
	defer cancel()

	nodegroupPattern := cmd.String("nodegroup")
	jsonOut := flags.format == "json" && !flags.healthOnly

	regions, explicitRegions := resolveUpdateRegions(cmd, awsCfg)
	// Discovery is bounded by --timeout so a stalled region can't hang an
	// unattended run. Only the default region sweep skips regions these
	// credentials can't reach (SCP region restrictions, opt-in regions).
	discoverCtx, cancelDiscover := fleetClusterContext(ctx, flags.timeout)
	disc, err := discoverFleetTargets(discoverCtx, awsCfg, regions, !explicitRegions, listRegionClusters)
	cancelDiscover()
	if err != nil {
		return discoveryStopError(ctx, err, flags.timeout)
	}
	if err := checkDiscovery(len(regions), disc); err != nil {
		return err
	}
	targets, regionErrs := disc.targets, disc.failed
	if len(targets) == 0 {
		color.Yellow("No clusters found across %d region(s)", len(regions))
		return nil
	}

	if flags.dryRun {
		if err := fleetDryRun(ctx, targets, nodegroupPattern, flags); err != nil {
			return err
		}
		return discoveryExit(regionErrs)
	}

	// One confirmation for the whole batch (or --yes); without a TTY, require
	// --yes rather than hang.
	if !flags.yes {
		if !isInteractive() {
			return fmt.Errorf("fleet update would modify %d cluster(s); re-run with --yes (no interactive terminal for confirmation)", len(targets))
		}
		if !promptYesNo(ctx, fmt.Sprintf("Update matching nodegroups across %d cluster(s) in %d region(s)?", len(targets), len(regions))) {
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
		payload := map[string]any{"clusters": results}
		if len(regionErrs) > 0 {
			payload["discoveryErrors"] = regionErrs
		}
		if len(disc.skipped) > 0 {
			payload["skippedRegions"] = disc.skipped
		}
		if _, err := runner.EncodeStdout("json", payload); err != nil {
			return err
		}
	} else {
		printFleetSummary(results, regionErrs)
	}
	if err := fleetExit(results, regionErrs); err != nil || len(results) == len(targets) {
		return err
	}
	// Interrupted between clusters: nothing was in progress, but the fleet
	// was not fully processed, so the run must not pass.
	return fmt.Errorf("fleet update interrupted: %d of %d cluster(s) not started", len(targets)-len(results), len(targets))
}

// discoveryStopError maps a discovery that ended with ctx done: a user
// interrupt passes through (exit 1), and the --timeout bound becomes exit 4.
func discoveryStopError(ctx context.Context, err error, timeout time.Duration) error {
	if ctx.Err() != nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return cli.Exit(fmt.Sprintf("fleet discovery did not finish within --timeout %s", timeout), 4)
}

// regionScopeHint tells the user how to narrow the region sweep.
const regionScopeHint = "scope with -r or REFRESH_EKS_REGIONS"

// checkDiscovery reports discovery problems on stderr: one line naming the
// regions skipped as not accessible, and one warning per region that failed.
// It fails (exit 4) when no region could be listed, or when the reachable
// regions had no clusters but some regions failed. Nothing is known about the
// failed regions, so "no clusters found" would be a false pass. Skipped
// regions alone never fail a run that reached at least one region.
func checkDiscovery(regions int, d fleetDiscovery) error {
	if len(d.skipped) > 0 {
		_, _ = fmt.Fprintln(fleetStderr, color.YellowString("Skipped %d region(s) not accessible to these credentials: %s (%s)",
			len(d.skipped), strings.Join(d.skipped, ", "), regionScopeHint))
	}
	for _, re := range d.failed {
		_, _ = fmt.Fprintln(fleetStderr, color.YellowString("Warning: skipping region %s: %s", re.Region, re.Error))
	}
	if regions > 0 && len(d.failed)+len(d.skipped) == regions {
		return cli.Exit(fmt.Sprintf("fleet discovery failed: could not list clusters in any of %d region(s) (%d not accessible, %d failed); %s",
			regions, len(d.skipped), len(d.failed), regionScopeHint), 4)
	}
	if len(d.failed) > 0 && len(d.targets) == 0 {
		return cli.Exit(fmt.Sprintf("no clusters found in %d reachable region(s); %d region(s) could not be listed; %s",
			regions-len(d.failed)-len(d.skipped), len(d.failed), regionScopeHint), 4)
	}
	return nil
}

// discoveryExit fails a finished run whose discovery missed regions.
func discoveryExit(regionErrs []regionDiscoveryError) error {
	if len(regionErrs) == 0 {
		return nil
	}
	return cli.Exit(fmt.Sprintf("fleet discovery could not list clusters in %d region(s); %s", len(regionErrs), regionScopeHint), 4)
}

// updateOneClusterInFleet runs the per-cluster pipeline (health gate → select →
// roll → verify) and captures the outcome instead of exiting, so the fleet loop
// can aggregate.
func updateOneClusterInFleet(parent context.Context, tgt clusterTarget, nodegroupPattern string, flags updateAMIFlags) clusterUpdateResult {
	res := clusterUpdateResult{Cluster: tgt.cluster, Region: tgt.region}
	eksClient := eks.NewFromConfig(tgt.awsCfg)

	ctx, cancel := fleetClusterContext(parent, flags.timeout)
	defer cancel()

	done, err := preflightHealthCheck(ctx, tgt.awsCfg, eksClient, tgt.cluster, nodegroupPattern, flags)
	if err != nil {
		if parent.Err() != nil {
			res.Interrupted = true
			return res
		}
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
		if parent.Err() != nil {
			res.Interrupted = true
			return res
		}
		res.Error = err.Error()
		return res
	}

	outcomes, verifyFailed, monErr := executeUpdates(ctx, tgt.awsCfg, eksClient, tgt.cluster, selected, flags)
	res.Outcomes = outcomes
	res.VerifyFailed = verifyFailed
	recordMonitorError(&res, monErr)
	return res
}

// recordMonitorError stores a monitoring error as typed state. An interrupt
// or a monitor timeout means the EKS update may still be running, not that it
// failed, so neither is recorded as an Error.
func recordMonitorError(res *clusterUpdateResult, monErr error) {
	switch {
	case monErr == nil:
	case errors.Is(monErr, monitoring.ErrCancelled):
		res.Interrupted = true
	case errors.Is(monErr, monitoring.ErrMonitorTimeout):
		res.TimedOut = true
	default:
		res.Error = monErr.Error()
	}
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
// explicit reports whether the user chose the regions (-r or the env var).
func resolveUpdateRegions(cmd *cli.Command, awsCfg aws.Config) (regions []string, explicit bool) {
	if r := cmd.StringSlice("region"); len(r) > 0 {
		return r, true
	}
	if env := appconfig.RegionsFromEnv(); len(env) > 0 {
		return env, true
	}
	return appconfig.GetRegionsForPartition(awsCfg.Region), false
}

// fleetDiscovery is the outcome of the region sweep.
type fleetDiscovery struct {
	targets []clusterTarget
	// failed regions could not be listed; their clusters are unknown.
	failed []regionDiscoveryError
	// skipped regions are default-sweep regions these credentials can't
	// reach (see awserr.IsRegionInaccessible). They are not failures.
	skipped []string
}

// discoverFleetTargets lists clusters in each region (bounded concurrency) and
// returns one target per cluster with a region-scoped config. A region whose
// listing fails is kept (in region order) instead of being dropped: as
// skipped when skipInaccessible is set and the error says the region is
// closed to these credentials, else as failed. If ctx ends before discovery
// finishes, it returns ctx.Err(): the target list would be incomplete, and
// unstarted regions report nothing.
func discoverFleetTargets(ctx context.Context, baseCfg aws.Config, regions []string, skipInaccessible bool, list listClustersFunc) (fleetDiscovery, error) {
	type regionResult struct {
		targets []clusterTarget
		err     error
	}
	perRegion := common.ForEachParallel(ctx, regions, common.DefaultItemConcurrency,
		func(fctx context.Context, region string) regionResult {
			cfg := baseCfg.Copy()
			cfg.Region = region
			names, err := list(fctx, cfg)
			if err != nil {
				return regionResult{err: err}
			}
			ts := make([]clusterTarget, 0, len(names))
			for _, n := range names {
				ts = append(ts, clusterTarget{cluster: n, region: region, awsCfg: cfg})
			}
			return regionResult{targets: ts}
		})
	if err := ctx.Err(); err != nil {
		return fleetDiscovery{}, fmt.Errorf("fleet discovery stopped: %w", err)
	}

	var d fleetDiscovery
	for i, r := range perRegion {
		switch {
		case r.err == nil:
			d.targets = append(d.targets, r.targets...)
		case skipInaccessible && awserr.IsRegionInaccessible(r.err):
			d.skipped = append(d.skipped, regions[i])
		default:
			msg := awsinternal.FormatAWSError(r.err, "listing clusters in "+regions[i]).Error()
			d.failed = append(d.failed, regionDiscoveryError{Region: regions[i], Error: msg})
		}
	}
	return d, nil
}

// listRegionClusters lists every EKS cluster in cfg's region. It returns the
// raw SDK error so discovery can classify it by API error code before
// formatting it.
func listRegionClusters(ctx context.Context, cfg aws.Config) ([]string, error) {
	eksClient := eks.NewFromConfig(cfg)
	return common.Paginate(ctx, func(rc context.Context, token *string) ([]string, *string, error) {
		out, err := common.WithRetry(rc, common.DefaultRetryConfig, func(rrc context.Context) (*eks.ListClustersOutput, error) {
			return eksClient.ListClusters(rrc, &eks.ListClustersInput{NextToken: token})
		})
		if err != nil {
			return nil, nil, err
		}
		return out.Clusters, out.NextToken, nil
	})
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

// printFleetSummary renders the end-of-run aggregate, including regions that
// discovery could not list.
func printFleetSummary(results []clusterUpdateResult, regionErrs []regionDiscoveryError) {
	color.Cyan("\nFleet summary (%d cluster(s)):", len(results))
	for _, r := range results {
		status := summarizeClusterResult(r)
		fmt.Printf("  %-28s %s\n", r.Cluster+" ("+r.Region+")", status)
	}
	if len(regionErrs) > 0 {
		color.Red("Regions not listed (%d); their clusters were not checked:", len(regionErrs))
		for _, re := range regionErrs {
			fmt.Printf("  %-28s %s\n", re.Region, color.RedString("discovery failed: %s", re.Error))
		}
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
	case r.Interrupted && len(r.Outcomes.Started) > 0:
		return color.YellowString("interrupted (update continues in AWS; check with refresh nodegroup list %s)", r.Cluster)
	case r.Interrupted:
		return color.YellowString("interrupted before any update started")
	case r.TimedOut:
		return color.YellowString("monitoring timed out (update may still be running; check with refresh nodegroup list %s)", r.Cluster)
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

// fleetExit returns the worst (highest) exit code across the run:
// 5 verification, 4 update-failed or a region that discovery could not list,
// 3 health-blocked, 1 interrupted or monitor timeout (as in the single-cluster
// updateExit), else 0.
func fleetExit(results []clusterUpdateResult, regionErrs []regionDiscoveryError) error {
	worst := 0
	bump := func(code int) {
		if code > worst {
			worst = code
		}
	}
	if len(regionErrs) > 0 {
		bump(4)
	}
	for _, r := range results {
		switch {
		case r.HealthBlocked:
			bump(3)
		case r.Error != "" || len(r.Outcomes.Failed) > 0:
			bump(4)
		case r.Interrupted || r.TimedOut:
			bump(1)
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
func promptYesNo(ctx context.Context, question string) bool {
	fmt.Printf("%s [y/N]: ", question)
	return ui.Confirm(ctx)
}
