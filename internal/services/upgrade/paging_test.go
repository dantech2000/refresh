package upgrade

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// With one item per page, the blocking insight sits on the last page. The
// plan must still see it: listing stops only when NextToken runs out.
func TestBuildPlan_InsightsFollowNextToken(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithPageSize(1).
		WithCluster("prod-east", "1.31").
		WithAddon("vpc-cni", "v1.31.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.31.0-eksbuild.1", "v1.33.0-eksbuild.1"}, "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("workers-b", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithInsight("prod-east", "Kubelet version skew", ekstypes.InsightStatusValuePassing, "1.32").
		WithInsight("prod-east", "Cluster health issues", ekstypes.InsightStatusValuePassing, "1.32").
		WithInsight("prod-east", "Deprecated APIs removed in 1.32", ekstypes.InsightStatusValueError, "1.32").
		Build()

	plan, err := newTestService(m).BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	blockers := strings.Join(plan.Blockers(), "\n")
	if !strings.Contains(blockers, "Deprecated APIs") {
		t.Errorf("blockers = %q, want the ERROR insight from the last page", blockers)
	}
	if m.Calls.ListInsights < 3 {
		t.Errorf("ListInsights calls = %d, want every page read", m.Calls.ListInsights)
	}
}

// A nodegroup beyond the kubelet skew blocks the plan even when it is on the
// last page of ListNodegroups.
func TestBuildPlan_NodegroupsFollowNextToken(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithPageSize(1).
		WithCluster("prod-east", "1.31").
		WithAddon("vpc-cni", "v1.31.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.31.0-eksbuild.1", "v1.33.0-eksbuild.1"}, "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("workers-b", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("ancient", "1.27", ekstypes.AMITypesAl2023X8664Standard).
		Build()

	plan, err := newTestService(m).BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if blockers := strings.Join(plan.Blockers(), "\n"); !strings.Contains(blockers, "ancient") {
		t.Fatalf("blockers = %q, want the lagging nodegroup from the last page", blockers)
	}
}

// The nodegroup phase rolls every nodegroup, not just the first page.
func TestUpgradeNodegroups_FollowsNextToken(t *testing.T) {
	names := []string{"workers-a", "workers-b", "workers-c"}
	b := mocks.NewEKSAPI().WithPageSize(1).WithCluster("prod-east", "1.32")
	for _, n := range names {
		b.WithNodegroup(n, "1.31", ekstypes.AMITypesAl2023X8664Standard).
			WithUpdateStatuses("u-"+n, ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusSuccessful)
	}
	m := b.Build()
	rolls := captureNodegroupRolls(m)

	if err := newTestService(m).UpgradeNodegroups(context.Background(), "prod-east", "1.32", NodegroupRollOptions{}, nil); err != nil {
		t.Fatalf("UpgradeNodegroups: %v", err)
	}
	if len(*rolls) != len(names) {
		t.Fatalf("rolls = %d, want %d", len(*rolls), len(names))
	}
	for i, want := range names {
		if got := aws.ToString((*rolls)[i].NodegroupName); got != want {
			t.Fatalf("roll %d = %s, want %s", i, got, want)
		}
	}
}

// A roll whose update ends FAILED stops the phase with an error naming the
// nodegroup, and nothing after it is rolled.
func TestUpgradeNodegroups_FailedUpdateStops(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("workers-b", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithUpdateStatuses("u-workers-a", ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusFailed).
		WithUpdateStatuses("u-workers-b", ekstypes.UpdateStatusSuccessful).
		Build()
	rolls := captureNodegroupRolls(m)

	err := newTestService(m).UpgradeNodegroups(context.Background(), "prod-east", "1.32", NodegroupRollOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), "workers-a") {
		t.Fatalf("err = %v, want a failure naming workers-a", err)
	}
	if len(*rolls) != 1 {
		t.Fatalf("rolls = %d, want 1 (stop after the failed roll)", len(*rolls))
	}
}
