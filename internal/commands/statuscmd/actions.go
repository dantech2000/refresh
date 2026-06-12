package statuscmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

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
	maxConc := cmd.Int("max-concurrency")
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
		return exitForStatuses(statuses)
	}
	if err := statusview.OutputFleetTable(statuses, elapsed); err != nil {
		return err
	}
	return exitForStatuses(statuses)
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

// gatherFleet fans out across regions with bounded concurrency, returning the
// merged cluster statuses and any per-region errors.
func gatherFleet(ctx context.Context, baseCfg aws.Config, regions []string, opts statussvc.ListOptions, maxConc int) ([]statussvc.ClusterStatus, []error) {
	if maxConc <= 0 {
		maxConc = appconfig.DefaultMaxConcurrency
	}
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
			svc := statussvc.NewService(cfg, nil)
			statuses, err := svc.ListClusterStatuses(ctx, opts)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("region %s: %w", r, err))
				return
			}
			all = append(all, statuses...)
		}(region)
	}
	wg.Wait()
	return all, errs
}

// exitForStatuses maps the fleet posture to the documented exit-code contract:
// 3 when any cluster is on extended/unsupported EKS, 2 when something is stale,
// 0 otherwise.
func exitForStatuses(statuses []statussvc.ClusterStatus) error {
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
		return cli.Exit("", 3)
	case stale:
		return cli.Exit("", 2)
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
