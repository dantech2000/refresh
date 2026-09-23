package statuscmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/commands/statusview"
	appconfig "github.com/dantech2000/refresh/internal/config"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
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

	regions := resolveRegions(cmd, awsCfg)
	maxConc := appconfig.ClampMaxConcurrency(cmd.Int("max-concurrency"))
	opts := statussvc.ListOptions{
		NamePattern:    strings.TrimSpace(cmd.Args().First()),
		MaxConcurrency: maxConc,
	}

	start := time.Now()
	var (
		statuses   []statussvc.ClusterStatus
		regionErrs []error
	)
	gather := func() error {
		statuses, regionErrs = gatherFleet(ctx, awsCfg, regions, opts, maxConc)
		// Only a total failure (no data from any region) is fatal.
		if len(statuses) == 0 && len(regionErrs) > 0 {
			return regionErrs[0]
		}
		return nil
	}
	if err := runner.WithSpinner("status", "Fleet status gathered!", gather); err != nil {
		return err
	}
	elapsed := time.Since(start)

	for _, e := range regionErrs {
		fmt.Fprintln(os.Stderr, color.YellowString("warning: %v", e))
	}

	sortStatuses(statuses, cmd.String("sort"), cmd.Bool("desc"))

	if handled, err := runner.EncodeStdout(cmd.String("format"), statussvc.FleetStatus{Clusters: statuses}); handled {
		if err != nil {
			return err
		}
		return exitForStatuses(statuses, len(regionErrs))
	}
	if err := statusview.OutputFleetTable(statuses, elapsed); err != nil {
		return err
	}
	return exitForStatuses(statuses, len(regionErrs))
}

// resolveRegions picks the region set: explicit --region wins, then
// --all-regions (partition sweep / REFRESH_EKS_REGIONS), else the config region.
func resolveRegions(cmd *cli.Command, awsCfg aws.Config) []string {
	if r := cmd.StringSlice("region"); len(r) > 0 {
		return r
	}
	if cmd.Bool("all-regions") {
		if env := appconfig.RegionsFromEnv(); len(env) > 0 {
			return env
		}
		return appconfig.GetRegionsForPartition(awsCfg.Region)
	}
	if awsCfg.Region != "" {
		return []string{awsCfg.Region}
	}
	return appconfig.GetRegionsForPartition(awsCfg.Region)
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

// gatherFleet fans out across regions with bounded concurrency, returning the
// merged cluster statuses and any per-region errors.
func gatherFleet(ctx context.Context, baseCfg aws.Config, regions []string, opts statussvc.ListOptions, maxConc int) ([]statussvc.ClusterStatus, []error) {
	if maxConc <= 0 {
		maxConc = appconfig.DefaultMaxConcurrency
	}
	// Build the shared logger once through the factory so service logs honor the
	// global --log-level/--verbose (quiet by default) instead of leaking at
	// Info level into the TUI. (REF-129)
	logger := factory.NewDefaultLogger(nil)
	var (
		mu   sync.Mutex
		all  []statussvc.ClusterStatus
		errs []error
		wg   sync.WaitGroup
		sem  = make(chan struct{}, maxConc)
	)
	for _, region := range regions {
		wg.Add(1)
		sem <- struct{}{}
		go func(r string) {
			defer wg.Done()
			defer func() { <-sem }()

			cfg := baseCfg.Copy()
			cfg.Region = r
			svc := newRegionService(cfg, logger)
			statuses, err := svc.ListClusterStatuses(ctx, opts)

			mu.Lock()
			defer mu.Unlock()
			// Keep partial rows even on error: a cancelled sweep returns the
			// clusters it reached plus "not evaluated" rows for the rest.
			all = append(all, statuses...)
			if err != nil {
				errs = append(errs, fmt.Errorf("region %s: %w", r, err))
			}
		}(region)
	}
	wg.Wait()
	return all, errs
}

// Exit codes for `refresh status` (documented in the command help).
const (
	exitStale       = 2
	exitSupportRisk = 3
	exitIncomplete  = 4
)

// exitForStatuses maps the fleet posture to the documented exit-code contract:
// 3 when any cluster is on extended/unsupported EKS, else 2 when something is
// stale, else 4 when any cluster row has errors or any region failed, else 0.
// A confirmed finding outranks incomplete data, but incomplete data never
// exits 0.
func exitForStatuses(statuses []statussvc.ClusterStatus, failedRegions int) error {
	supportRisk, stale, incompleteRows := false, false, 0
	for _, c := range statuses {
		if c.SupportRisk() {
			supportRisk = true
		}
		if c.NeedsAttention() {
			stale = true
		}
		if c.Incomplete() {
			incompleteRows++
		}
	}
	switch {
	case supportRisk:
		return cli.Exit("", exitSupportRisk)
	case stale:
		return cli.Exit("", exitStale)
	case incompleteRows > 0 || failedRegions > 0:
		return cli.Exit(fmt.Sprintf("incomplete data: %d cluster(s) with errors, %d region(s) failed",
			incompleteRows, failedRegions), exitIncomplete)
	default:
		return nil
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
			sa, sb := a.StaleAMI.Behind+a.AddonsBehind.Behind, b.StaleAMI.Behind+b.AddonsBehind.Behind
			if sa != sb {
				return sa < sb
			}
			return byName(a, b)
		}
	default: // cluster
		return byName
	}
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
