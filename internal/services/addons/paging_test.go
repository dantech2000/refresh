package addons

import (
	"context"
	"log/slog"
	"slices"
	"testing"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// ListAddonNames follows NextToken across every page.
func TestListAddonNames_FollowsNextToken(t *testing.T) {
	want := []string{"vpc-cni", "coredns", "kube-proxy", "aws-ebs-csi-driver", "eks-pod-identity-agent"}
	b := mocks.NewEKSAPI().WithPageSize(2).WithCluster("prod", "1.32")
	for _, name := range want {
		b.WithAddon(name, "v1.0.0-eksbuild.1", ekstypes.AddonStatusActive)
	}
	m := b.Build()

	got, err := NewService(m, slog.New(slog.DiscardHandler)).ListAddonNames(context.Background(), "prod")
	if err != nil {
		t.Fatalf("ListAddonNames: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("addons = %v, want %v", got, want)
	}
	if m.Calls.ListAddons != 3 {
		t.Fatalf("ListAddons calls = %d, want 3 pages", m.Calls.ListAddons)
	}
}

// GetAvailableVersions reads every page of DescribeAddonVersions. The mock
// lists versions oldest first, so the newest is only on the last page: a
// service that stopped after page one would report v1.0.0 as latest.
func TestGetAvailableVersions_FollowsNextToken(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithPageSize(2).
		WithAddonVersions("vpc-cni", []string{"v1.0.0", "v1.1.0", "v1.2.0", "v1.10.0", "v1.3.0"}, "1.32").
		Build()

	got, err := NewService(m, slog.New(slog.DiscardHandler)).GetAvailableVersions(context.Background(), "vpc-cni", "1.32")
	if err != nil {
		t.Fatalf("GetAvailableVersions: %v", err)
	}
	var versions []string
	for _, v := range got {
		versions = append(versions, v.Version)
	}
	want := []string{"v1.10.0", "v1.3.0", "v1.2.0", "v1.1.0", "v1.0.0"}
	if !slices.Equal(versions, want) {
		t.Fatalf("versions = %v, want %v (all pages, newest first)", versions, want)
	}
	if m.Calls.DescribeAddonVersions != 3 {
		t.Fatalf("DescribeAddonVersions calls = %d, want 3 pages", m.Calls.DescribeAddonVersions)
	}
}

// List resolves every addon across pages.
func TestList_FollowsNextToken(t *testing.T) {
	names := []string{"a1", "a2", "a3"}
	b := mocks.NewEKSAPI().WithPageSize(1).WithCluster("prod", "1.32")
	for _, name := range names {
		b.WithAddon(name, "v1.0.0", ekstypes.AddonStatusActive)
	}
	m := b.Build()

	got, err := NewService(m, slog.New(slog.DiscardHandler)).List(context.Background(), "prod", ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var gotNames []string
	for _, a := range got {
		gotNames = append(gotNames, a.Name)
	}
	slices.Sort(gotNames)
	if !slices.Equal(gotNames, names) {
		t.Fatalf("List = %v, want %v", gotNames, names)
	}
}
