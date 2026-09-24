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
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/common"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/dryrun"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/monitoring"
	"github.com/dantech2000/refresh/internal/regionsweep"
	"github.com/dantech2000/refresh/internal/ui"
)

// clusterTarget is a cluster to update plus the region-scoped AWS config to
// reach it.
type clusterTarget struct {
	cluster string
	region  string
	awsCfg  aws.Config
}

// clusterStatus is the outcome of one cluster in a fleet run.
// docs/concepts/output.md documents the values; later versions may add
// values.
type clusterStatus string

const (
	// clusterSucceeded: the cluster did what the run asked, with no failure.
	clusterSucceeded clusterStatus = "Succeeded"
	// clusterIncomplete: the run finished in the cluster, but some data could
	// not be read (see the failures). Exit 4.
	clusterIncomplete clusterStatus = "Incomplete"
	// clusterFailed: a nodegroup update could not start or did not succeed,
	// or the nodegroups could not be selected. Exit 4.
	clusterFailed clusterStatus = "Failed"
	// clusterHealthBlocked: the pre-flight health check blocked the cluster.
	// Nothing was rolled. Exit 3.
	clusterHealthBlocked clusterStatus = "HealthBlocked"
	// clusterHealthWarned: the health check warned and --health-only or
	// --require-healthy stopped the cluster there. Exit 2.
	clusterHealthWarned clusterStatus = "HealthWarned"
	// clusterVerifyFailed: the updates succeeded, but post-roll verification
	// found issues. Exit 5.
	clusterVerifyFailed clusterStatus = "VerifyFailed"
	// clusterInterrupted: the user stopped the run while this cluster was in
	// progress. Started EKS updates keep running. Exit 1.
	clusterInterrupted clusterStatus = "Interrupted"
	// clusterTimedOut: the cluster's --wait-timeout passed. Started EKS
	// updates may still be running. Exit 1.
	clusterTimedOut clusterStatus = "TimedOut"
	// clusterNotAttempted: the run stopped before it reached this cluster.
	// Exit 1.
	clusterNotAttempted clusterStatus = "NotAttempted"
	// clusterPlanned: a dry run previewed every selected nodegroup.
	clusterPlanned clusterStatus = "Planned"
)

// EnumValues lists every clusterStatus.
func (clusterStatus) EnumValues() []string {
	return []string{
		string(clusterSucceeded), string(clusterIncomplete), string(clusterFailed),
		string(clusterHealthBlocked), string(clusterHealthWarned), string(clusterVerifyFailed),
		string(clusterInterrupted), string(clusterTimedOut), string(clusterNotAttempted),
		string(clusterPlanned),
	}
}

// clusterUpdateResult is one cluster's outcome within a fleet run.
type clusterUpdateResult struct {
	Cluster    string            `json:"cluster" yaml:"cluster"`
	Region     string            `json:"region" yaml:"region"`
	Status     clusterStatus     `json:"status" yaml:"status"`
	Nodegroups []nodegroupResult `json:"nodegroups" yaml:"nodegroups"`
	// Verification is the post-roll verification, when it ran.
	Verification *PostRollVerification `json:"verification,omitempty" yaml:"verification,omitempty"`
	// Health is the pre-flight verdict whenever a check ran (nil when skipped).
	Health *health.HealthSummary `json:"health,omitempty" yaml:"health,omitempty"`
	// Failure is a failure of the cluster itself: its nodegroups could not
	// be selected, or the run stopped (Interrupted, TimedOut, NotAttempted)
	// before any update started. A nodegroup's failure is on the nodegroup.
	Failure *diag.Failure `json:"failure,omitempty" yaml:"failure,omitempty"`

	// run is what the run did in the cluster, for the failures list.
	run updateRun
}

// failures lists the cluster's failures: its own, each nodegroup's, the
// post-roll read failures, and the health check's.
func (r clusterUpdateResult) failures() []diag.Failure {
	var out []diag.Failure
	if r.Failure != nil {
		out = append(out, *r.Failure)
	}
	out = append(out, r.run.failures()...)
	if r.Health != nil {
		out = append(out, healthFailures(r.run, r.Health)...)
	}
	return out
}

// fleetDryRunResult is one cluster's preview in a -o json/yaml fleet dry-run.
type fleetDryRunResult struct {
	Cluster string `json:"cluster" yaml:"cluster"`
	Region  string `json:"region" yaml:"region"`
	// Status is Planned, Incomplete (the plan has nodegroups that could not
	// be read), or Failed (no plan).
	Status clusterStatus `json:"status" yaml:"status"`
	Plan   *dryRunPlan   `json:"plan,omitempty" yaml:"plan,omitempty"`
	// Failure is set when the cluster has no plan.
	Failure *diag.Failure `json:"failure,omitempty" yaml:"failure,omitempty"`
}

// failures lists the preview's failures: the cluster's own and the plan's.
func (r fleetDryRunResult) failures() []diag.Failure {
	var out []diag.Failure
	if r.Failure != nil {
		out = append(out, *r.Failure)
	}
	if r.Plan != nil {
		out = append(out, r.Plan.Failures...)
	}
	return out
}

// fleetUpdateDocument is the -o json/yaml document of a fleet run.
type fleetUpdateDocument struct {
	Clusters []clusterUpdateResult `json:"clusters" yaml:"clusters"`
	// SkippedRegions are default-sweep regions these credentials can't use.
	// They are a notice, not a failure.
	SkippedRegions []string  `json:"skippedRegions,omitempty" yaml:"skippedRegions,omitempty"`
	Failures       diag.List `json:"failures" yaml:"failures"`
}

// DocumentKind is FleetUpdate.
func (fleetUpdateDocument) DocumentKind() apidoc.Kind { return apidoc.KindFleetUpdate }

// fleetDryRunDocument is the -o json/yaml document of a fleet dry run.
type fleetDryRunDocument struct {
	Clusters       []fleetDryRunResult `json:"clusters" yaml:"clusters"`
	SkippedRegions []string            `json:"skippedRegions,omitempty" yaml:"skippedRegions,omitempty"`
	Failures       diag.List           `json:"failures" yaml:"failures"`
}

// DocumentKind is FleetUpdatePlan.
func (fleetDryRunDocument) DocumentKind() apidoc.Kind { return apidoc.KindFleetUpdatePlan }

// newFleetUpdateDocument builds the fleet document from the per-cluster
// results and discovery. Failures are the regions discovery could not list
// plus every cluster's failures, sorted.
func newFleetUpdateDocument(results []clusterUpdateResult, disc fleetDiscovery) fleetUpdateDocument {
	fs := diag.List(append([]diag.Failure(nil), disc.failed...))
	for _, r := range results {
		fs = append(fs, r.failures()...)
	}
	diag.Sort(fs)
	if results == nil {
		results = []clusterUpdateResult{}
	}
	for i := range results {
		results[i].Nodegroups = apidoc.List(results[i].Nodegroups)
	}
	return fleetUpdateDocument{Clusters: results, SkippedRegions: disc.skipped, Failures: fs}
}

// newFleetDryRunDocument builds the fleet dry-run document.
func newFleetDryRunDocument(results []fleetDryRunResult, disc fleetDiscovery) fleetDryRunDocument {
	fs := diag.List(append([]diag.Failure(nil), disc.failed...))
	for _, r := range results {
		fs = append(fs, r.failures()...)
	}
	diag.Sort(fs)
	if results == nil {
		results = []fleetDryRunResult{}
	}
	return fleetDryRunDocument{Clusters: results, SkippedRegions: disc.skipped, Failures: fs}
}

// listClustersFunc lists the EKS cluster names reachable with cfg (a
// region-scoped config). A seam so discovery is testable without AWS.
type listClustersFunc func(ctx context.Context, cfg aws.Config) ([]string, error)

// fleetStderr receives the discovery notices and failure lines. A seam for
// tests.
var fleetStderr io.Writer = ui.Stderr

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
	if cmd.IsSet("cluster") {
		return fmt.Errorf("--all-clusters cannot be combined with --cluster; drop one of them (scope the fleet with -r)")
	}
	if cmd.String("kube-context") != "" {
		return fmt.Errorf("--all-clusters cannot be combined with --kube-context: fleet mode matches each cluster to a kubeconfig context by endpoint, and one explicit context would be used for every cluster")
	}
	return nil
}

// runFleetUpdate is "patch Tuesday": discover clusters across regions and roll
// matching nodegroups serially (blast-radius control), with one batch
// confirmation, an aggregate summary, and a worst-outcome exit code.
func runFleetUpdate(ctx context.Context, cmd *cli.Command) (err error) {
	if err := validateFleetFlags(cmd); err != nil {
		return err
	}
	flags, err := readUpdateAMIFlags(cmd)
	if err != nil {
		return err
	}
	// No overall deadline: clusters roll serially, so one --wait-timeout across the
	// whole fleet would starve later clusters. --wait-timeout applies per cluster
	// (see updateOneClusterInFleet); the run stays signal-cancellable.
	ctx, cancel, awsCfg, err := runner.SetupAWSWithDeadline(ctx, cmd, 0)
	if err != nil {
		return err
	}
	defer cancel()
	// Each cluster's deadline is --wait-timeout: a timeout names it.
	defer runner.WaitDeadlineHint(&err)

	nodegroupPattern := cmd.String("nodegroup")
	// -o json/yaml: stdout gets one document for the whole fleet run (with
	// --health-only too: the verdicts are collected per cluster).
	machine := flags.machine()

	regions, explicitRegions := resolveUpdateRegions(cmd, awsCfg)
	// Discovery is bounded by --wait-timeout so a stalled region can't hang an
	// unattended run. Only the default region sweep skips regions these
	// credentials can't reach (SCP region restrictions, opt-in regions).
	discoverCtx, cancelDiscover := fleetClusterContext(ctx, flags.timeout)
	disc, err := discoverFleetTargets(discoverCtx, awsCfg, regions, !explicitRegions, listRegionClusters)
	cancelDiscover()
	if err != nil {
		return discoveryStopError(ctx, err, flags.timeout)
	}
	if err := checkDiscovery(len(regions), disc); err != nil {
		if cerr := runner.NoRegionAnswered(ctx, awsCfg, disc.skipped, disc.errs); cerr != nil {
			return cerr
		}
		runner.WriteFailures(flags.format, os.Stdout, fleetStderr, disc.failed)
		return err
	}
	targets := disc.targets
	if len(targets) == 0 {
		return finishEmptyFleet(ctx, disc, len(regions), flags)
	}

	if flags.dryRun {
		return runFleetDryRun(ctx, targets, disc, nodegroupPattern, flags)
	}

	// One confirmation for the whole batch (or --yes); without a TTY or with
	// -o json/yaml, require --yes rather than hang. --health-only changes
	// nothing, so it needs no confirmation.
	if !flags.yes && !flags.healthOnly {
		if !flags.canPrompt() {
			return fmt.Errorf("fleet update would modify %d cluster(s); re-run with --yes (%s)", len(targets), flags.noPromptReason())
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
	notStarted := 0
	for _, tgt := range targets {
		if ctx.Err() != nil {
			results = append(results, notAttemptedCluster(ctx, tgt))
			notStarted++
			continue
		}
		if !flags.quiet && !machine {
			color.Cyan("\n=== %s (%s) ===", tgt.cluster, tgt.region)
		}
		results = append(results, updateOneClusterInFleet(ctx, tgt, nodegroupPattern, cflags))
	}
	if notStarted > 0 {
		flags.notice(color.FgYellow, "Interrupted: %d of %d cluster(s) not started", notStarted, len(targets))
	}

	doc := newFleetUpdateDocument(results, disc)
	if machine {
		if _, err := runner.EncodeStdout(flags.format, doc); err != nil {
			return err
		}
	} else {
		printFleetSummary(results, len(disc.failed))
	}
	runner.WriteFailures(flags.format, os.Stdout, fleetStderr, doc.Failures)
	// After Ctrl+C the run exits 1, whatever the clusters reported.
	if err := runner.UnlessInterrupted(ctx, fleetExit(results, disc.failed)); err != nil || notStarted == 0 {
		return err
	}
	// Interrupted between clusters: nothing was in progress, but the fleet
	// was not fully processed, so the run must not pass.
	return fmt.Errorf("fleet update interrupted: %d of %d cluster(s) not started", notStarted, len(targets))
}

// finishEmptyFleet ends a fleet run that found no clusters. The regions that
// could not be listed may hold clusters, so they make the run incomplete
// (exit 4), not a pass.
func finishEmptyFleet(ctx context.Context, disc fleetDiscovery, regions int, flags updateAMIFlags) error {
	if flags.machine() {
		var doc apidoc.Document = newFleetUpdateDocument(nil, disc)
		if flags.dryRun {
			doc = newFleetDryRunDocument(nil, disc)
		}
		if _, err := runner.EncodeStdout(flags.format, doc); err != nil {
			return err
		}
	} else {
		color.Yellow("No clusters found across %d region(s)", regions-len(disc.failed)-len(disc.skipped))
	}
	runner.WriteFailures(flags.format, os.Stdout, fleetStderr, disc.failed)
	return runner.UnlessInterrupted(ctx, runner.IncompleteExit(disc.failed))
}

// runFleetDryRun previews every cluster: the fleet dry-run document with
// -o json/yaml, else the human preview. A cluster or nodegroup that could
// not be previewed, or a region that could not be listed, is a failure
// (exit 4).
func runFleetDryRun(ctx context.Context, targets []clusterTarget, disc fleetDiscovery, nodegroupPattern string, flags updateAMIFlags) error {
	var fs diag.List
	if flags.machine() {
		plans, err := fleetDryRunResults(ctx, targets, nodegroupPattern, flags)
		if err != nil {
			return err
		}
		doc := newFleetDryRunDocument(plans, disc)
		if _, err := runner.EncodeStdout(flags.format, doc); err != nil {
			return err
		}
		fs = doc.Failures
	} else {
		clusterFailures, err := fleetDryRun(ctx, targets, nodegroupPattern, flags)
		if err != nil {
			return err
		}
		fs = append(diag.List(append([]diag.Failure(nil), disc.failed...)), clusterFailures...)
		diag.Sort(fs)
	}
	runner.WriteFailures(flags.format, os.Stdout, fleetStderr, fs)
	return runner.UnlessInterrupted(ctx, runner.IncompleteExit(fs))
}

// notAttemptedCluster is the result of a cluster the run never reached
// because ctx ended.
func notAttemptedCluster(ctx context.Context, tgt clusterTarget) clusterUpdateResult {
	f := diag.New(diag.KindCluster, tgt.cluster, diag.ReasonNotAttempted, "the run stopped before this cluster: "+context.Cause(ctx).Error())
	f.Region = tgt.region
	return clusterUpdateResult{
		Cluster:    tgt.cluster,
		Region:     tgt.region,
		Status:     clusterNotAttempted,
		Nodegroups: []nodegroupResult{},
		Failure:    &f,
		run:        newUpdateRun(tgt.cluster, tgt.region),
	}
}

// discoveryStopError maps a discovery that ended with ctx done: a user
// interrupt passes through, and the --wait-timeout bound gets a message naming
// it. Both exit 1: discovery gathered nothing (REF-165).
func discoveryStopError(ctx context.Context, err error, timeout time.Duration) error {
	if ctx.Err() != nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("fleet discovery did not finish within --wait-timeout %s: %w", timeout, err)
}

// checkDiscovery prints one notice naming the regions skipped as not
// accessible, and fails with exit 1 when no region could be listed: nothing
// was gathered. The regions that failed are reported by the caller, once,
// with the run's other failures.
func checkDiscovery(regions int, d fleetDiscovery) error {
	runner.ReportSkippedRegions(fleetStderr, d.skipped)
	if regions > 0 && len(d.failed)+len(d.skipped) == regions {
		return fmt.Errorf("fleet discovery failed: could not list clusters in any of %d region(s) (%d not accessible, %d failed); %s",
			regions, len(d.skipped), len(d.failed), runner.RegionScopeHint)
	}
	return nil
}

// updateOneClusterInFleet runs the per-cluster pipeline (health gate → select →
// roll → verify) and captures the outcome instead of exiting, so the fleet loop
// can aggregate.
func updateOneClusterInFleet(parent context.Context, tgt clusterTarget, nodegroupPattern string, flags updateAMIFlags) clusterUpdateResult {
	res := clusterUpdateResult{Cluster: tgt.cluster, Region: tgt.region, Nodegroups: []nodegroupResult{}, run: newUpdateRun(tgt.cluster, tgt.region)}
	eksClient := eks.NewFromConfig(tgt.awsCfg)

	ctx, cancel := fleetClusterContext(parent, flags.timeout)
	defer cancel()

	// stopped records a cluster that ctx ended before any update started.
	stopped := func(what string) clusterUpdateResult {
		reason, status := diag.ReasonTimeout, clusterTimedOut
		if parent.Err() != nil {
			reason, status = diag.ReasonInterrupted, clusterInterrupted
		}
		f := diag.New(diag.KindCluster, tgt.cluster, reason, what+": "+context.Cause(ctx).Error())
		f.Region = tgt.region
		res.Status, res.Failure = status, &f
		return res
	}

	summary, done, err := preflightHealthCheck(ctx, tgt.awsCfg, eksClient, tgt.cluster, nodegroupPattern, flags)
	res.Health = summary
	if err != nil {
		if ctx.Err() != nil {
			return stopped("stopped during the health check")
		}
		// A warn-level stop (exit 2: --health-only or --require-healthy)
		// is not a block (exit 3), so the fleet exit code matches what the
		// same cluster gives on its own.
		if healthExitCode(err) == runner.ExitNeedsAttention {
			res.Status = clusterHealthWarned
		} else {
			res.Status = clusterHealthBlocked
		}
		return res
	}
	if done {
		res.Status = finishedStatus(res)
		return res
	}

	selected, err := selectNodegroupsForUpdate(ctx, eksClient, tgt.cluster, nodegroupPattern, flags)
	if err != nil {
		if ctx.Err() != nil {
			return stopped("stopped while selecting nodegroups")
		}
		f := selectionFailure(tgt.cluster, tgt.region, err)
		res.Status, res.Failure = clusterFailed, &f
		return res
	}

	run, verifyFailed, monErr := executeUpdates(ctx, tgt.awsCfg, eksClient, tgt.cluster, tgt.region, selected, flags)
	res.run = run
	res.Nodegroups = run.nodegroups
	res.Verification = run.verification
	switch {
	case run.rollFailed() || run.startFailed():
		res.Status = clusterFailed
	case errors.Is(monErr, monitoring.ErrCancelled) || parent.Err() != nil:
		res.Status = clusterInterrupted
	case errors.Is(monErr, monitoring.ErrMonitorTimeout) || ctx.Err() != nil:
		res.Status = clusterTimedOut
	case verifyFailed:
		res.Status = clusterVerifyFailed
	default:
		res.Status = finishedStatus(res)
	}
	return res
}

// finishedStatus is the status of a cluster whose run finished: Incomplete
// when it has failures, else Succeeded.
func finishedStatus(r clusterUpdateResult) clusterStatus {
	if len(r.failures()) > 0 {
		return clusterIncomplete
	}
	return clusterSucceeded
}

// healthExitCode returns the exit code a health-gate error carries, or 0 when
// it carries none.
func healthExitCode(err error) int {
	var ec cli.ExitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 0
}

// fleetClusterContext scopes --wait-timeout to a single cluster in a fleet run
// (health gate + roll + verify). timeout <= 0 means no per-cluster limit.
func fleetClusterContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

// resolveUpdateRegions picks the regions to sweep for --all-clusters: the
// local -r/--region wins, then REFRESH_EKS_REGIONS, else the partition's EKS
// regions. --all-clusters is a sweep, so a global --region before the
// subcommand only sets the home region (awsCfg.Region, and so the partition);
// see runner.Regions. explicit reports whether the user chose the regions
// (-r or the env var).
func resolveUpdateRegions(cmd *cli.Command, awsCfg aws.Config) (regions []string, explicit bool) {
	if r := runner.Regions(cmd, true); len(r) > 0 {
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
	// failed are the regions that could not be listed (KindRegion failures);
	// their clusters are unknown. errs holds the error behind each, in the
	// same order: a failure is a one-line summary, and
	// runner.NoRegionAnswered needs the error itself.
	failed []diag.Failure
	errs   []error
	// skipped regions are default-sweep regions these credentials can't
	// reach (see awserr.IsRegionInaccessible). They are not failures.
	skipped []string
}

// discoverFleetTargets lists clusters in each region (regionsweep, bounded
// concurrency) and returns one target per cluster with a region-scoped
// config. A region whose listing fails is kept instead of being dropped: as
// skipped when skipInaccessible is set and the error says the region is
// closed to these credentials, else as failed. If ctx ends before discovery
// finishes, it returns ctx.Err(): the target list would be incomplete, and
// unstarted regions report nothing.
func discoverFleetTargets(ctx context.Context, baseCfg aws.Config, regions []string, skipInaccessible bool, list listClustersFunc) (fleetDiscovery, error) {
	res := regionsweep.Run(ctx, regions, regionsweep.Options{SkipInaccessible: skipInaccessible},
		func(fctx context.Context, region string) ([]clusterTarget, error) {
			cfg := baseCfg.Copy()
			cfg.Region = region
			names, err := list(fctx, cfg)
			if err != nil {
				return nil, err
			}
			ts := make([]clusterTarget, 0, len(names))
			for _, n := range names {
				ts = append(ts, clusterTarget{cluster: n, region: region, awsCfg: cfg})
			}
			return ts, nil
		})
	if err := ctx.Err(); err != nil {
		return fleetDiscovery{}, fmt.Errorf("fleet discovery stopped: %w", err)
	}

	d := fleetDiscovery{failed: res.Failed, errs: res.Errors, skipped: res.Skipped}
	for _, a := range res.Answered {
		d.targets = append(d.targets, a.Value...)
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

// fleetDryRun prints the per-cluster plan without mutating anything and
// returns the failures of the clusters it could not preview fully.
func fleetDryRun(ctx context.Context, targets []clusterTarget, nodegroupPattern string, flags updateAMIFlags) ([]diag.Failure, error) {
	color.Cyan("Fleet dry-run: %d cluster(s)", len(targets))
	var fs []diag.Failure
	for _, tgt := range targets {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		color.Cyan("\n=== %s (%s) ===", tgt.cluster, tgt.region)
		fs = append(fs, fleetDryRunCluster(ctx, tgt, nodegroupPattern, flags)...)
	}
	return fs, nil
}

// fleetDryRunCluster previews one cluster under a per-cluster --wait-timeout
// and returns its failures.
func fleetDryRunCluster(ctx context.Context, tgt clusterTarget, nodegroupPattern string, flags updateAMIFlags) []diag.Failure {
	ctx, cancel := fleetClusterContext(ctx, flags.timeout)
	defer cancel()
	eksClient := eks.NewFromConfig(tgt.awsCfg)
	flags.yes = true // a preview selects every match without asking
	selected, err := selectNodegroupsForUpdate(ctx, eksClient, tgt.cluster, nodegroupPattern, flags)
	if err != nil {
		color.Red("  could not select nodegroups (see INCOMPLETE DATA)")
		return []diag.Failure{selectionFailure(tgt.cluster, tgt.region, err)}
	}
	unreadable, err := dryrun.PerformDryRun(ctx, tgt.awsCfg, eksClient, tgt.cluster, selected, flags.dryRunOptions())
	if err != nil {
		color.Red("  could not preview the cluster (see INCOMPLETE DATA)")
		f := diag.FromError(diag.KindCluster, tgt.cluster, diag.OpDescribeCluster, err)
		f.Region = tgt.region
		return []diag.Failure{f}
	}
	if !flags.quiet {
		printChangelogsForNodegroups(ctx, tgt.awsCfg, eksClient, tgt.cluster, selected, flags.changelog)
	}
	return dryRunFailures(tgt.cluster, tgt.region, unreadable)
}

// fleetDryRunResults previews every cluster without printing, for the
// -o json/yaml fleet dry-run. A cluster whose preview fails carries its
// failure instead of a plan. It stops early only when ctx is done.
func fleetDryRunResults(ctx context.Context, targets []clusterTarget, nodegroupPattern string, flags updateAMIFlags) ([]fleetDryRunResult, error) {
	flags.yes = true // a preview selects every match without asking
	out := make([]fleetDryRunResult, 0, len(targets))
	for _, tgt := range targets {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		out = append(out, fleetDryRunOne(ctx, tgt, nodegroupPattern, flags))
	}
	return out, nil
}

// fleetDryRunOne previews one cluster under its --wait-timeout.
func fleetDryRunOne(ctx context.Context, tgt clusterTarget, nodegroupPattern string, flags updateAMIFlags) fleetDryRunResult {
	res := fleetDryRunResult{Cluster: tgt.cluster, Region: tgt.region}
	cctx, cancel := fleetClusterContext(ctx, flags.timeout)
	defer cancel()
	eksClient := eks.NewFromConfig(tgt.awsCfg)
	selected, err := selectNodegroupsForUpdate(cctx, eksClient, tgt.cluster, nodegroupPattern, flags)
	if err != nil {
		f := selectionFailure(tgt.cluster, tgt.region, err)
		res.Status, res.Failure = clusterFailed, &f
		return res
	}
	plan, err := dryRunDocument(cctx, tgt.awsCfg, eksClient, tgt.cluster, selected, flags)
	if err != nil {
		f := diag.FromError(diag.KindCluster, tgt.cluster, diag.OpDescribeCluster, err)
		f.Region = tgt.region
		res.Status, res.Failure = clusterFailed, &f
		return res
	}
	res.Plan = &plan
	res.Status = clusterPlanned
	if len(plan.Failures) > 0 {
		res.Status = clusterIncomplete
	}
	return res
}

// printFleetSummary renders the end-of-run aggregate. Failures, including
// the regions discovery could not list, are on stderr.
func printFleetSummary(results []clusterUpdateResult, failedRegions int) {
	color.Cyan("\nFleet summary (%d cluster(s)):", len(results))
	for _, r := range results {
		status := summarizeClusterResult(r)
		fmt.Printf("  %-28s %s\n", r.Cluster+" ("+r.Region+")", status)
	}
	if failedRegions > 0 {
		color.Red("%d region(s) could not be listed; their clusters were not checked (see INCOMPLETE DATA)", failedRegions)
	}
}

func summarizeClusterResult(r clusterUpdateResult) string {
	run := updateRun{nodegroups: r.Nodegroups}
	started := len(run.started())
	skipped := run.count(ngSkipped)
	switch r.Status {
	case clusterHealthBlocked:
		return color.RedString("health-blocked (%s)", healthProblemsOf(r.Health))
	case clusterHealthWarned:
		return color.YellowString("health warnings (%s)", healthProblemsOf(r.Health))
	case clusterFailed:
		n := len(r.run.failures())
		if r.Failure != nil {
			n++
		}
		return color.RedString("failed: %d failure(s), see INCOMPLETE DATA", n)
	case clusterInterrupted:
		if started > 0 {
			return color.YellowString("interrupted (update continues in AWS; check with refresh nodegroup list %s)", r.Cluster)
		}
		return color.YellowString("interrupted before any update started")
	case clusterTimedOut:
		return color.YellowString("timed out (--wait-timeout; an update may still be running; check with refresh nodegroup list %s)", r.Cluster)
	case clusterNotAttempted:
		return color.YellowString("not started (the run was interrupted)")
	case clusterVerifyFailed:
		return color.YellowString("updated %d, verification issues", started)
	case clusterIncomplete:
		return color.YellowString("updated %d, skipped %d, some data could not be read", started, skipped)
	default:
		if started > 0 {
			return color.GreenString("updated %d, skipped %d", started, skipped)
		}
		return color.GreenString("nothing to update (skipped %d)", skipped)
	}
}

// healthProblemsOf names the checks behind a health verdict, or says there
// is none.
func healthProblemsOf(summary *health.HealthSummary) string {
	if summary == nil {
		return "no verdict"
	}
	return healthProblems(*summary)
}

// fleetExit returns the worst (highest) exit code across the run:
// 5 verification, 4 a failed or incomplete cluster or a region that
// discovery could not list, 3 health-blocked, 2 health warnings that stopped
// a cluster (--health-only or --require-healthy), 1 interrupted, timed out,
// or not started (as in the single-cluster updateExit), else 0.
func fleetExit(results []clusterUpdateResult, failedRegions []diag.Failure) error {
	worst := 0
	bump := func(code int) {
		if code > worst {
			worst = code
		}
	}
	if len(failedRegions) > 0 {
		bump(runner.ExitIncomplete)
	}
	for _, r := range results {
		switch r.Status {
		case clusterFailed, clusterIncomplete:
			bump(runner.ExitIncomplete)
		case clusterHealthBlocked:
			bump(runner.ExitBlocked)
		case clusterHealthWarned:
			bump(runner.ExitNeedsAttention)
		case clusterInterrupted, clusterTimedOut, clusterNotAttempted:
			bump(runner.ExitError)
		case clusterVerifyFailed:
			bump(runner.ExitVerifyFailed)
		}
	}
	if worst == 0 {
		return nil
	}
	return cli.Exit(fmt.Sprintf("fleet update finished with issues (worst exit code %d)", worst), worst)
}

// promptYesNo asks a yes/no question on stderr; defaults to no.
func promptYesNo(ctx context.Context, question string) bool {
	_, _ = fmt.Fprintf(ui.Stderr, "%s [y/N]: ", question)
	return ui.Confirm(ctx)
}
