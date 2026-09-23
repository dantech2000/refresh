package mocks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// EKSAPIBuilder constructs a mocks.EKSAPI with sensible defaults.
// Call methods to override individual behaviours before calling Build.
type EKSAPIBuilder struct {
	m *EKSAPI

	// clusters holds every cluster registered with WithCluster, in order.
	// DescribeCluster, ListClusters, ListNodegroups, ListAddons and
	// ListInsights consult it once it is non-empty.
	clusters []*ekstypes.Cluster

	// supportedVersions is what DescribeClusterVersions offers.
	supportedVersions []ekstypes.ClusterVersionInformation

	// insights are keyed by cluster name.
	insights map[string][]insightSpec

	// updates are scripted DescribeUpdate status sequences, keyed by update
	// ID. updatesMu guards the per-ID cursor: services poll concurrently.
	updatesMu sync.Mutex
	updates   map[string]*updateScript
}

// DefaultRegion is the region WithCluster builds endpoints and ARNs in when
// no ClusterRegion option is given.
const DefaultRegion = "us-east-1"

// DefaultSupportedVersions are the Kubernetes versions DescribeClusterVersions
// offers unless WithSupportedVersions replaces them. The newest four are in
// standard support, the rest in extended support.
var DefaultSupportedVersions = []string{"1.28", "1.29", "1.30", "1.31", "1.32", "1.33", "1.34", "1.35"}

type insightSpec struct {
	name       string
	status     ekstypes.InsightStatusValue
	k8sVersion string
}

type updateScript struct {
	statuses []ekstypes.UpdateStatus
	next     int
}

// NewEKSAPI returns a builder whose mock returns empty-but-valid responses by
// default. Override individual Fn fields or call builder methods for the
// behaviour each test needs.
func NewEKSAPI() *EKSAPIBuilder {
	b := &EKSAPIBuilder{m: &EKSAPI{}}

	b.m.ListClustersFn = func(_ context.Context, _ *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
		return &eks.ListClustersOutput{}, nil
	}
	b.m.ListAddonsFn = func(_ context.Context, in *eks.ListAddonsInput, _ ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
		if err := b.checkCluster(in.ClusterName); err != nil {
			return nil, err
		}
		return &eks.ListAddonsOutput{}, nil
	}
	b.m.ListNodegroupsFn = func(_ context.Context, in *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
		if err := b.checkCluster(in.ClusterName); err != nil {
			return nil, err
		}
		return &eks.ListNodegroupsOutput{}, nil
	}
	b.m.ListInsightsFn = func(_ context.Context, in *eks.ListInsightsInput, _ ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
		if err := b.checkCluster(in.ClusterName); err != nil {
			return nil, err
		}
		return &eks.ListInsightsOutput{}, nil
	}
	b.m.DescribeAddonVersionsFn = func(_ context.Context, _ *eks.DescribeAddonVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
		return &eks.DescribeAddonVersionsOutput{}, nil
	}
	b.WithSupportedVersions(DefaultSupportedVersions...)
	b.m.DescribeClusterVersionsFn = func(_ context.Context, in *eks.DescribeClusterVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error) {
		out := &eks.DescribeClusterVersionsOutput{}
		for _, v := range b.supportedVersions {
			if len(in.ClusterVersions) > 0 && !slices.Contains(in.ClusterVersions, aws.ToString(v.ClusterVersion)) {
				continue
			}
			out.ClusterVersions = append(out.ClusterVersions, v)
		}
		return out, nil
	}
	b.m.ListClustersFn = func(_ context.Context, _ *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
		out := &eks.ListClustersOutput{}
		for _, c := range b.clusters {
			out.Clusters = append(out.Clusters, aws.ToString(c.Name))
		}
		return out, nil
	}

	return b
}

// ClusterOption customises a cluster registered with WithCluster.
type ClusterOption func(*ekstypes.Cluster)

// ClusterEndpoint overrides the cluster's API server endpoint.
func ClusterEndpoint(url string) ClusterOption {
	return func(c *ekstypes.Cluster) { c.Endpoint = aws.String(url) }
}

// ClusterRegion places the cluster in region: its endpoint and ARN use it.
// Apply it before ClusterEndpoint if both are given.
func ClusterRegion(region string) ClusterOption {
	return func(c *ekstypes.Cluster) {
		name := aws.ToString(c.Name)
		c.Arn = aws.String(clusterARN(region, name))
		c.Endpoint = aws.String(clusterEndpoint(region, name))
	}
}

// ClusterStatus overrides the cluster status (ACTIVE by default).
func ClusterStatus(status ekstypes.ClusterStatus) ClusterOption {
	return func(c *ekstypes.Cluster) { c.Status = status }
}

// clusterEndpoint builds an endpoint in the shape EKS assigns:
// https://<32 hex chars>.gr7.<region>.eks.amazonaws.com. The hash is derived
// from the name so it is stable across runs.
func clusterEndpoint(region, name string) string {
	sum := sha256.Sum256([]byte(region + "/" + name))
	return fmt.Sprintf("https://%s.gr7.%s.eks.amazonaws.com", strings.ToUpper(hex.EncodeToString(sum[:16])), region)
}

func clusterARN(region, name string) string {
	return fmt.Sprintf("arn:aws:eks:%s:123456789012:cluster/%s", region, name)
}

// WithCluster registers a cluster: DescribeCluster returns it (ACTIVE, with a
// realistic endpoint and ARN in DefaultRegion unless options say otherwise)
// and ListClusters lists it. Calling WithCluster again accumulates clusters;
// re-registering a name replaces it.
//
// Once any cluster is registered, DescribeCluster, ListNodegroups, ListAddons
// and ListInsights fail with ResourceNotFoundException for any other name,
// as EKS does.
func (b *EKSAPIBuilder) WithCluster(name, k8sVersion string, opts ...ClusterOption) *EKSAPIBuilder {
	c := &ekstypes.Cluster{
		Name:            aws.String(name),
		Arn:             aws.String(clusterARN(DefaultRegion, name)),
		Version:         aws.String(k8sVersion),
		Status:          ekstypes.ClusterStatusActive,
		Endpoint:        aws.String(clusterEndpoint(DefaultRegion, name)),
		PlatformVersion: aws.String("eks.1"),
	}
	for _, opt := range opts {
		opt(c)
	}
	b.clusters = slices.DeleteFunc(b.clusters, func(existing *ekstypes.Cluster) bool {
		return aws.ToString(existing.Name) == name
	})
	b.clusters = append(b.clusters, c)

	b.m.DescribeClusterFn = func(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
		c := b.cluster(aws.ToString(in.Name))
		if c == nil {
			return nil, clusterNotFound(aws.ToString(in.Name))
		}
		cp := *c
		return &eks.DescribeClusterOutput{Cluster: &cp}, nil
	}
	return b
}

// cluster returns the registered cluster called name, or nil.
func (b *EKSAPIBuilder) cluster(name string) *ekstypes.Cluster {
	for _, c := range b.clusters {
		if aws.ToString(c.Name) == name {
			return c
		}
	}
	return nil
}

// checkCluster fails with ResourceNotFoundException when clusters are
// registered and name is not one of them. With none registered the builder
// stays permissive, for tests that never call WithCluster.
func (b *EKSAPIBuilder) checkCluster(name *string) error {
	if len(b.clusters) == 0 || b.cluster(aws.ToString(name)) != nil {
		return nil
	}
	return clusterNotFound(aws.ToString(name))
}

func clusterNotFound(name string) error {
	return notFound(fmt.Sprintf("No cluster found for name: %s.", name))
}

// WithSupportedVersions replaces the Kubernetes versions DescribeClusterVersions
// offers. Versions are given oldest first; the newest four are marked
// standard support and the rest extended support. A requested version not in
// the list is simply absent from the response, as with EKS.
func (b *EKSAPIBuilder) WithSupportedVersions(versions ...string) *EKSAPIBuilder {
	b.supportedVersions = nil
	for i, v := range versions {
		status := ekstypes.ClusterVersionStatusExtendedSupport
		if i >= len(versions)-4 {
			status = ekstypes.ClusterVersionStatusStandardSupport
		}
		b.supportedVersions = append(b.supportedVersions, ekstypes.ClusterVersionInformation{
			ClusterVersion: aws.String(v),
			ClusterType:    aws.String("eks"),
			Status:         status,
			DefaultVersion: i == len(versions)-1,
		})
	}
	return b
}

// WithPageSize makes every list call return at most n items per page with a
// NextToken for the rest (see EKSAPI.PageSize). 0, the default, disables
// paging.
func (b *EKSAPIBuilder) WithPageSize(n int) *EKSAPIBuilder {
	b.m.PageSize = n
	return b
}

// WithAddon registers an addon so that ListAddons includes it and DescribeAddon
// returns its details. Calling WithAddon multiple times accumulates addons.
func (b *EKSAPIBuilder) WithAddon(name, version string, status ekstypes.AddonStatus) *EKSAPIBuilder {
	prevList := b.m.ListAddonsFn
	prevDescribe := b.m.DescribeAddonFn

	b.m.ListAddonsFn = func(ctx context.Context, in *eks.ListAddonsInput, opts ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
		out, err := prevList(ctx, in, opts...)
		if err != nil {
			return nil, err
		}
		out.Addons = append(out.Addons, name)
		return out, nil
	}

	b.m.DescribeAddonFn = func(ctx context.Context, in *eks.DescribeAddonInput, opts ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
		if aws.ToString(in.AddonName) == name {
			return &eks.DescribeAddonOutput{
				Addon: &ekstypes.Addon{
					AddonName:    aws.String(name),
					AddonVersion: aws.String(version),
					Status:       status,
				},
			}, nil
		}
		if prevDescribe != nil {
			return prevDescribe(ctx, in, opts...)
		}
		return nil, &ekstypes.ResourceNotFoundException{Message: aws.String("addon not found: " + aws.ToString(in.AddonName))}
	}
	return b
}

// WithAddonVersions sets DescribeAddonVersions to return the given version list
// when queried for addonName. Versions should be in descending order (latest first).
func (b *EKSAPIBuilder) WithAddonVersions(addonName string, versions []string, k8sVersion string) *EKSAPIBuilder {
	prev := b.m.DescribeAddonVersionsFn

	b.m.DescribeAddonVersionsFn = func(ctx context.Context, in *eks.DescribeAddonVersionsInput, opts ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
		if aws.ToString(in.AddonName) == addonName {
			vinfos := make([]ekstypes.AddonVersionInfo, 0, len(versions))
			for _, v := range versions {
				vinfos = append(vinfos, ekstypes.AddonVersionInfo{
					AddonVersion:    aws.String(v),
					Compatibilities: []ekstypes.Compatibility{{ClusterVersion: aws.String(k8sVersion)}},
				})
			}
			return &eks.DescribeAddonVersionsOutput{
				Addons: []ekstypes.AddonInfo{
					{AddonName: aws.String(addonName), AddonVersions: vinfos},
				},
			}, nil
		}
		if prev != nil {
			return prev(ctx, in, opts...)
		}
		return &eks.DescribeAddonVersionsOutput{}, nil
	}
	return b
}

// WithUpdateAddon sets UpdateAddon to return a successful in-progress update.
func (b *EKSAPIBuilder) WithUpdateAddon(updateID string) *EKSAPIBuilder {
	b.m.UpdateAddonFn = func(_ context.Context, _ *eks.UpdateAddonInput, _ ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
		return &eks.UpdateAddonOutput{
			Update: &ekstypes.Update{
				Id:     aws.String(updateID),
				Status: ekstypes.UpdateStatusInProgress,
			},
		}, nil
	}
	return b
}

// WithNodegroup registers a nodegroup so ListNodegroups includes it and
// DescribeNodegroup returns its details (ACTIVE, no health issues). Calling
// WithNodegroup multiple times accumulates nodegroups in order.
func (b *EKSAPIBuilder) WithNodegroup(name, k8sVersion string, amiType ekstypes.AMITypes) *EKSAPIBuilder {
	prevList := b.m.ListNodegroupsFn
	prevDescribe := b.m.DescribeNodegroupFn

	b.m.ListNodegroupsFn = func(ctx context.Context, in *eks.ListNodegroupsInput, opts ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
		out, err := prevList(ctx, in, opts...)
		if err != nil {
			return nil, err
		}
		out.Nodegroups = append(out.Nodegroups, name)
		return out, nil
	}

	b.m.DescribeNodegroupFn = func(ctx context.Context, in *eks.DescribeNodegroupInput, opts ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		if aws.ToString(in.NodegroupName) == name {
			return &eks.DescribeNodegroupOutput{
				Nodegroup: &ekstypes.Nodegroup{
					NodegroupName: aws.String(name),
					Version:       aws.String(k8sVersion),
					AmiType:       amiType,
					Status:        ekstypes.NodegroupStatusActive,
				},
			}, nil
		}
		if prevDescribe != nil {
			return prevDescribe(ctx, in, opts...)
		}
		return nil, &ekstypes.ResourceNotFoundException{Message: aws.String("nodegroup not found: " + aws.ToString(in.NodegroupName))}
	}
	return b
}

// WithInsight registers an UPGRADE_READINESS insight on cluster, reported
// when ListInsights asks about that cluster and (if it filters by version)
// k8sVersion. Calling WithInsight multiple times accumulates.
func (b *EKSAPIBuilder) WithInsight(cluster, name string, status ekstypes.InsightStatusValue, k8sVersion string) *EKSAPIBuilder {
	if b.insights == nil {
		b.insights = map[string][]insightSpec{}
	}
	b.insights[cluster] = append(b.insights[cluster], insightSpec{name: name, status: status, k8sVersion: k8sVersion})

	b.m.ListInsightsFn = func(_ context.Context, in *eks.ListInsightsInput, _ ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
		if err := b.checkCluster(in.ClusterName); err != nil {
			return nil, err
		}
		out := &eks.ListInsightsOutput{}
		for i, ins := range b.insights[aws.ToString(in.ClusterName)] {
			if f := in.Filter; f != nil {
				if len(f.KubernetesVersions) > 0 && !slices.Contains(f.KubernetesVersions, ins.k8sVersion) {
					continue
				}
				if len(f.Categories) > 0 && !slices.Contains(f.Categories, ekstypes.CategoryUpgradeReadiness) {
					continue
				}
				if len(f.Statuses) > 0 && !slices.Contains(f.Statuses, ins.status) {
					continue
				}
			}
			out.Insights = append(out.Insights, ekstypes.InsightSummary{
				Id:                aws.String(fmt.Sprintf("insight-%s-%d", aws.ToString(in.ClusterName), i)),
				Name:              aws.String(ins.name),
				Category:          ekstypes.CategoryUpgradeReadiness,
				KubernetesVersion: aws.String(ins.k8sVersion),
				InsightStatus:     &ekstypes.InsightStatus{Status: ins.status},
			})
		}
		return out, nil
	}
	return b
}

// WithUpdateStatuses scripts DescribeUpdate for one update ID: each call
// returns the next status in order, and the last one repeats once the script
// runs out. DescribeUpdate for an ID with no script fails with
// ResourceNotFoundException, as EKS does for an unknown update.
func (b *EKSAPIBuilder) WithUpdateStatuses(updateID string, statuses ...ekstypes.UpdateStatus) *EKSAPIBuilder {
	if len(statuses) == 0 {
		panic("mocks.WithUpdateStatuses: at least one status is required")
	}
	b.updatesMu.Lock()
	if b.updates == nil {
		b.updates = map[string]*updateScript{}
	}
	b.updates[updateID] = &updateScript{statuses: statuses}
	b.updatesMu.Unlock()

	b.m.DescribeUpdateFn = func(_ context.Context, in *eks.DescribeUpdateInput, _ ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		id := aws.ToString(in.UpdateId)
		b.updatesMu.Lock()
		defer b.updatesMu.Unlock()
		script, ok := b.updates[id]
		if !ok {
			return nil, notFound(fmt.Sprintf("No update found for ID: %s.", id))
		}
		status := script.statuses[min(script.next, len(script.statuses)-1)]
		script.next++
		return &eks.DescribeUpdateOutput{
			Update: &ekstypes.Update{Id: aws.String(id), Status: status},
		}, nil
	}
	return b
}

// Build returns the fully configured mock.
func (b *EKSAPIBuilder) Build() *EKSAPI {
	return b.m
}
