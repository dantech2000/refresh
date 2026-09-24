package addons

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/diag"
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

	if issues, err := svc.postUpdateHealthCheck(context.Background(), "cluster", "vpc-cni"); err != nil || issues != "" {
		t.Fatalf("expected no issues for a healthy ACTIVE addon, got: %q, %v", issues, err)
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

	issues, err := svc.postUpdateHealthCheck(context.Background(), "cluster", "vpc-cni")
	if err != nil || issues == "" {
		t.Fatalf("expected issues (not a read error) for DEGRADED status, got %q, %v", issues, err)
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

	issues, err := svc.postUpdateHealthCheck(context.Background(), "cluster", "vpc-cni")
	if err != nil || issues == "" {
		t.Fatalf("expected issues for an ACTIVE addon with health issues, got %q, %v", issues, err)
	}
}

// A post-update DescribeAddon that fails is a read failure (the add-on's
// health is unknown), not a health issue: Update reports Unverified with a
// failure that names eks:DescribeAddon.
func TestUpdate_PostUpdateReadFailureIsUnverified(t *testing.T) {
	calls := 0
	m := waitMock("v1.18.0", ekstypes.AddonStatusActive, "v1.19.0").
		WithUpdateStatuses("u-1", ekstypes.UpdateStatusSuccessful).
		Build()
	mocks.SettleAddonUpdates(m)
	describe := m.DescribeAddonFn
	m.DescribeAddonFn = func(ctx context.Context, in *eks.DescribeAddonInput, opts ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
		calls++
		// The current-version read, the version confirmation, then the
		// post-update health check, which fails.
		if calls >= 3 {
			return nil, mocks.AccessDenied()
		}
		return describe(ctx, in, opts...)
	}
	res, err := NewService(m, logger()).Update(t.Context(), "prod", "vpc-cni", waitOpts("latest"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.Status != StatusUnverified || res.HealthIssues != "" {
		t.Fatalf("result = %+v, want Unverified with no health issues", res)
	}
	f := res.Failure
	if f == nil || f.Kind != diag.KindAddon || f.Name != "vpc-cni" || f.Cluster != "prod" ||
		f.Operation != diag.OpDescribeAddon || f.Reason != diag.ReasonAccessDenied || f.UpdateID != "u-1" {
		t.Errorf("failure = %+v, want an AccessDenied eks:DescribeAddon failure for vpc-cni", f)
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
	if result.Status != StatusDryRun {
		t.Errorf("status = %s, want DRY_RUN", result.Status)
	}
	if m.Calls.UpdateAddon != 0 {
		t.Errorf("UpdateAddon called %d times during dry-run, want 0", m.Calls.UpdateAddon)
	}
}

// An empty DescribeAddon response is an error, not a nil dereference.
func TestDescribeAddonEmptyResponse_NoPanic(t *testing.T) {
	m := mocks.NewEKSAPI().Build()
	m.DescribeAddonFn = func(_ context.Context, _ *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
		return &eks.DescribeAddonOutput{}, nil
	}
	svc := NewService(m, logger())
	ctx := context.Background()

	if _, err := svc.Describe(ctx, "cluster", "vpc-cni", DescribeOptions{}); err == nil {
		t.Error("Describe: want an error for an empty response")
	}
	if err := svc.preUpdateHealthCheck(ctx, "cluster", "vpc-cni"); err == nil {
		t.Error("preUpdateHealthCheck: want an error for an empty response")
	}
	if _, err := svc.postUpdateHealthCheck(ctx, "cluster", "vpc-cni"); err == nil {
		t.Error("postUpdateHealthCheck: want an error for an empty response")
	}
}

// A DELETING add-on cannot be updated: the gate blocks it.
func TestPreUpdateHealthCheck_Deleting_Blocked(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithAddon("vpc-cni", "v1.14.0", ekstypes.AddonStatusDeleting).
		Build()
	err := NewService(m, logger()).preUpdateHealthCheck(context.Background(), "cluster", "vpc-cni")
	if err == nil || !strings.Contains(err.Error(), "DELETING") {
		t.Fatalf("err = %v, want a block that names DELETING", err)
	}
}

// With the health check on, an update of a DELETING add-on never calls
// UpdateAddon.
func TestUpdate_HealthCheckBlocks_WhenDeleting(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("cluster", "1.32").
		WithAddon("vpc-cni", "v1.14.0-eksbuild.1", ekstypes.AddonStatusDeleting).
		WithAddonVersions("vpc-cni", []string{"v1.15.0-eksbuild.1", "v1.14.0-eksbuild.1"}, "1.32").
		WithUpdateAddon("u-1").
		Build()
	_, err := NewService(m, logger()).Update(context.Background(), "cluster", "vpc-cni", UpdateOptions{Version: "latest", HealthCheck: true})
	if err == nil {
		t.Fatal("expected the pre-update health check to block a DELETING add-on")
	}
	if n := m.Calls.UpdateAddon; n != 0 {
		t.Fatalf("UpdateAddon called %d times, want 0", n)
	}
}

// The pre-update health check formats an AWS error: a missing permission
// names the IAM action instead of the raw SDK text.
func TestPreUpdateHealthCheck_AccessDeniedIsFormatted(t *testing.T) {
	m := mocks.NewEKSAPI().Build()
	m.DescribeAddonFn = func(context.Context, *eks.DescribeAddonInput, ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
		return nil, mocks.AccessDenied()
	}
	err := NewService(m, logger()).preUpdateHealthCheck(context.Background(), "cluster", "vpc-cni")
	if err == nil || !strings.Contains(err.Error(), "eks:DescribeAddon") {
		t.Fatalf("err = %v, want a formatted permission error that names eks:DescribeAddon", err)
	}
}

// An incompatible pinned version names the versions the cluster supports,
// not a flag that does not exist.
func TestValidateVersionCompatibility_ListsSupportedVersions(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithAddonVersions("coredns", []string{"v1.11.1-eksbuild.9", "v1.11.4-eksbuild.2"}, "1.32").
		Build()
	err := NewService(m, logger()).validateVersionCompatibility(context.Background(), "1.32", "coredns", "v1.11.1")
	if err == nil {
		t.Fatal("expected an incompatibility error")
	}
	msg := err.Error()
	if strings.Contains(msg, "--show-versions") {
		t.Errorf("error points to the nonexistent --show-versions flag: %s", msg)
	}
	if !strings.Contains(msg, "v1.11.4-eksbuild.2, v1.11.1-eksbuild.9") {
		t.Errorf("error does not list the supported versions newest first: %s", msg)
	}
}
