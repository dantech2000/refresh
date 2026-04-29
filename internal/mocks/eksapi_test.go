package mocks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

func TestEKSAPIBuilderDefaultsAndOverrides(t *testing.T) {
	mock := NewEKSAPI().
		WithCluster("prod", "1.30").
		WithAddon("vpc-cni", "v1.0.0", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.1.0", "v1.0.0"}, "1.30").
		WithUpdateAddon("upd-1").
		Build()

	ctx := context.Background()

	clusters, err := mock.ListClusters(ctx, &eks.ListClustersInput{})
	if err != nil || clusters == nil {
		t.Fatalf("ListClusters() = %v, %v", clusters, err)
	}
	addons, err := mock.ListAddons(ctx, &eks.ListAddonsInput{ClusterName: aws.String("prod")})
	if err != nil || len(addons.Addons) != 1 || addons.Addons[0] != "vpc-cni" {
		t.Fatalf("ListAddons() = %#v, %v", addons, err)
	}
	nodegroups, err := mock.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String("prod")})
	if err != nil || nodegroups == nil {
		t.Fatalf("ListNodegroups() = %v, %v", nodegroups, err)
	}
	cluster, err := mock.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("prod")})
	if err != nil || aws.ToString(cluster.Cluster.Name) != "prod" {
		t.Fatalf("DescribeCluster() = %#v, %v", cluster, err)
	}
	addon, err := mock.DescribeAddon(ctx, &eks.DescribeAddonInput{AddonName: aws.String("vpc-cni")})
	if err != nil || aws.ToString(addon.Addon.AddonVersion) != "v1.0.0" {
		t.Fatalf("DescribeAddon() = %#v, %v", addon, err)
	}
	versions, err := mock.DescribeAddonVersions(ctx, &eks.DescribeAddonVersionsInput{AddonName: aws.String("vpc-cni")})
	if err != nil || len(versions.Addons) != 1 || len(versions.Addons[0].AddonVersions) != 2 {
		t.Fatalf("DescribeAddonVersions() = %#v, %v", versions, err)
	}
	update, err := mock.UpdateAddon(ctx, &eks.UpdateAddonInput{AddonName: aws.String("vpc-cni")})
	if err != nil || aws.ToString(update.Update.Id) != "upd-1" {
		t.Fatalf("UpdateAddon() = %#v, %v", update, err)
	}

	if mock.Calls.ListClusters != 1 || mock.Calls.ListAddons != 1 || mock.Calls.ListNodegroups != 1 ||
		mock.Calls.DescribeCluster != 1 || mock.Calls.DescribeAddon != 1 ||
		mock.Calls.DescribeAddonVersions != 1 || mock.Calls.UpdateAddon != 1 {
		t.Fatalf("unexpected calls: %#v", mock.Calls)
	}
}

func TestEKSAPIBuilderFallbacks(t *testing.T) {
	mock := NewEKSAPI().
		WithAddon("vpc-cni", "v1.0.0", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.0.0"}, "1.30").
		Build()

	if _, err := mock.DescribeAddon(context.Background(), &eks.DescribeAddonInput{AddonName: aws.String("coredns")}); err == nil {
		t.Fatal("DescribeAddon() expected not found error")
	}

	out, err := mock.DescribeAddonVersions(context.Background(), &eks.DescribeAddonVersionsInput{AddonName: aws.String("coredns")})
	if err != nil || len(out.Addons) != 0 {
		t.Fatalf("DescribeAddonVersions() fallback = %#v, %v", out, err)
	}
}

func TestEKSAPIBuilderChainedFallbacksAndErrors(t *testing.T) {
	sentinel := errors.New("list failed")
	builder := NewEKSAPI()
	builder.m.ListAddonsFn = func(context.Context, *eks.ListAddonsInput, ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
		return nil, sentinel
	}
	mock := builder.WithAddon("vpc-cni", "v1.0.0", ekstypes.AddonStatusActive).Build()
	if _, err := mock.ListAddons(context.Background(), &eks.ListAddonsInput{}); !errors.Is(err, sentinel) {
		t.Fatalf("ListAddons error = %v, want sentinel", err)
	}

	mock = NewEKSAPI().
		WithAddon("vpc-cni", "v1.0.0", ekstypes.AddonStatusActive).
		WithAddon("coredns", "v1.0.1", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.0.0"}, "1.30").
		WithAddonVersions("coredns", []string{"v1.0.1"}, "1.30").
		Build()

	addon, err := mock.DescribeAddon(context.Background(), &eks.DescribeAddonInput{AddonName: aws.String("vpc-cni")})
	if err != nil || aws.ToString(addon.Addon.AddonName) != "vpc-cni" {
		t.Fatalf("DescribeAddon chained fallback = %#v, %v", addon, err)
	}
	versions, err := mock.DescribeAddonVersions(context.Background(), &eks.DescribeAddonVersionsInput{AddonName: aws.String("vpc-cni")})
	if err != nil || aws.ToString(versions.Addons[0].AddonName) != "vpc-cni" {
		t.Fatalf("DescribeAddonVersions chained fallback = %#v, %v", versions, err)
	}
}

func TestEKSAPIDelegatesAndUnexpectedCalls(t *testing.T) {
	ctx := context.Background()
	mock := &EKSAPI{
		ListAddonsFn: func(context.Context, *eks.ListAddonsInput, ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
			return &eks.ListAddonsOutput{}, nil
		},
		DescribeAddonFn: func(context.Context, *eks.DescribeAddonInput, ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
			return &eks.DescribeAddonOutput{}, nil
		},
		DescribeAddonVersionsFn: func(context.Context, *eks.DescribeAddonVersionsInput, ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
			return &eks.DescribeAddonVersionsOutput{}, nil
		},
		UpdateAddonFn: func(context.Context, *eks.UpdateAddonInput, ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
			return &eks.UpdateAddonOutput{}, nil
		},
		DescribeClusterFn: func(context.Context, *eks.DescribeClusterInput, ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
			return &eks.DescribeClusterOutput{}, nil
		},
		ListNodegroupsFn: func(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return &eks.ListNodegroupsOutput{}, nil
		},
		DescribeNodegroupFn: func(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{}, nil
		},
		UpdateNodegroupConfigFn: func(context.Context, *eks.UpdateNodegroupConfigInput, ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error) {
			return &eks.UpdateNodegroupConfigOutput{}, nil
		},
		ListClustersFn: func(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
			return &eks.ListClustersOutput{}, nil
		},
	}

	_, _ = mock.ListAddons(ctx, &eks.ListAddonsInput{ClusterName: aws.String("cluster")})
	_, _ = mock.DescribeAddon(ctx, &eks.DescribeAddonInput{AddonName: aws.String("addon")})
	_, _ = mock.DescribeAddonVersions(ctx, &eks.DescribeAddonVersionsInput{AddonName: aws.String("addon")})
	_, _ = mock.UpdateAddon(ctx, &eks.UpdateAddonInput{AddonName: aws.String("addon")})
	_, _ = mock.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("cluster")})
	_, _ = mock.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String("cluster")})
	_, _ = mock.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{NodegroupName: aws.String("ng")})
	_, _ = mock.UpdateNodegroupConfig(ctx, &eks.UpdateNodegroupConfigInput{NodegroupName: aws.String("ng")})
	_, _ = mock.ListClusters(ctx, &eks.ListClustersInput{})

	for name, call := range map[string]int{
		"ListAddons":            mock.Calls.ListAddons,
		"DescribeAddon":         mock.Calls.DescribeAddon,
		"DescribeAddonVersions": mock.Calls.DescribeAddonVersions,
		"UpdateAddon":           mock.Calls.UpdateAddon,
		"DescribeCluster":       mock.Calls.DescribeCluster,
		"ListNodegroups":        mock.Calls.ListNodegroups,
		"DescribeNodegroup":     mock.Calls.DescribeNodegroup,
		"UpdateNodegroupConfig": mock.Calls.UpdateNodegroupConfig,
		"ListClusters":          mock.Calls.ListClusters,
	} {
		if call != 1 {
			t.Fatalf("%s calls = %d, want 1", name, call)
		}
	}
}

func TestEKSAPIUnexpectedCallPanics(t *testing.T) {
	tests := []struct {
		name string
		call func(*EKSAPI)
		want string
	}{
		{"ListAddons", func(m *EKSAPI) { _, _ = m.ListAddons(context.Background(), &eks.ListAddonsInput{ClusterName: aws.String("cluster")}) }, "cluster"},
		{"DescribeAddon", func(m *EKSAPI) { _, _ = m.DescribeAddon(context.Background(), &eks.DescribeAddonInput{}) }, "<nil>"},
		{"DescribeAddonVersions", func(m *EKSAPI) { _, _ = m.DescribeAddonVersions(context.Background(), &eks.DescribeAddonVersionsInput{}) }, "<nil>"},
		{"UpdateAddon", func(m *EKSAPI) { _, _ = m.UpdateAddon(context.Background(), &eks.UpdateAddonInput{}) }, "<nil>"},
		{"DescribeCluster", func(m *EKSAPI) { _, _ = m.DescribeCluster(context.Background(), &eks.DescribeClusterInput{}) }, "<nil>"},
		{"ListNodegroups", func(m *EKSAPI) { _, _ = m.ListNodegroups(context.Background(), &eks.ListNodegroupsInput{}) }, "<nil>"},
		{"DescribeNodegroup", func(m *EKSAPI) { _, _ = m.DescribeNodegroup(context.Background(), &eks.DescribeNodegroupInput{}) }, "<nil>"},
		{"UpdateNodegroupConfig", func(m *EKSAPI) { _, _ = m.UpdateNodegroupConfig(context.Background(), &eks.UpdateNodegroupConfigInput{}) }, "<nil>"},
		{"ListClusters", func(m *EKSAPI) { _, _ = m.ListClusters(context.Background(), &eks.ListClustersInput{}) }, "ListClusters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic")
				}
				if !strings.Contains(r.(string), tt.want) {
					t.Fatalf("panic = %q, want %q", r, tt.want)
				}
			}()
			tt.call(&EKSAPI{})
		})
	}
}
