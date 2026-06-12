//go:build integration

// Package integration holds opt-in, end-to-end tests that talk to a real AWS
// account. They are excluded from the default `go test ./...` by the
// `integration` build tag above and are additionally gated at runtime on the
// REFRESH_INTEGRATION env var, so even `go test -tags integration ./...` is a
// no-op unless you explicitly opt in.
//
// Prerequisites to actually exercise these tests:
//
//   - An AWS account reachable via the standard credential chain
//     (env vars, shared config/credentials, SSO, or an instance/role profile).
//   - A disposable EKS test cluster you don't mind issuing read-only calls
//     against. Set its name via REFRESH_TEST_CLUSTER (the cluster-bound tests
//     skip when it is unset).
//   - REFRESH_INTEGRATION=1 in the environment.
//
// Run with:
//
//	REFRESH_INTEGRATION=1 \
//	REFRESH_TEST_CLUSTER=my-disposable-cluster \
//	task test:integration
//
// These tests are intentionally read-only / dry-run: they assert basic
// invariants of the live AWS surface and must never mutate cluster state.
package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/dantech2000/refresh/internal/awsconfig"
	"github.com/dantech2000/refresh/internal/commands/factory"
	addonsvc "github.com/dantech2000/refresh/internal/services/addons"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
)

// requireIntegration skips the test unless REFRESH_INTEGRATION=1. Call it at
// the top of every test so the suite is a no-op without an explicit opt-in.
func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("REFRESH_INTEGRATION") != "1" {
		t.Skip("set REFRESH_INTEGRATION=1 and AWS creds to run")
	}
}

// loadConfig loads real AWS config through the same path the application uses.
// A nil *cli.Command is valid: awsconfig.Load falls back to env vars, the
// active refresh context, and SDK defaults.
func loadConfig(t *testing.T) aws.Config {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := awsconfig.Load(ctx, nil)
	if err != nil {
		t.Fatalf("loading AWS config: %v", err)
	}
	return cfg
}

// testCluster returns REFRESH_TEST_CLUSTER or skips when it is unset, so
// cluster-scoped tests don't fail on accounts without a designated test cluster.
func testCluster(t *testing.T) string {
	t.Helper()
	name := os.Getenv("REFRESH_TEST_CLUSTER")
	if name == "" {
		t.Skip("set REFRESH_TEST_CLUSTER to a disposable cluster to run this test")
	}
	return name
}

// TestIntegrationClusterListAllRegions exercises multi-region cluster discovery
// (the `cluster list -A` path) against the live account and asserts the
// summaries come back coherent.
func TestIntegrationClusterListAllRegions(t *testing.T) {
	requireIntegration(t)
	cfg := loadConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	svc := clustersvc.NewService(cfg, nil, nil)
	summaries, regions, err := svc.ListAllRegionsWithMeta(ctx, clustersvc.ListOptions{AllRegions: true})
	if err != nil {
		t.Fatalf("ListAllRegionsWithMeta: %v", err)
	}
	if regions <= 0 {
		t.Fatalf("expected at least one region queried, got %d", regions)
	}
	for _, s := range summaries {
		if s.Name == "" {
			t.Errorf("cluster summary missing a name: %+v", s)
		}
		if s.Region == "" {
			t.Errorf("cluster %q missing a region after multi-region relabel", s.Name)
		}
	}
}

// TestIntegrationAddonListDescribe lists addons for the test cluster and, for
// the first one found, describes it, asserting the describe round-trips the
// addon name.
func TestIntegrationAddonListDescribe(t *testing.T) {
	requireIntegration(t)
	cluster := testCluster(t)
	cfg := loadConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	svc := factory.NewAddonService(cfg, nil)
	addons, err := svc.List(ctx, cluster, addonsvc.ListOptions{})
	if err != nil {
		t.Fatalf("addon List: %v", err)
	}
	if len(addons) == 0 {
		t.Skipf("cluster %q has no addons to describe", cluster)
	}

	first := addons[0].Name
	details, err := svc.Describe(ctx, cluster, first, addonsvc.DescribeOptions{})
	if err != nil {
		t.Fatalf("addon Describe(%q): %v", first, err)
	}
	if details.Name != first {
		t.Errorf("Describe returned name %q, want %q", details.Name, first)
	}
}

// TestIntegrationNodegroupScaleDryRun verifies the scale dry-run path is a true
// no-op against the live account: it lists nodegroups, then issues a dry-run
// scale that must return without error and without mutating anything.
func TestIntegrationNodegroupScaleDryRun(t *testing.T) {
	requireIntegration(t)
	cluster := testCluster(t)
	cfg := loadConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	svc := nodegroupsvc.NewService(cfg, nil, nil)
	groups, err := svc.List(ctx, cluster, nodegroupsvc.ListOptions{})
	if err != nil {
		t.Fatalf("nodegroup List: %v", err)
	}
	if len(groups) == 0 {
		t.Skipf("cluster %q has no nodegroups to dry-run scale", cluster)
	}

	target := groups[0].Name
	desired := groups[0].DesiredSize // scale to the current size — a true no-op even if --dry-run regressed
	if err := svc.Scale(ctx, cluster, target, aws.Int32(desired), nil, nil, nodegroupsvc.ScaleOptions{DryRun: true}); err != nil {
		t.Fatalf("dry-run Scale(%q): %v", target, err)
	}
}
