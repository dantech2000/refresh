package addons

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

func logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// ---- preUpdateHealthCheck ----

func TestPreUpdateHealthCheck_Active(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithAddon("vpc-cni", "v1.14.0", ekstypes.AddonStatusActive).
		Build()
	svc := NewService(m, logger())

	if err := svc.preUpdateHealthCheck(context.Background(), "cluster", "vpc-cni"); err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
}

func TestPreUpdateHealthCheck_Creating_Blocked(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithAddon("vpc-cni", "v1.14.0", ekstypes.AddonStatusCreating).
		Build()
	svc := NewService(m, logger())

	err := svc.preUpdateHealthCheck(context.Background(), "cluster", "vpc-cni")
	if err == nil {
		t.Fatal("expected error for CREATING state, got nil")
	}
}

func TestPreUpdateHealthCheck_Updating_Blocked(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithAddon("vpc-cni", "v1.14.0", ekstypes.AddonStatusUpdating).
		Build()
	svc := NewService(m, logger())

	err := svc.preUpdateHealthCheck(context.Background(), "cluster", "vpc-cni")
	if err == nil {
		t.Fatal("expected error for UPDATING state, got nil")
	}
}

func TestPreUpdateHealthCheck_Degraded_Allowed(t *testing.T) {
	// Users must be able to update a degraded addon to remediate it.
	m := mocks.NewEKSAPI().
		WithAddon("vpc-cni", "v1.14.0", ekstypes.AddonStatusDegraded).
		Build()
	svc := NewService(m, logger())

	if err := svc.preUpdateHealthCheck(context.Background(), "cluster", "vpc-cni"); err != nil {
		t.Fatalf("DEGRADED should be allowed, got: %v", err)
	}
}

func TestPreUpdateHealthCheck_APIError_Propagated(t *testing.T) {
	apiErr := errors.New("network timeout")
	m := &mocks.EKSAPI{
		DescribeAddonFn: func(_ context.Context, _ *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
			return nil, apiErr
		},
	}
	svc := NewService(m, logger())

	err := svc.preUpdateHealthCheck(context.Background(), "cluster", "vpc-cni")
	if err == nil {
		t.Fatal("expected error from DescribeAddon failure")
	}
}

// ---- validateVersionCompatibility ----

func TestValidateVersionCompatibility_CompatibleVersion(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithAddonVersions("vpc-cni", []string{"v1.15.0", "v1.14.0"}, "1.28").
		Build()
	svc := NewService(m, logger())

	if err := svc.validateVersionCompatibility(context.Background(), "1.28", "vpc-cni", "v1.15.0"); err != nil {
		t.Fatalf("expected nil for compatible version, got: %v", err)
	}
}

func TestValidateVersionCompatibility_IncompatibleVersion(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithAddonVersions("vpc-cni", []string{"v1.15.0", "v1.14.0"}, "1.28").
		Build()
	svc := NewService(m, logger())

	err := svc.validateVersionCompatibility(context.Background(), "1.28", "vpc-cni", "v1.99.0")
	if err == nil {
		t.Fatal("expected incompatibility error, got nil")
	}
}

func TestValidateVersionCompatibility_ClusterDescribeError_Skips(t *testing.T) {
	// A network error describing the cluster must not block the update:
	// clusterK8sVersion degrades to "" and validation is skipped.
	m := &mocks.EKSAPI{
		DescribeClusterFn: func(_ context.Context, _ *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
			return nil, fmt.Errorf("simulated describe cluster error")
		},
	}
	svc := NewService(m, logger())

	if v := svc.clusterK8sVersion(context.Background(), "cluster"); v != "" {
		t.Fatalf("clusterK8sVersion on API error = %q, want empty", v)
	}
	if err := svc.validateVersionCompatibility(context.Background(), "", "vpc-cni", "v1.15.0"); err != nil {
		t.Fatalf("unknown cluster version should be skipped gracefully, got: %v", err)
	}
}

func TestClusterK8sVersion_Memoized(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("cluster", "1.28").
		Build()
	svc := NewService(m, logger())

	if v := svc.clusterK8sVersion(context.Background(), "cluster"); v != "1.28" {
		t.Fatalf("clusterK8sVersion = %q, want 1.28", v)
	}
	if v := svc.clusterK8sVersion(context.Background(), "cluster"); v != "1.28" {
		t.Fatalf("memoized clusterK8sVersion = %q, want 1.28", v)
	}
	if m.Calls.DescribeCluster != 1 {
		t.Errorf("DescribeCluster called %d times, want 1 (memoized)", m.Calls.DescribeCluster)
	}
}

func TestValidateVersionCompatibility_VersionsAPIError_Skips(t *testing.T) {
	m := &mocks.EKSAPI{
		DescribeAddonVersionsFn: func(_ context.Context, _ *eks.DescribeAddonVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
			return nil, fmt.Errorf("simulated versions API error")
		},
	}
	svc := NewService(m, logger())

	if err := svc.validateVersionCompatibility(context.Background(), "1.28", "vpc-cni", "v1.15.0"); err != nil {
		t.Fatalf("versions API error should be skipped gracefully, got: %v", err)
	}
}

// ---- postUpdateHealthCheck ----

func TestPostUpdateHealthCheck_Active_NoIssues(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithAddon("vpc-cni", "v1.15.0", ekstypes.AddonStatusActive).
		Build()
	svc := NewService(m, logger())

	if err := svc.postUpdateHealthCheck(context.Background(), "cluster", "vpc-cni"); err != nil {
		t.Fatalf("expected nil for healthy ACTIVE addon, got: %v", err)
	}
}

func TestPostUpdateHealthCheck_NotActive_ReturnsError(t *testing.T) {
	m := &mocks.EKSAPI{
		DescribeAddonFn: func(_ context.Context, _ *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
			return &eks.DescribeAddonOutput{
				Addon: &ekstypes.Addon{
					AddonName:    aws.String("vpc-cni"),
					AddonVersion: aws.String("v1.15.0"),
					Status:       ekstypes.AddonStatusDegraded,
				},
			}, nil
		},
	}
	svc := NewService(m, logger())

	err := svc.postUpdateHealthCheck(context.Background(), "cluster", "vpc-cni")
	if err == nil {
		t.Fatal("expected error for DEGRADED status, got nil")
	}
}

func TestPostUpdateHealthCheck_ActiveWithIssues_ReturnsError(t *testing.T) {
	m := &mocks.EKSAPI{
		DescribeAddonFn: func(_ context.Context, _ *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
			return &eks.DescribeAddonOutput{
				Addon: &ekstypes.Addon{
					AddonName:    aws.String("vpc-cni"),
					AddonVersion: aws.String("v1.15.0"),
					Status:       ekstypes.AddonStatusActive,
					Health: &ekstypes.AddonHealth{
						Issues: []ekstypes.AddonIssue{
							{Code: "ConfigurationConflict", Message: aws.String("conflict detected")},
						},
					},
				},
			}, nil
		},
	}
	svc := NewService(m, logger())

	err := svc.postUpdateHealthCheck(context.Background(), "cluster", "vpc-cni")
	if err == nil {
		t.Fatal("expected error for ACTIVE addon with health issues, got nil")
	}
}

// ---- Update with health-check ----

func TestUpdate_HealthCheckBlocks_WhenUpdating(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("cluster", "1.28").
		WithAddon("vpc-cni", "v1.14.0", ekstypes.AddonStatusUpdating).
		WithAddonVersions("vpc-cni", []string{"v1.15.0", "v1.14.0"}, "1.28").
		Build()
	svc := NewService(m, logger())

	_, err := svc.Update(context.Background(), "cluster", "vpc-cni", UpdateOptions{
		Version:     "v1.15.0",
		HealthCheck: true,
	})
	if err == nil {
		t.Fatal("expected health check to block update while addon is UPDATING")
	}
}

func TestUpdate_DryRun_DoesNotCallUpdateAddon(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("cluster", "1.28").
		WithAddon("vpc-cni", "v1.14.0", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.15.0", "v1.14.0"}, "1.28").
		Build()
	svc := NewService(m, logger())

	result, err := svc.Update(context.Background(), "cluster", "vpc-cni", UpdateOptions{
		Version: "v1.15.0",
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "DRY_RUN" {
		t.Errorf("status = %s, want DRY_RUN", result.Status)
	}
	if m.Calls.UpdateAddon != 0 {
		t.Errorf("UpdateAddon called %d times during dry-run, want 0", m.Calls.UpdateAddon)
	}
}
