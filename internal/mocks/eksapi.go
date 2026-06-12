// Package mocks provides configurable mock implementations of AWS client interfaces
// used across the service packages. Each mock uses function fields so individual
// test cases can inject their own behavior without subclassing.
//
// A single [EKSAPI] struct satisfies addons.EKSAPI, nodegroup.EKSAPI, and
// cluster.EKSAPI — the union of all their methods.
package mocks

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/eks"
)

// EKSAPI is a test double for the EKS client. Set any Fn field to control the
// response for that method; leave it nil to get a "not implemented" panic that
// identifies the unexpected call in test output.
//
// Calls tracks how many times each method was invoked.
type EKSAPI struct {
	ListAddonsFn            func(ctx context.Context, in *eks.ListAddonsInput, optFns ...func(*eks.Options)) (*eks.ListAddonsOutput, error)
	DescribeAddonFn         func(ctx context.Context, in *eks.DescribeAddonInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonOutput, error)
	DescribeAddonVersionsFn func(ctx context.Context, in *eks.DescribeAddonVersionsInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error)
	UpdateAddonFn           func(ctx context.Context, in *eks.UpdateAddonInput, optFns ...func(*eks.Options)) (*eks.UpdateAddonOutput, error)
	DescribeClusterFn       func(ctx context.Context, in *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	ListNodegroupsFn        func(ctx context.Context, in *eks.ListNodegroupsInput, optFns ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroupFn     func(ctx context.Context, in *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
	UpdateNodegroupConfigFn func(ctx context.Context, in *eks.UpdateNodegroupConfigInput, optFns ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error)
	ListClustersFn          func(ctx context.Context, in *eks.ListClustersInput, optFns ...func(*eks.Options)) (*eks.ListClustersOutput, error)

	UpdateClusterVersionFn    func(ctx context.Context, in *eks.UpdateClusterVersionInput, optFns ...func(*eks.Options)) (*eks.UpdateClusterVersionOutput, error)
	UpdateNodegroupVersionFn  func(ctx context.Context, in *eks.UpdateNodegroupVersionInput, optFns ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error)
	DescribeUpdateFn          func(ctx context.Context, in *eks.DescribeUpdateInput, optFns ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error)
	DescribeClusterVersionsFn func(ctx context.Context, in *eks.DescribeClusterVersionsInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error)
	ListInsightsFn            func(ctx context.Context, in *eks.ListInsightsInput, optFns ...func(*eks.Options)) (*eks.ListInsightsOutput, error)
	DescribeInsightFn         func(ctx context.Context, in *eks.DescribeInsightInput, optFns ...func(*eks.Options)) (*eks.DescribeInsightOutput, error)

	// mu guards Calls: services fan out describe calls concurrently, so the
	// counters must be safe to increment from multiple goroutines. Read them
	// only after the operation under test has returned.
	mu sync.Mutex

	Calls struct {
		ListAddons              int
		DescribeAddon           int
		DescribeAddonVersions   int
		UpdateAddon             int
		DescribeCluster         int
		ListNodegroups          int
		DescribeNodegroup       int
		UpdateNodegroupConfig   int
		ListClusters            int
		UpdateClusterVersion    int
		UpdateNodegroupVersion  int
		DescribeUpdate          int
		DescribeClusterVersions int
		ListInsights            int
		DescribeInsight         int
	}
}

func (m *EKSAPI) ListAddons(ctx context.Context, in *eks.ListAddonsInput, optFns ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
	m.inc(&m.Calls.ListAddons)
	if m.ListAddonsFn == nil {
		panic(fmt.Sprintf("mocks.EKSAPI: unexpected call to ListAddons (cluster=%s)", ptrStr(in.ClusterName)))
	}
	return m.ListAddonsFn(ctx, in, optFns...)
}

func (m *EKSAPI) DescribeAddon(ctx context.Context, in *eks.DescribeAddonInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
	m.inc(&m.Calls.DescribeAddon)
	if m.DescribeAddonFn == nil {
		panic(fmt.Sprintf("mocks.EKSAPI: unexpected call to DescribeAddon (addon=%s)", ptrStr(in.AddonName)))
	}
	return m.DescribeAddonFn(ctx, in, optFns...)
}

func (m *EKSAPI) DescribeAddonVersions(ctx context.Context, in *eks.DescribeAddonVersionsInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
	m.inc(&m.Calls.DescribeAddonVersions)
	if m.DescribeAddonVersionsFn == nil {
		panic(fmt.Sprintf("mocks.EKSAPI: unexpected call to DescribeAddonVersions (addon=%s)", ptrStr(in.AddonName)))
	}
	return m.DescribeAddonVersionsFn(ctx, in, optFns...)
}

func (m *EKSAPI) UpdateAddon(ctx context.Context, in *eks.UpdateAddonInput, optFns ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
	m.inc(&m.Calls.UpdateAddon)
	if m.UpdateAddonFn == nil {
		panic(fmt.Sprintf("mocks.EKSAPI: unexpected call to UpdateAddon (addon=%s)", ptrStr(in.AddonName)))
	}
	return m.UpdateAddonFn(ctx, in, optFns...)
}

func (m *EKSAPI) DescribeCluster(ctx context.Context, in *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	m.inc(&m.Calls.DescribeCluster)
	if m.DescribeClusterFn == nil {
		panic(fmt.Sprintf("mocks.EKSAPI: unexpected call to DescribeCluster (cluster=%s)", ptrStr(in.Name)))
	}
	return m.DescribeClusterFn(ctx, in, optFns...)
}

func (m *EKSAPI) ListNodegroups(ctx context.Context, in *eks.ListNodegroupsInput, optFns ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	m.inc(&m.Calls.ListNodegroups)
	if m.ListNodegroupsFn == nil {
		panic(fmt.Sprintf("mocks.EKSAPI: unexpected call to ListNodegroups (cluster=%s)", ptrStr(in.ClusterName)))
	}
	return m.ListNodegroupsFn(ctx, in, optFns...)
}

func (m *EKSAPI) DescribeNodegroup(ctx context.Context, in *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	m.inc(&m.Calls.DescribeNodegroup)
	if m.DescribeNodegroupFn == nil {
		panic(fmt.Sprintf("mocks.EKSAPI: unexpected call to DescribeNodegroup (ng=%s)", ptrStr(in.NodegroupName)))
	}
	return m.DescribeNodegroupFn(ctx, in, optFns...)
}

func (m *EKSAPI) UpdateNodegroupConfig(ctx context.Context, in *eks.UpdateNodegroupConfigInput, optFns ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error) {
	m.inc(&m.Calls.UpdateNodegroupConfig)
	if m.UpdateNodegroupConfigFn == nil {
		panic(fmt.Sprintf("mocks.EKSAPI: unexpected call to UpdateNodegroupConfig (ng=%s)", ptrStr(in.NodegroupName)))
	}
	return m.UpdateNodegroupConfigFn(ctx, in, optFns...)
}

func (m *EKSAPI) ListClusters(ctx context.Context, in *eks.ListClustersInput, optFns ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	m.inc(&m.Calls.ListClusters)
	if m.ListClustersFn == nil {
		panic("mocks.EKSAPI: unexpected call to ListClusters")
	}
	return m.ListClustersFn(ctx, in, optFns...)
}

func (m *EKSAPI) UpdateClusterVersion(ctx context.Context, in *eks.UpdateClusterVersionInput, optFns ...func(*eks.Options)) (*eks.UpdateClusterVersionOutput, error) {
	m.inc(&m.Calls.UpdateClusterVersion)
	if m.UpdateClusterVersionFn == nil {
		panic(fmt.Sprintf("mocks.EKSAPI: unexpected call to UpdateClusterVersion (cluster=%s)", ptrStr(in.Name)))
	}
	return m.UpdateClusterVersionFn(ctx, in, optFns...)
}

func (m *EKSAPI) UpdateNodegroupVersion(ctx context.Context, in *eks.UpdateNodegroupVersionInput, optFns ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error) {
	m.inc(&m.Calls.UpdateNodegroupVersion)
	if m.UpdateNodegroupVersionFn == nil {
		panic(fmt.Sprintf("mocks.EKSAPI: unexpected call to UpdateNodegroupVersion (ng=%s)", ptrStr(in.NodegroupName)))
	}
	return m.UpdateNodegroupVersionFn(ctx, in, optFns...)
}

func (m *EKSAPI) DescribeUpdate(ctx context.Context, in *eks.DescribeUpdateInput, optFns ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
	m.inc(&m.Calls.DescribeUpdate)
	if m.DescribeUpdateFn == nil {
		panic(fmt.Sprintf("mocks.EKSAPI: unexpected call to DescribeUpdate (id=%s)", ptrStr(in.UpdateId)))
	}
	return m.DescribeUpdateFn(ctx, in, optFns...)
}

func (m *EKSAPI) DescribeClusterVersions(ctx context.Context, in *eks.DescribeClusterVersionsInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error) {
	m.inc(&m.Calls.DescribeClusterVersions)
	if m.DescribeClusterVersionsFn == nil {
		panic("mocks.EKSAPI: unexpected call to DescribeClusterVersions")
	}
	return m.DescribeClusterVersionsFn(ctx, in, optFns...)
}

func (m *EKSAPI) ListInsights(ctx context.Context, in *eks.ListInsightsInput, optFns ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
	m.inc(&m.Calls.ListInsights)
	if m.ListInsightsFn == nil {
		panic(fmt.Sprintf("mocks.EKSAPI: unexpected call to ListInsights (cluster=%s)", ptrStr(in.ClusterName)))
	}
	return m.ListInsightsFn(ctx, in, optFns...)
}

func (m *EKSAPI) DescribeInsight(ctx context.Context, in *eks.DescribeInsightInput, optFns ...func(*eks.Options)) (*eks.DescribeInsightOutput, error) {
	m.inc(&m.Calls.DescribeInsight)
	if m.DescribeInsightFn == nil {
		panic(fmt.Sprintf("mocks.EKSAPI: unexpected call to DescribeInsight (id=%s)", ptrStr(in.Id)))
	}
	return m.DescribeInsightFn(ctx, in, optFns...)
}

func (m *EKSAPI) inc(counter *int) {
	m.mu.Lock()
	*counter++
	m.mu.Unlock()
}

func ptrStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
