package statuscmd

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/commands/statusview"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/services/common"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/ui"
)

func runStatus(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsStandard); err != nil {
		return err
	}
	ctx, cancel, awsCfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	regions, defaultSweep := resolveRegions(cmd, awsCfg)
	// --max-concurrency bounds the clusters evaluated at once in each
	// region, and regions at min(regionConcurrency, --max-concurrency).
	opts := statussvc.ListOptions{
		NamePattern:    strings.TrimSpace(cmd.Args().First()),
		MaxConcurrency: appconfig.ClampMaxConcurrency(cmd.Int("max-concurrency")),
	}

	start := time.Now()
	var sweep fleetSweep
	gather := func() error {
		// Only the default all-regions sweep skips regions closed to these
		// credentials; a region the user asked for by name still fails.
		sweep = gatherFleet(ctx, awsCfg, regions, opts, defaultSweep)
		// Only a total failure is fatal (exit 1): no row, and no region
		// answered. The one error gets the full formatted text. When some
		// data came back, the missing regions make it incomplete (exit 4).
		if len(sweep.statuses) == 0 && sweep.answered == 0 && len(sweep.errs) > 0 {
			if cerr := runner.NoRegionAnswered(ctx, awsCfg, sweep.skipped, sweep.regionFailures); cerr != nil {
				return cerr
			}
			return sweep.errs[0]
		}
		return nil
	}
	if err := runner.WithSpinner("status", "Fleet status gathered!", gather); err != nil {
		return err
	}
	elapsed := time.Since(start)
	statuses := sweep.statuses
	if statuses == nil {
		// A complete sweep with no clusters: -o json|yaml print [], not null.
		statuses = []statussvc.ClusterStatus{}
	}

	runner.ReportSkippedRegions(ui.Stderr, sweep.skipped)
	if err := allRegionsSkipped(len(regions), sweep.skipped); err != nil {
		if cerr := runner.NoRegionAnswered(ctx, awsCfg, sweep.skipped, nil); cerr != nil {
			return cerr
		}
		return err
	}

	sortStatuses(statuses, cmd.String("sort"), cmd.Bool("desc"))
	failures := fleetFailures(sweep)
	format := cmd.String("format")

	doc := statussvc.FleetStatus{Clusters: statuses, Failures: failures}
	if handled, err := runner.EncodeStdout(format, doc); handled {
		if err != nil {
			return err
		}
	} else if err := statusview.OutputFleetTable(statuses, failures, elapsed); err != nil {
		return err
	}
	if !runner.TableListsFailures(format) {
		runner.ReportFailures(ui.Stderr, failures)
	}
	return runner.UnlessInterrupted(ctx, exitForStatuses(statuses, failures))
}

// resolveRegions picks the region set: explicit --region wins, then
// --all-regions (partition sweep / REFRESH_EKS_REGIONS), else the config region.
// defaultSweep reports a partition sweep nobody scoped: not -r, not
// REFRESH_EKS_REGIONS, not the config region. Only that sweep skips regions
// closed to these credentials (the fleet discovery rule from #331).
func resolveRegions(cmd *cli.Command, awsCfg aws.Config) (regions []string, defaultSweep bool) {
	// runner.Regions also honors the global `refresh --region X status`
	// (not with -A, where it only picks the home region/partition).
	if r := runner.Regions(cmd, cmd.Bool("all-regions")); len(r) > 0 {
		return r, false
	}
	if cmd.Bool("all-regions") {
		if env := appconfig.RegionsFromEnv(); len(env) > 0 {
			return env, false
		}
		return appconfig.GetRegionsForPartition(awsCfg.Region), true
	}
	if awsCfg.Region != "" {
		return []string{awsCfg.Region}, false
	}
	return appconfig.GetRegionsForPartition(awsCfg.Region), true
}

// regionError is a failed region sweep. It unwraps to the service error, so
// callers can still classify it.
type regionError struct {
	Region string
	Err    error
}

func (e *regionError) Error() string { return fmt.Sprintf("region %s: %v", e.Region, e.Err) }
func (e *regionError) Unwrap() error { return e.Err }

// fleetSweep is the outcome of gatherFleet.
type fleetSweep struct {
	statuses []statussvc.ClusterStatus
	// errs holds one *regionError per region that could not list its
	// clusters.
	errs []error
	// regionFailures has one failure per entry of errs.
	regionFailures []diag.Failure
	// skipped regions were closed to these credentials in a default sweep.
	// They are not failures.
	skipped []string
	// answered counts the regions that listed their clusters.
	answered int
}

// allRegionsSkipped fails (exit 1) when every region of the sweep was skipped
// as not accessible: nothing could be gathered, so an empty table is never a
// false pass.
func allRegionsSkipped(regions int, skipped []string) error {
	if regions == 0 || len(skipped) != regions {
		return nil
	}
	return fmt.Errorf("could not list clusters in any of %d region(s): none is accessible to these credentials; %s",
		regions, runner.RegionScopeHint)
}

// fleetFailures is the sweep's one failure list: the failed regions, then
// the failures of each cluster row, in diag.Sort order.
func fleetFailures(s fleetSweep) diag.List {
	out := slices.Clone(s.regionFailures)
	for _, c := range s.statuses {
		out = append(out, c.Failures...)
	}
	diag.Sort(out)
	return out
}

// regionLister is the per-region status sweep gatherFleet fans out over.
type regionLister interface {
	ListClusterStatuses(ctx context.Context, opts statussvc.ListOptions) ([]statussvc.ClusterStatus, error)
}

// newRegionService builds the per-region status service; tests swap it for a
// fake so the multi-region path runs without AWS.
var newRegionService = func(cfg aws.Config, logger *slog.Logger) regionLister {
	return statussvc.NewService(cfg, logger)
}

// regionConcurrency caps how many regions gatherFleet sweeps at once. Each
// region already fans out over its clusters (opts.MaxConcurrency, from
// --max-concurrency), so applying --max-concurrency alone to regions too
// would multiply: 64 regions x 64 clusters x several describes each.
const regionConcurrency = 4

// regionFanout is how many regions gatherFleet sweeps at once:
// regionConcurrency, lowered to --max-concurrency when that is smaller, so
// -C 1 (set to avoid throttling) means one region at a time.
func regionFanout(maxConcurrency int) int {
	if maxConcurrency > 0 && maxConcurrency < regionConcurrency {
		return maxConcurrency
	}
	return regionConcurrency
}

// regionSweep is one region's result inside gatherFleet.
type regionSweep struct {
	ran      bool
	statuses []statussvc.ClusterStatus
	err      error
}

// gatherFleet sweeps regions, at most regionFanout(opts.MaxConcurrency) at a
// time, and merges
// the cluster statuses (in region order) and per-region errors. If ctx ends
// before a region starts, that region is reported as failed with ctx's error,
// so an interrupted sweep never looks complete.
func gatherFleet(ctx context.Context, baseCfg aws.Config, regions []string, opts statussvc.ListOptions, skipInaccessible bool) fleetSweep {
	// Build the shared logger once through the factory so service logs honor the
	// global --log-level/--verbose (quiet by default) instead of leaking at
	// Info level into the TUI. (REF-129)
	logger := factory.NewDefaultLogger(nil)
	results := common.ForEachParallel(ctx, regions, regionFanout(opts.MaxConcurrency), func(rctx context.Context, r string) regionSweep {
		cfg := baseCfg.Copy()
		cfg.Region = r
		statuses, err := newRegionService(cfg, logger).ListClusterStatuses(rctx, opts)
		return regionSweep{ran: true, statuses: statuses, err: err}
	})

	var sweep fleetSweep
	for i, r := range regions {
		res := results[i]
		if !res.ran {
			res.err = fmt.Errorf("not queried: %w", context.Cause(ctx))
		}
		// Keep partial rows even on error: a cancelled sweep returns the
		// clusters it reached plus "not evaluated" rows for the rest.
		sweep.statuses = append(sweep.statuses, res.statuses...)
		switch {
		case res.err == nil:
			sweep.answered++
		case len(res.statuses) > 0:
			// The region listed its clusters, but the sweep stopped before
			// it evaluated them all. The "not evaluated" rows carry those
			// failures; the region itself answered.
			sweep.answered++
		case skipInaccessible && awserr.IsRegionInaccessible(res.err):
			sweep.skipped = append(sweep.skipped, r)
		default:
			sweep.errs = append(sweep.errs, &regionError{Region: r, Err: res.err})
			f := diag.FromError(diag.KindRegion, r, diag.OpListClusters, res.err)
			if !res.ran {
				f = diag.New(diag.KindRegion, r, diag.ReasonNotAttempted, res.err.Error())
				f.Region = r
			}
			sweep.regionFailures = append(sweep.regionFailures, f)
		}
	}
	sort.Strings(sweep.skipped)
	return sweep
}

// Exit codes for `refresh status`, from the shared contract (runner).
const (
	exitStale       = runner.ExitNeedsAttention
	exitSupportRisk = runner.ExitBlocked
)

// exitForStatuses maps the fleet posture to the documented exit-code contract:
// 3 when any cluster is on extended/unsupported EKS, else 2 when something is
// stale, else 4 when there are failures (a region that could not be listed,
// or part of a cluster row that could not be read), else 0. A confirmed
// finding outranks incomplete data, but incomplete data never exits 0.
func exitForStatuses(statuses []statussvc.ClusterStatus, failures []diag.Failure) error {
	supportRisk, stale := false, false
	for _, c := range statuses {
		if c.SupportRisk() {
			supportRisk = true
		}
		if c.NeedsAttention() {
			stale = true
		}
	}
	switch {
	case supportRisk:
		return cli.Exit("", exitSupportRisk)
	case stale:
		return cli.Exit("", exitStale)
	default:
		return runner.IncompleteExit(failures)
	}
}

func sortStatuses(statuses []statussvc.ClusterStatus, key string, desc bool) {
	less := lessFunc(strings.ToLower(strings.TrimSpace(key)))
	sort.SliceStable(statuses, func(i, j int) bool {
		if desc {
			return less(statuses[j], statuses[i])
		}
		return less(statuses[i], statuses[j])
	})
}

func lessFunc(key string) func(a, b statussvc.ClusterStatus) bool {
	byName := func(a, b statussvc.ClusterStatus) bool {
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Region < b.Region
	}
	switch key {
	case "region":
		return func(a, b statussvc.ClusterStatus) bool {
			if a.Region != b.Region {
				return a.Region < b.Region
			}
			return byName(a, b)
		}
	case "version":
		return func(a, b statussvc.ClusterStatus) bool {
			if a.Version != b.Version {
				return a.Version < b.Version
			}
			return byName(a, b)
		}
	case "support":
		return func(a, b statussvc.ClusterStatus) bool {
			ra, rb := supportSeverity(a.Support.Tier), supportSeverity(b.Support.Tier)
			if ra != rb {
				return ra < rb
			}
			return byName(a, b)
		}
	case "stale":
		return func(a, b statussvc.ClusterStatus) bool {
			sa, sb := staleScore(a), staleScore(b)
			if sa != sb {
				return sa < sb
			}
			return byName(a, b)
		}
	default: // cluster
		return byName
	}
}

// staleScore is the "stale" sort key: stale AMIs, nodegroups behind the
// control plane, and addons behind latest.
func staleScore(c statussvc.ClusterStatus) int {
	return c.StaleAMI.Behind + c.NodegroupsBehindControlPlane + c.AddonsBehind.Behind
}

// supportSeverity orders tiers from healthiest to most urgent so descending
// sort surfaces the clusters that need attention first.
func supportSeverity(t statussvc.SupportTier) int {
	switch t {
	case statussvc.SupportStandard:
		return 0
	case statussvc.SupportUnknown:
		return 1
	case statussvc.SupportExtended:
		return 2
	case statussvc.SupportUnsupported:
		return 3
	default:
		return 1
	}
}
