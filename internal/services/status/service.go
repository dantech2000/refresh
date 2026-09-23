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
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/services/common"
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
// plus a "name: reason" entry for each nodegroup that could not be described.
// Satisfied by *nodegroup.ServiceImpl.
type NodegroupLister interface {
	ListWithFailures(ctx context.Context, clusterName string, options nodegroup.ListOptions) ([]nodegroup.NodegroupSummary, []string, error)
}

// AddonAnalyzer provides installed addons and their available versions.
// Satisfied by *addons.ServiceImpl.
type AddonAnalyzer interface {
	List(ctx context.Context, clusterName string, options addons.ListOptions) ([]addons.AddonSummary, error)
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
	logger     *slog.Logger

	// now is injectable for tests; nil means time.Now.
	now func() time.Time

	supportMu    sync.Mutex
	supportCache map[string]SupportPosture
}

// ListOptions controls cluster selection for a single-region status sweep.
type ListOptions struct {
	NamePattern    string
	MaxConcurrency int
}

// NewService builds a region-scoped status service from an AWS config, wiring
// the concrete cluster/nodegroup/addons/ec2 clients.
func NewService(awsCfg aws.Config, logger *slog.Logger) *Service {
	if logger == nil {
		// Defense-in-depth: callers should pass factory.NewDefaultLogger(nil)
		// (quiet by default, honoring --log-level/--verbose). If a caller still
		// passes nil, discard service logs rather than falling back to
		// slog.Default(), which is Info-level and would leak into the TUI. (REF-129)
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	eksClient := eks.NewFromConfig(awsCfg)
	return &Service{
		region:     awsCfg.Region,
		clusterAPI: eksClient,
		nodegroups: nodegroup.NewService(awsCfg, nil, logger),
		addons:     addons.NewService(eksClient, logger),
		ec2:        ec2.NewFromConfig(awsCfg),
		logger:     logger,
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
// are recorded on the row rather than failing the whole sweep.
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
	results := common.ForEachParallel(ctx, names, conc,
		func(fctx context.Context, name string) ClusterStatus {
			return s.assembleCluster(fctx, name)
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
	reason := "sweep stopped early"
	if err := ctx.Err(); err != nil {
		reason = err.Error()
	}
	return ClusterStatus{
		Name:    name,
		Region:  s.region,
		Support: SupportPosture{Tier: SupportUnknown},
		Compute: ComputeNone,
		Errors:  []string{"not evaluated: " + reason},
	}
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
// best-effort: a failure appends to Errors and leaves that field zero-valued.
func (s *Service) assembleCluster(ctx context.Context, name string) ClusterStatus {
	cs := ClusterStatus{Name: name, Region: s.region}

	desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.clusterAPI.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(name)})
	})
	if err != nil || desc == nil || desc.Cluster == nil {
		cs.Errors = append(cs.Errors, fmt.Sprintf("describe cluster: %v", err))
		cs.Support = SupportPosture{Tier: SupportUnknown}
		cs.Compute = ComputeNone
		return cs
	}
	cluster := desc.Cluster
	cs.Version = aws.ToString(cluster.Version)
	cs.Support = ApplySupportType(s.resolveSupport(ctx, cs.Version), SupportTypeOf(cluster))
	if cluster.Health != nil {
		cs.HealthIssues = len(cluster.Health.Issues)
	}

	ngs, ngFailures, ngErr := s.nodegroups.ListWithFailures(ctx, name, nodegroup.ListOptions{})
	if ngErr != nil {
		cs.Errors = append(cs.Errors, fmt.Sprintf("list nodegroups: %v", ngErr))
	} else {
		// Failed nodegroups still exist: count them so compute detection
		// isn't fooled, but their AMI posture is unknown, so flag the row.
		// A nodegroup whose latest-AMI lookup failed is both summarized and
		// reported as a failure; count it once.
		cs.NodegroupCount = len(ngs) + len(ngFailures)
		cs.StaleAMI = s.staleAMISummary(ctx, ngs)
		for _, ng := range ngs {
			if ng.VersionBehind {
				cs.NodegroupsBehindControlPlane++
			}
			if ng.AMILookupError != "" {
				cs.NodegroupCount--
			}
		}
		if len(ngFailures) > 0 {
			cs.Errors = append(cs.Errors, fmt.Sprintf("nodegroup(s): %s", strings.Join(ngFailures, "; ")))
		}
	}

	cs.Compute = s.detectCompute(ctx, name, cluster, cs.NodegroupCount)

	behind, addErr := s.addonsBehind(ctx, name, cs.Version)
	if addErr != nil {
		cs.Errors = append(cs.Errors, fmt.Sprintf("analyze addons: %v", addErr))
	}
	cs.AddonsBehind = behind

	return cs
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
// reported through the returned error alongside the partial summary.
func (s *Service) addonsBehind(ctx context.Context, cluster, k8sVersion string) (AddonsBehindSummary, error) {
	installed, err := s.addons.List(ctx, cluster, addons.ListOptions{})
	if err != nil {
		return AddonsBehindSummary{}, err
	}
	summary := AddonsBehindSummary{Total: len(installed)}
	var unreadable []string
	for _, a := range installed {
		// addons.List reports a failed DescribeAddon as Status UNKNOWN with an
		// empty version; comparing "" would count the addon as behind.
		if a.Version == "" || strings.EqualFold(a.Status, "UNKNOWN") {
			unreadable = append(unreadable, a.Name+" (installed version unknown)")
			continue
		}
		avail, verr := s.addons.GetAvailableVersions(ctx, a.Name, k8sVersion)
		if errors.Is(verr, addons.ErrNoVersionsFound) {
			continue // no compatible version published — nothing to compare
		}
		if verr != nil {
			unreadable = append(unreadable, a.Name+" (latest version unknown)")
			continue
		}
		if len(avail) == 0 {
			continue // defensive: treat like ErrNoVersionsFound
		}
		latest := avail[0].Version
		if addons.CompareVersions(a.Version, latest) < 0 {
			summary.Behind++
			summary.Names = append(summary.Names, a.Name)
		}
	}
	if len(unreadable) > 0 {
		return summary, fmt.Errorf("could not read version for %s", strings.Join(unreadable, ", "))
	}
	return summary, nil
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
