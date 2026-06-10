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

	// mu guards Calls: services fan out describe calls concurrently, so the
	// counters must be safe to increment from multiple goroutines. Read them
	// only after the operation under test has returned.
	mu sync.Mutex

	Calls struct {
		ListAddons            int
		DescribeAddon         int
		DescribeAddonVersions int
		UpdateAddon           int
		DescribeCluster       int
		ListNodegroups        int
		DescribeNodegroup     int
		UpdateNodegroupConfig int
		ListClusters          int
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
