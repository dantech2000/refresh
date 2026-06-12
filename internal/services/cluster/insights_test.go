package cluster

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

func TestListInsights_MappingFilterPagination(t *testing.T) {
	var gotCategories []ekstypes.Category
	var gotStatuses []ekstypes.InsightStatusValue

	mock := &mocks.EKSAPI{
		ListInsightsFn: func(_ context.Context, in *eks.ListInsightsInput, _ ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
			if in.Filter != nil {
				gotCategories = in.Filter.Categories
				gotStatuses = in.Filter.Statuses
			}
			// Two pages, to exercise pagination.
			if in.NextToken == nil {
				return &eks.ListInsightsOutput{
					Insights: []ekstypes.InsightSummary{{
						Id:                aws.String("i-1"),
						Name:              aws.String("Deprecated APIs"),
						Category:          ekstypes.CategoryUpgradeReadiness,
						KubernetesVersion: aws.String("1.31"),
						InsightStatus:     &ekstypes.InsightStatus{Status: ekstypes.InsightStatusValueWarning, Reason: aws.String("3 APIs in use")},
					}},
					NextToken: aws.String("page2"),
				}, nil
			}
			return &eks.ListInsightsOutput{
				Insights: []ekstypes.InsightSummary{
					{Id: aws.String("i-2"), Name: aws.String("Healthy check"), Category: ekstypes.CategoryUpgradeReadiness, InsightStatus: &ekstypes.InsightStatus{Status: ekstypes.InsightStatusValuePassing}},
				},
			}, nil
		},
	}

	svc := &ServiceImpl{eksClient: mock}

	// Default: PASSING hidden → only the WARNING insight.
	got, err := svc.ListInsights(context.Background(), "prod", UpgradeCheckOptions{Statuses: []string{"warning"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d insights, want 1 (PASSING hidden)", len(got))
	}
	if got[0].Status != InsightStatusWarning || got[0].StatusReason != "3 APIs in use" {
		t.Errorf("flattened status = %q/%q, want WARNING/3 APIs in use", got[0].Status, got[0].StatusReason)
	}
	if len(gotCategories) != 1 || gotCategories[0] != ekstypes.CategoryUpgradeReadiness {
		t.Errorf("category filter = %v, want [UPGRADE_READINESS]", gotCategories)
	}
	if len(gotStatuses) != 1 || gotStatuses[0] != ekstypes.InsightStatusValueWarning {
		t.Errorf("status filter passthrough = %v, want [WARNING]", gotStatuses)
	}

	// ShowPassing: both insights, across both pages.
	all, err := svc.ListInsights(context.Background(), "prod", UpgradeCheckOptions{ShowPassing: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ShowPassing returned %d, want 2 (pagination + passing)", len(all))
	}
}

func TestUpgradeCheck_Skew(t *testing.T) {
	mock := &mocks.EKSAPI{
		DescribeClusterFn: func(_ context.Context, _ *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
			return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{Name: aws.String("prod"), Version: aws.String("1.31")}}, nil
		},
		ListInsightsFn: func(_ context.Context, _ *eks.ListInsightsInput, _ ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
			return &eks.ListInsightsOutput{}, nil
		},
		ListNodegroupsFn: func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return &eks.ListNodegroupsOutput{Nodegroups: []string{"ng-old"}}, nil
		},
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{Version: aws.String("1.29")}}, nil
		},
		ListAddonsFn: func(_ context.Context, _ *eks.ListAddonsInput, _ ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
			return &eks.ListAddonsOutput{Addons: []string{"vpc-cni"}}, nil
		},
		DescribeAddonFn: func(_ context.Context, _ *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
			return &eks.DescribeAddonOutput{Addon: &ekstypes.Addon{AddonName: aws.String("vpc-cni"), AddonVersion: aws.String("v1.10.0")}}, nil
		},
		DescribeAddonVersionsFn: func(_ context.Context, _ *eks.DescribeAddonVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
			return &eks.DescribeAddonVersionsOutput{Addons: []ekstypes.AddonInfo{{
				AddonVersions: []ekstypes.AddonVersionInfo{{AddonVersion: aws.String("v1.18.1")}, {AddonVersion: aws.String("v1.10.0")}},
			}}}, nil
		},
	}

	svc := &ServiceImpl{eksClient: mock}
	report, err := svc.UpgradeCheck(context.Background(), "prod", UpgradeCheckOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Skew.ControlPlaneVersion != "1.31" {
		t.Errorf("control plane = %q, want 1.31", report.Skew.ControlPlaneVersion)
	}
	if len(report.Skew.Nodegroups) != 1 || report.Skew.Nodegroups[0].MinorsBehind != 2 {
		t.Fatalf("nodegroup skew = %+v, want 1 ng at 2 minors behind", report.Skew.Nodegroups)
	}
	if report.Skew.Nodegroups[0].Blocking {
		t.Error("2 minors behind should not be blocking (limit is 3)")
	}
	if len(report.Skew.Addons) != 1 || !report.Skew.Addons[0].Behind {
		t.Fatalf("addon skew = %+v, want vpc-cni behind", report.Skew.Addons)
	}
	if report.Skew.Addons[0].Latest != "v1.18.1" {
		t.Errorf("latest addon = %q, want v1.18.1", report.Skew.Addons[0].Latest)
	}
	// Findings: a lagging nodegroup and a behind addon.
	if len(report.Skew.Findings) != 2 {
		t.Errorf("findings = %v, want 2", report.Skew.Findings)
	}
}
