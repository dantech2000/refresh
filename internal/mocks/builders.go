package mocks

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// EKSAPIBuilder constructs a mocks.EKSAPI with sensible defaults.
// Call methods to override individual behaviours before calling Build.
type EKSAPIBuilder struct {
	m *EKSAPI
}

// NewEKSAPI returns a builder whose mock returns empty-but-valid responses by
// default. Override individual Fn fields or call builder methods for the
// behaviour each test needs.
func NewEKSAPI() *EKSAPIBuilder {
	b := &EKSAPIBuilder{m: &EKSAPI{}}

	b.m.ListClustersFn = func(_ context.Context, _ *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
		return &eks.ListClustersOutput{}, nil
	}
	b.m.ListAddonsFn = func(_ context.Context, _ *eks.ListAddonsInput, _ ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
		return &eks.ListAddonsOutput{}, nil
	}
	b.m.ListNodegroupsFn = func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
		return &eks.ListNodegroupsOutput{}, nil
	}
	b.m.ListInsightsFn = func(_ context.Context, _ *eks.ListInsightsInput, _ ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
		return &eks.ListInsightsOutput{}, nil
	}
	b.m.DescribeAddonVersionsFn = func(_ context.Context, _ *eks.DescribeAddonVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
		return &eks.DescribeAddonVersionsOutput{}, nil
	}
	// Echo requested versions back as offered, so upgrade tests don't all
	// need to enumerate the EKS version catalogue.
	b.m.DescribeClusterVersionsFn = func(_ context.Context, in *eks.DescribeClusterVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error) {
		out := &eks.DescribeClusterVersionsOutput{}
		for _, v := range in.ClusterVersions {
			out.ClusterVersions = append(out.ClusterVersions, ekstypes.ClusterVersionInformation{
				ClusterVersion: aws.String(v),
			})
		}
		return out, nil
	}

	return b
}

// WithCluster sets DescribeCluster to return the given name and Kubernetes version.
func (b *EKSAPIBuilder) WithCluster(name, k8sVersion string) *EKSAPIBuilder {
	b.m.DescribeClusterFn = func(_ context.Context, _ *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
		return &eks.DescribeClusterOutput{
			Cluster: &ekstypes.Cluster{
				Name:    aws.String(name),
				Version: aws.String(k8sVersion),
				Status:  ekstypes.ClusterStatusActive,
			},
		}, nil
	}
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

// WithInsight registers an UPGRADE_READINESS insight returned for the given
// Kubernetes version. Calling WithInsight multiple times accumulates.
func (b *EKSAPIBuilder) WithInsight(name string, status ekstypes.InsightStatusValue, k8sVersion string) *EKSAPIBuilder {
	prev := b.m.ListInsightsFn

	b.m.ListInsightsFn = func(ctx context.Context, in *eks.ListInsightsInput, opts ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
		out, err := prev(ctx, in, opts...)
		if err != nil {
			return nil, err
		}
		if in.Filter != nil && len(in.Filter.KubernetesVersions) > 0 {
			match := false
			for _, v := range in.Filter.KubernetesVersions {
				if v == k8sVersion {
					match = true
				}
			}
			if !match {
				return out, nil
			}
		}
		out.Insights = append(out.Insights, ekstypes.InsightSummary{
			Name:          aws.String(name),
			Category:      ekstypes.CategoryUpgradeReadiness,
			InsightStatus: &ekstypes.InsightStatus{Status: status},
		})
		return out, nil
	}
	return b
}

// WithUpdateClusterVersion sets UpdateClusterVersion to return a successful
// in-progress update with the given ID.
func (b *EKSAPIBuilder) WithUpdateClusterVersion(updateID string) *EKSAPIBuilder {
	b.m.UpdateClusterVersionFn = func(_ context.Context, _ *eks.UpdateClusterVersionInput, _ ...func(*eks.Options)) (*eks.UpdateClusterVersionOutput, error) {
		return &eks.UpdateClusterVersionOutput{
			Update: &ekstypes.Update{
				Id:     aws.String(updateID),
				Status: ekstypes.UpdateStatusInProgress,
				Type:   ekstypes.UpdateTypeVersionUpdate,
			},
		}, nil
	}
	return b
}

// WithUpdateNodegroupVersion sets UpdateNodegroupVersion to return a
// successful in-progress update with the given ID.
func (b *EKSAPIBuilder) WithUpdateNodegroupVersion(updateID string) *EKSAPIBuilder {
	b.m.UpdateNodegroupVersionFn = func(_ context.Context, _ *eks.UpdateNodegroupVersionInput, _ ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error) {
		return &eks.UpdateNodegroupVersionOutput{
			Update: &ekstypes.Update{
				Id:     aws.String(updateID),
				Status: ekstypes.UpdateStatusInProgress,
				Type:   ekstypes.UpdateTypeVersionUpdate,
			},
		}, nil
	}
	return b
}

// WithDescribeUpdate sets DescribeUpdate to report every update with the
// given terminal status.
func (b *EKSAPIBuilder) WithDescribeUpdate(status ekstypes.UpdateStatus) *EKSAPIBuilder {
	b.m.DescribeUpdateFn = func(_ context.Context, in *eks.DescribeUpdateInput, _ ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		return &eks.DescribeUpdateOutput{
			Update: &ekstypes.Update{
				Id:     in.UpdateId,
				Status: status,
			},
		}, nil
	}
	return b
}

// Build returns the fully configured mock.
func (b *EKSAPIBuilder) Build() *EKSAPI {
	return b.m
}
