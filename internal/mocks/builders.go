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

// Build returns the fully configured mock.
func (b *EKSAPIBuilder) Build() *EKSAPI {
	return b.m
}
