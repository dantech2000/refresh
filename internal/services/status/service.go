package status

import (
	"context"
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

// NodegroupLister provides per-cluster nodegroup summaries (with AMI status).
// Satisfied by *nodegroup.ServiceImpl.
type NodegroupLister interface {
	List(ctx context.Context, clusterName string, options nodegroup.ListOptions) ([]nodegroup.NodegroupSummary, error)
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
	return results, nil
}

func (s *Service) listClusterNames(ctx context.Context) ([]string, error) {
	var names []string
	var token *string
	for {
		out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.ListClustersOutput, error) {
			return s.clusterAPI.ListClusters(rc, &eks.ListClustersInput{NextToken: token})
		})
		if err != nil {
			return nil, fmt.Errorf("listing clusters in %s: %w", s.region, err)
		}
		names = append(names, out.Clusters...)
		if out.NextToken == nil {
			break
		}
		token = out.NextToken
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
	cs.Support = s.resolveSupport(ctx, cs.Version)
	if cluster.Health != nil {
		cs.HealthIssues = len(cluster.Health.Issues)
	}

	ngs, ngErr := s.nodegroups.List(ctx, name, nodegroup.ListOptions{})
	if ngErr != nil {
		cs.Errors = append(cs.Errors, fmt.Sprintf("list nodegroups: %v", ngErr))
	} else {
		cs.NodegroupCount = len(ngs)
		cs.StaleAMI = s.staleAMISummary(ctx, ngs)
	}

	cs.Compute = s.detectCompute(ctx, cluster, cs.NodegroupCount)

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
	out, err := s.ec2.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: dedupe(amiIDs)})
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
// version compatible with the cluster's Kubernetes version.
func (s *Service) addonsBehind(ctx context.Context, cluster, k8sVersion string) (AddonsBehindSummary, error) {
	installed, err := s.addons.List(ctx, cluster, addons.ListOptions{})
	if err != nil {
		return AddonsBehindSummary{}, err
	}
	summary := AddonsBehindSummary{Total: len(installed)}
	for _, a := range installed {
		avail, verr := s.addons.GetAvailableVersions(ctx, a.Name, k8sVersion)
		if verr != nil || len(avail) == 0 {
			continue // can't determine latest — don't guess
		}
		latest := avail[0].Version
		if addons.CompareVersions(a.Version, latest) < 0 {
			summary.Behind++
			summary.Names = append(summary.Names, a.Name)
		}
	}
	return summary, nil
}

// karpenterTagKeys are the EC2 instance tags Karpenter sets on the nodes it
// provisions (current and legacy).
var karpenterTagKeys = []string{"karpenter.sh/nodepool", "karpenter.sh/provisioner-name"}

// detectCompute classifies how a cluster runs compute so a nodegroup-less
// cluster never renders as an empty "nothing to do" row.
func (s *Service) detectCompute(ctx context.Context, cluster *ekstypes.Cluster, ngCount int) ComputeType {
	if cluster != nil && cluster.ComputeConfig != nil && aws.ToBool(cluster.ComputeConfig.Enabled) {
		return ComputeAutoMode
	}
	if ngCount > 0 {
		return ComputeManaged
	}
	if s.hasKarpenterInstances(ctx) {
		return ComputeKarpenter
	}
	return ComputeNone
}

// hasKarpenterInstances is a best-effort probe for Karpenter-provisioned EC2
// instances in the region. Any error (including missing permission) is treated
// as "no signal".
func (s *Service) hasKarpenterInstances(ctx context.Context) bool {
	if s.ec2 == nil {
		return false
	}
	out, err := s.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		MaxResults: aws.Int32(5),
		Filters: []ec2types.Filter{
			{Name: aws.String("tag-key"), Values: karpenterTagKeys},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running"}},
		},
	})
	if err != nil || out == nil {
		return false
	}
	for _, r := range out.Reservations {
		if len(r.Instances) > 0 {
			return true
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
