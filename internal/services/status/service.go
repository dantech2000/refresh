package status

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/common"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/types"
)

// ClusterAPI is the subset of the EKS API the status service calls directly.
type ClusterAPI interface {
	ListClusters(ctx context.Context, in *eks.ListClustersInput, optFns ...func(*eks.Options)) (*eks.ListClustersOutput, error)
	DescribeCluster(ctx context.Context, in *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	DescribeClusterVersions(ctx context.Context, in *eks.DescribeClusterVersionsInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error)
}

// NodegroupLister provides per-cluster nodegroup summaries (with AMI status),
// plus a failure for each nodegroup that could not be described. Satisfied
// by *nodegroup.ServiceImpl.
type NodegroupLister interface {
	ListDetailed(ctx context.Context, clusterName string, options nodegroup.ListOptions) (nodegroup.ListResult, error)
}

// AddonAnalyzer provides installed addons and their available versions.
// Satisfied by *addons.ServiceImpl.
type AddonAnalyzer interface {
	ListDetailed(ctx context.Context, clusterName string, options addons.ListOptions) (addons.ListResult, error)
	GetAvailableVersions(ctx context.Context, addonName, k8sVersion string) ([]addons.AddonVersionInfo, error)
}

// EC2API is the optional EC2 subset used for AMI age and Karpenter detection.
type EC2API interface {
	DescribeImages(ctx context.Context, in *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error)
	DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

// Service aggregates fleet patch posture for a single region.
type Service struct {
	region     string
	clusterAPI ClusterAPI
	nodegroups NodegroupLister
	addons     AddonAnalyzer
	ec2        EC2API // optional; nil disables AMI-age and Karpenter probes

	// now is injectable for tests; nil means time.Now.
	now func() time.Time

	// support memoizes version support posture for the Service's lifetime;
	// EKS support dates don't move within a run.
	support common.Memo[string, SupportPosture]
	// newLatestAMI builds the latest-AMI cache for one sweep. Nil means each
	// cluster's nodegroup listing uses its own.
	newLatestAMI func() *awsinternal.LatestAMICache
}

// sweep holds the lookups one ListClusterStatuses call shares across its
// clusters. It is built per call, so a reused Service never serves an addon
// version or AMI from an earlier sweep.
type sweep struct {
	addonVersions common.Memo[addonVersionsKey, addonVersions]
	latestAMI     *awsinternal.LatestAMICache // nil: one cache per cluster
	detail        bool                        // ListOptions.Detail
}

func (s *Service) newSweep() *sweep {
	sw := &sweep{}
	if s.newLatestAMI != nil {
		sw.latestAMI = s.newLatestAMI()
	}
	return sw
}

type addonVersionsKey struct {
	addon, k8sVersion string
}

// addonVersions is one GetAvailableVersions answer. None records
// ErrNoVersionsFound, which is an answer rather than a failure, so it is
// memoized too.
type addonVersions struct {
	latest string
	none   bool
}

// ListOptions controls cluster selection for a single-region status sweep.
type ListOptions struct {
	NamePattern    string
	MaxConcurrency int
	// Detail keeps the per-nodegroup and per-add-on rows on each
	// ClusterStatus (Nodegroups, Addons). It costs no extra AWS calls.
	Detail bool
}

// NewService builds a region-scoped status service from an AWS config, wiring
// the concrete cluster/nodegroup/addons/ec2 clients.
func NewService(awsCfg aws.Config, logger *slog.Logger) *Service {
	if logger == nil {
		// Defense-in-depth: callers should pass factory.NewDefaultLogger(nil)
		// (quiet by default, honoring --log-level/--verbose). If a caller still
		// passes nil, discard service logs rather than falling back to
		// slog.Default(), which is Info-level and would leak into the command's
		// terminal output. (REF-129)
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	eksClient := eks.NewFromConfig(awsCfg)
	ngSvc := nodegroup.NewService(awsCfg, nil, logger)
	return &Service{
		region:     awsCfg.Region,
		clusterAPI: eksClient,
		nodegroups: ngSvc,
		addons:     addons.NewService(eksClient, logger),
		ec2:        ec2.NewFromConfig(awsCfg),
		// The sweep calls it; a method value keeps the service's SSM client.
		newLatestAMI: ngSvc.NewLatestAMICache,
	}
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// ListClusterStatuses returns the patch posture of every cluster in the
// service's region (optionally filtered by NamePattern). Per-cluster failures
// are recorded on the row (ClusterStatus.Failures) rather than failing the
// whole sweep.
//
// If ctx is cancelled or times out mid-sweep, clusters the sweep never reached
// come back as rows marked "not evaluated" (never as zero-value rows), and the
// error wraps ctx.Err() so the caller can report the partial sweep.
func (s *Service) ListClusterStatuses(ctx context.Context, opts ListOptions) ([]ClusterStatus, error) {
	names, err := s.listClusterNames(ctx)
	if err != nil {
		return nil, err
	}
	if p := strings.TrimSpace(opts.NamePattern); p != "" {
		filtered := names[:0]
		for _, n := range names {
			if strings.Contains(strings.ToLower(n), strings.ToLower(p)) {
				filtered = append(filtered, n)
			}
		}
		names = filtered
	}

	conc := opts.MaxConcurrency
	if conc <= 0 {
		conc = common.DefaultItemConcurrency
	}
	sw := s.newSweep()
	sw.detail = opts.Detail
	results := common.ForEachParallel(ctx, names, conc,
		func(fctx context.Context, name string) ClusterStatus {
			return s.assembleCluster(fctx, sw, name)
		})
	// ForEachParallel leaves undispatched items zero-valued when ctx is done.
	// Mark them explicitly so they never render as a healthy "unknown" row.
	skipped := 0
	for i := range results {
		if results[i].Name == "" {
			results[i] = s.notEvaluated(ctx, names[i])
			skipped++
		}
	}
	// Report the cancellation only when it actually cost us rows; a deadline
	// that fires after every cluster was evaluated is not a partial sweep.
	if skipped > 0 {
		reason := ctx.Err()
		if reason == nil {
			reason = errors.New("sweep stopped early")
		}
		return results, fmt.Errorf("listing cluster statuses in %s: %d cluster(s) not evaluated: %w", s.region, skipped, reason)
	}
	return results, nil
}

// notEvaluated builds the row for a cluster the sweep never reached.
func (s *Service) notEvaluated(ctx context.Context, name string) ClusterStatus {
	reason := "the sweep stopped early"
	if err := context.Cause(ctx); err != nil {
		reason = err.Error()
	}
	cs := ClusterStatus{
		Name:    name,
		Region:  s.region,
		Support: SupportPosture{Tier: SupportUnknown},
		Compute: ComputeNone,
	}
	f := diag.New(diag.KindCluster, name, diag.ReasonNotAttempted, "not evaluated: "+reason)
	f.Region = s.region
	cs.addFailure(f)
	return cs
}

// errEmptyResponse stands for a describe call that returned no item.
var errEmptyResponse = errors.New("empty response")

// fail records a failure to read part of cluster cs: the cluster itself
// (kind KindCluster, item cs.Name) or one of its nodegroups or add-ons. op
// is the IAM action that failed, or "" to take it from err's
// diag.WithOperation tag.
func (s *Service) fail(cs *ClusterStatus, kind diag.Kind, item, op string, err error) {
	f := diag.FromError(kind, item, op, err)
	if kind != diag.KindCluster {
		f.Cluster = cs.Name
	}
	f.Region = s.region
	cs.addFailure(f)
}

// addFailure records a failure a nodegroup or add-on service built, in the
// service's region when it has none.
func (s *Service) addFailure(cs *ClusterStatus, f diag.Failure) {
	if f.Region == "" {
		f.Region = s.region
	}
	if f.Cluster == "" && f.Kind != diag.KindCluster {
		f.Cluster = cs.Name
	}
	cs.addFailure(f)
}

func (s *Service) listClusterNames(ctx context.Context) ([]string, error) {
	names, err := awsinternal.ListAllPages(ctx, fmt.Sprintf("listing clusters in %s", s.region),
		func(rc context.Context, token *string) (*eks.ListClustersOutput, error) {
			return s.clusterAPI.ListClusters(rc, &eks.ListClustersInput{NextToken: token})
		},
		func(out *eks.ListClustersOutput) ([]string, *string) { return out.Clusters, out.NextToken },
	)
	if err != nil {
		return nil, err
	}
	return names, nil
}

// assembleCluster builds one cluster's status row. Each data source is
// best-effort: a failure is recorded on the row (see fail) and leaves that
// field zero-valued.
func (s *Service) assembleCluster(ctx context.Context, sw *sweep, name string) ClusterStatus {
	cs := ClusterStatus{Name: name, Region: s.region}

	desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.clusterAPI.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(name)})
	})
	if err != nil || desc == nil || desc.Cluster == nil {
		if err == nil {
			err = errEmptyResponse
		}
		s.fail(&cs, diag.KindCluster, name, diag.OpDescribeCluster, err)
		cs.Support = SupportPosture{Tier: SupportUnknown}
		cs.Compute = ComputeNone
		return cs
	}
	cluster := desc.Cluster
	cs.Version = aws.ToString(cluster.Version)
	cs.State = string(cluster.Status)
	cs.Support = ApplySupportType(s.resolveSupport(ctx, cs.Version), SupportTypeOf(cluster))
	if cluster.Health != nil {
		cs.HealthIssues = len(cluster.Health.Issues)
	}

	// Nodegroups (then compute detection, which needs their count) and
	// addons are independent: run them side by side so a cluster costs the
	// slower of the two, not the sum. Each side fills its own fields and
	// failures, merged below in a fixed order.
	var (
		ngSide    = ClusterStatus{Name: name, Region: s.region}
		addonSide struct {
			behind   AddonsBehindSummary
			rows     []AddonPosture
			failures []diag.Failure
			err      error
		}
		wg sync.WaitGroup
	)
	wg.Go(func() {
		addonSide.behind, addonSide.rows, addonSide.failures, addonSide.err = s.addonsBehind(ctx, sw, name, cs.Version)
	})
	s.assembleNodegroups(ctx, sw, &ngSide, name, cluster, cs.Version)
	wg.Wait()

	cs.NodegroupCount = ngSide.NodegroupCount
	cs.StaleAMI = ngSide.StaleAMI
	cs.NodegroupsBehindControlPlane = ngSide.NodegroupsBehindControlPlane
	cs.Compute = ngSide.Compute
	cs.Nodegroups = ngSide.Nodegroups
	for _, f := range ngSide.Failures {
		s.addFailure(&cs, f)
	}

	if addonSide.err != nil {
		s.fail(&cs, diag.KindCluster, name, "", addonSide.err)
	}
	for _, f := range addonSide.failures {
		s.addFailure(&cs, f)
	}
	cs.AddonsBehind = addonSide.behind
	cs.Addons = addonSide.rows

	return cs
}

// assembleNodegroups fills the nodegroup fields and compute type of cs (a
// scratch row owned by the caller's goroutine) for one described cluster.
func (s *Service) assembleNodegroups(ctx context.Context, sw *sweep, cs *ClusterStatus, name string, cluster *ekstypes.Cluster, version string) {
	ngs, ngErr := s.nodegroups.ListDetailed(ctx, name, nodegroup.ListOptions{
		ClusterVersion: version,
		LatestAMI:      sw.latestAMI,
	})
	if ngErr != nil {
		// The nodegroup service tags the error with the call that failed.
		s.fail(cs, diag.KindCluster, name, "", ngErr)
	} else {
		// Failed nodegroups still exist: count them so compute detection
		// isn't fooled, but their AMI posture is unknown, so flag the row.
		cs.NodegroupCount = len(ngs.Summaries) + len(ngs.Failures)
		cs.StaleAMI = s.staleAMISummary(ctx, ngs.Summaries)
		for _, ng := range ngs.Summaries {
			if ng.VersionBehind {
				cs.NodegroupsBehindControlPlane++
			}
			if sw.detail {
				cs.Nodegroups = append(cs.Nodegroups, NodegroupPosture{
					Name: ng.Name, Status: ng.Status, Version: ng.K8sVersion, VersionBehind: ng.VersionBehind,
					CurrentAMI: ng.CurrentAMI, AMIStatus: ng.AMIStatus, DesiredSize: ng.DesiredSize,
				})
			}
			// Advisory in `nodegroup list`, but here an unknown AMI status
			// makes the STALE AMI count incomplete.
			if ng.AMILookupFailure != nil {
				s.addFailure(cs, *ng.AMILookupFailure)
			}
		}
		for _, f := range ngs.Failures {
			s.addFailure(cs, f)
		}
	}
	cs.Compute = s.detectCompute(ctx, name, cluster, cs.NodegroupCount)
}

// staleAMISummary counts outdated nodegroup AMIs and, best-effort, the age of
// the oldest stale AMI.
func (s *Service) staleAMISummary(ctx context.Context, ngs []nodegroup.NodegroupSummary) StaleAMISummary {
	summary := StaleAMISummary{Total: len(ngs)}
	var staleIDs []string
	for _, ng := range ngs {
		if ng.AMIStatus == types.AMIOutdated {
			summary.Behind++
			if ng.CurrentAMI != "" {
				staleIDs = append(staleIDs, ng.CurrentAMI)
			}
		}
	}
	if summary.Behind > 0 && len(staleIDs) > 0 {
		if days := s.amiOldestDays(ctx, staleIDs); days != nil {
			summary.OldestDays = days
		}
	}
	return summary
}

// amiOldestDays resolves the age in days of the oldest AMI among the given IDs
// via DescribeImages. Returns nil when EC2 is unavailable or the call fails.
func (s *Service) amiOldestDays(ctx context.Context, amiIDs []string) *int {
	if s.ec2 == nil {
		return nil
	}
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*ec2.DescribeImagesOutput, error) {
		return s.ec2.DescribeImages(rc, &ec2.DescribeImagesInput{ImageIds: dedupe(amiIDs)})
	})
	if err != nil || out == nil || len(out.Images) == 0 {
		return nil
	}
	var oldest time.Time
	for _, img := range out.Images {
		created := aws.ToString(img.CreationDate)
		if created == "" {
			continue
		}
		t, perr := time.Parse(time.RFC3339, created)
		if perr != nil {
			continue
		}
		if oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
	}
	if oldest.IsZero() {
		return nil
	}
	return daysBetween(oldest, s.clock())
}

// addonsBehind counts cluster addons whose installed version trails the latest
// version compatible with the cluster's Kubernetes version. An addon whose
// installed or latest version can't be read is never counted as behind; it is
// returned as a failure alongside the partial summary. The error is set only
// when the add-ons could not be listed at all. With sw.detail it also
// returns one row per described add-on.
func (s *Service) addonsBehind(ctx context.Context, sw *sweep, cluster, k8sVersion string) (AddonsBehindSummary, []AddonPosture, []diag.Failure, error) {
	res, err := s.addons.ListDetailed(ctx, cluster, addons.ListOptions{})
	if err != nil {
		return AddonsBehindSummary{}, nil, nil, err
	}
	summary := AddonsBehindSummary{Total: len(res.Summaries) + len(res.Failures)}
	failures := res.Failures

	// One version lookup per addon, run in parallel. Each result is shared
	// with every other cluster on the same Kubernetes version.
	type check struct {
		done    bool // false for addons ForEachParallel never dispatched
		behind  bool
		latest  string
		failure *diag.Failure
	}
	checks := common.ForEachParallel(ctx, res.Summaries, common.DefaultItemConcurrency,
		func(fctx context.Context, a addons.AddonSummary) check {
			// Comparing "" would count the addon as behind.
			if a.Version == "" {
				f := diag.New(diag.KindAddon, a.Name, diag.ReasonUnknown, "DescribeAddon returned no installed version")
				f.Operation = diag.OpDescribeAddon
				return check{done: true, failure: &f}
			}
			avail, verr := s.latestAddonVersion(fctx, sw, a.Name, k8sVersion)
			if verr != nil {
				f := diag.FromError(diag.KindAddon, a.Name, diag.OpDescribeAddonVersions, verr)
				return check{done: true, failure: &f}
			}
			if avail.none {
				return check{done: true} // no compatible version published — nothing to compare
			}
			return check{done: true, latest: avail.latest, behind: addons.CompareVersions(a.Version, avail.latest) < 0}
		})
	var rows []AddonPosture
	for i, c := range checks {
		a := res.Summaries[i]
		if sw.detail {
			rows = append(rows, AddonPosture{Name: a.Name, Status: a.Status, Version: a.Version, Latest: c.latest, Behind: c.behind})
		}
		switch {
		case !c.done:
			// The sweep was cancelled before this addon was checked; an
			// unchecked addon must not read as up to date.
			cause := context.Cause(ctx)
			if cause == nil {
				cause = errors.New("sweep stopped early")
			}
			failures = append(failures, diag.FromError(diag.KindAddon, a.Name, diag.OpDescribeAddonVersions, cause))
		case c.failure != nil:
			failures = append(failures, *c.failure)
		case c.behind:
			summary.Behind++
			summary.Names = append(summary.Names, a.Name)
		}
	}
	return summary, rows, failures, nil
}

// latestAddonVersion returns the newest version of addon compatible with
// k8sVersion, memoized for the sweep.
func (s *Service) latestAddonVersion(ctx context.Context, sw *sweep, addon, k8sVersion string) (addonVersions, error) {
	return sw.addonVersions.Get(ctx, addonVersionsKey{addon: addon, k8sVersion: k8sVersion},
		func(ctx context.Context) (addonVersions, error) {
			avail, err := s.addons.GetAvailableVersions(ctx, addon, k8sVersion)
			if errors.Is(err, addons.ErrNoVersionsFound) {
				return addonVersions{none: true}, nil
			}
			if err != nil {
				return addonVersions{}, err
			}
			if len(avail) == 0 {
				return addonVersions{none: true}, nil // defensive: treat like ErrNoVersionsFound
			}
			return addonVersions{latest: avail[0].Version}, nil
		})
}

// karpenterTagKeys are the EC2 instance tags Karpenter sets on the nodes it
// provisions (current and legacy).
var karpenterTagKeys = []string{"karpenter.sh/nodepool", "karpenter.sh/provisioner-name"}

// karpenterClusterFilters returns the alternative EC2 filters that scope an
// instance to one cluster. Karpenter tags its instances with both
// kubernetes.io/cluster/<name>=owned and eks:eks-cluster-name=<name>
// (https://karpenter.sh/docs/concepts/nodeclasses/); older releases set only
// the first. EC2 ANDs separate filters, so each is its own query.
func karpenterClusterFilters(clusterName string) []ec2types.Filter {
	return []ec2types.Filter{
		{Name: aws.String("tag:kubernetes.io/cluster/" + clusterName), Values: []string{"owned"}},
		{Name: aws.String("tag:eks:eks-cluster-name"), Values: []string{clusterName}},
	}
}

// detectCompute classifies how a cluster runs compute so a nodegroup-less
// cluster never renders as an empty "nothing to do" row.
func (s *Service) detectCompute(ctx context.Context, clusterName string, cluster *ekstypes.Cluster, ngCount int) ComputeType {
	if cluster != nil && cluster.ComputeConfig != nil && aws.ToBool(cluster.ComputeConfig.Enabled) {
		return ComputeAutoMode
	}
	if ngCount > 0 {
		return ComputeManaged
	}
	if s.hasKarpenterInstances(ctx, clusterName) {
		return ComputeKarpenter
	}
	return ComputeNone
}

// hasKarpenterInstances is a best-effort probe for live Karpenter-provisioned
// EC2 instances that belong to clusterName. Any error (including missing
// permission) is treated as "no signal".
func (s *Service) hasKarpenterInstances(ctx context.Context, clusterName string) bool {
	if s.ec2 == nil || clusterName == "" {
		return false
	}
	for _, clusterFilter := range karpenterClusterFilters(clusterName) {
		out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*ec2.DescribeInstancesOutput, error) {
			return s.ec2.DescribeInstances(rc, &ec2.DescribeInstancesInput{
				MaxResults: aws.Int32(5),
				Filters: []ec2types.Filter{
					clusterFilter,
					{Name: aws.String("tag-key"), Values: karpenterTagKeys},
					{Name: aws.String("instance-state-name"), Values: []string{"pending", "running"}},
				},
			})
		})
		if err != nil || out == nil {
			continue
		}
		for _, r := range out.Reservations {
			if len(r.Instances) > 0 {
				return true
			}
		}
	}
	return false
}

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
