package cluster

import (
	"context"
	"strings"
	"testing"
	"time"

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

func TestDescribeInsight_Deprecations(t *testing.T) {
	lastNewRelic := time.Date(2026, 6, 14, 9, 30, 0, 0, time.UTC)
	lastKubectl := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	var gotCluster, gotID string
	mock := &mocks.EKSAPI{
		DescribeInsightFn: func(_ context.Context, in *eks.DescribeInsightInput, _ ...func(*eks.Options)) (*eks.DescribeInsightOutput, error) {
			gotCluster = aws.ToString(in.ClusterName)
			gotID = aws.ToString(in.Id)
			return &eks.DescribeInsightOutput{Insight: &ekstypes.Insight{
				Id:                aws.String("dep-1"),
				Name:              aws.String("Deprecated APIs removed in 1.32"),
				Category:          ekstypes.CategoryUpgradeReadiness,
				KubernetesVersion: aws.String("1.32"),
				InsightStatus:     &ekstypes.InsightStatus{Status: ekstypes.InsightStatusValueError, Reason: aws.String("1 deprecated API in use")},
				Recommendation:    aws.String("Migrate clients off policy/v1beta1 PodDisruptionBudget."),
				CategorySpecificSummary: &ekstypes.InsightCategorySpecificSummary{
					DeprecationDetails: []ekstypes.DeprecationDetail{{
						Usage:              aws.String("policy/v1beta1 PodDisruptionBudget"),
						ReplacedWith:       aws.String("policy/v1 PodDisruptionBudget"),
						StopServingVersion: aws.String("1.25"),
						// Intentionally out of order — service must sort most-active first.
						ClientStats: []ekstypes.ClientStat{
							{UserAgent: aws.String("kubectl/v1.29.0"), LastRequestTime: aws.Time(lastKubectl), NumberOfRequestsLast30Days: 12},
							{UserAgent: aws.String("newrelic-kube-state-metric/v2"), LastRequestTime: aws.Time(lastNewRelic), NumberOfRequestsLast30Days: 412},
						},
					}},
				},
			}}, nil
		},
	}

	svc := &ServiceImpl{eksClient: mock}
	detail, err := svc.DescribeInsight(context.Background(), "prod", "dep-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Request passthrough.
	if gotCluster != "prod" || gotID != "dep-1" {
		t.Errorf("DescribeInsight called with cluster=%q id=%q, want prod/dep-1", gotCluster, gotID)
	}

	// Summary fields still map.
	if detail.Status != InsightStatusError || detail.Recommendation == "" {
		t.Errorf("summary mapping lost: status=%q recommendation=%q", detail.Status, detail.Recommendation)
	}

	if len(detail.Deprecations) != 1 {
		t.Fatalf("got %d deprecations, want 1", len(detail.Deprecations))
	}
	d := detail.Deprecations[0]
	if d.Usage != "policy/v1beta1 PodDisruptionBudget" || d.ReplacedWith != "policy/v1 PodDisruptionBudget" || d.StopServingVersion != "1.25" {
		t.Errorf("deprecation fields = %+v", d)
	}
	if len(d.ClientStats) != 2 {
		t.Fatalf("got %d client stats, want 2", len(d.ClientStats))
	}
	// Most-active caller first (412 > 12).
	if d.ClientStats[0].UserAgent != "newrelic-kube-state-metric/v2" || d.ClientStats[0].NumberOfRequestsLast30Days != 412 {
		t.Errorf("client stats not sorted most-active-first: %+v", d.ClientStats)
	}
	if d.ClientStats[0].LastRequestTime == nil || !d.ClientStats[0].LastRequestTime.Equal(lastNewRelic) {
		t.Errorf("LastRequestTime not preserved: %v", d.ClientStats[0].LastRequestTime)
	}
	if d.ClientStats[1].UserAgent != "kubectl/v1.29.0" || d.ClientStats[1].NumberOfRequestsLast30Days != 12 {
		t.Errorf("second client stat = %+v", d.ClientStats[1])
	}
}

func TestResolveInsightID(t *testing.T) {
	mock := &mocks.EKSAPI{
		ListInsightsFn: func(_ context.Context, _ *eks.ListInsightsInput, _ ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
			return &eks.ListInsightsOutput{Insights: []ekstypes.InsightSummary{
				{Id: aws.String("bc8b2f86-aaaa"), Name: aws.String("Amazon Linux 2 compatibility"), Category: ekstypes.CategoryUpgradeReadiness, InsightStatus: &ekstypes.InsightStatus{Status: ekstypes.InsightStatusValuePassing}},
				{Id: aws.String("d52457c8-bbbb"), Name: aws.String("Kubelet version skew"), Category: ekstypes.CategoryUpgradeReadiness, InsightStatus: &ekstypes.InsightStatus{Status: ekstypes.InsightStatusValuePassing}},
				{Id: aws.String("0e6b6f8f-cccc"), Name: aws.String("kube-proxy version skew"), Category: ekstypes.CategoryUpgradeReadiness, InsightStatus: &ekstypes.InsightStatus{Status: ekstypes.InsightStatusValuePassing}},
				{Id: aws.String("dep12345-dddd"), Name: aws.String("Deprecated APIs removed in 1.33"), Category: ekstypes.CategoryUpgradeReadiness, InsightStatus: &ekstypes.InsightStatus{Status: ekstypes.InsightStatusValueError}},
			}}, nil
		},
	}
	svc := &ServiceImpl{eksClient: mock}
	ctx := context.Background()

	cases := []struct{ query, want string }{
		{"bc8b2f86-aaaa", "bc8b2f86-aaaa"}, // exact ID
		{"d52457c8", "d52457c8-bbbb"},      // ID prefix
		{"kubelet", "d52457c8-bbbb"},       // name substring (case-insensitive)
		{"deprecated", "dep12345-dddd"},    // name substring
		{"AMAZON LINUX", "bc8b2f86-aaaa"},  // case-insensitive name
	}
	for _, c := range cases {
		got, err := svc.ResolveInsightID(ctx, "prod", "", c.query)
		if err != nil {
			t.Errorf("ResolveInsightID(%q): unexpected error: %v", c.query, err)
			continue
		}
		if got != c.want {
			t.Errorf("ResolveInsightID(%q) = %q, want %q", c.query, got, c.want)
		}
	}

	// Ambiguous: "skew" matches both version-skew insights → error naming candidates.
	if _, err := svc.ResolveInsightID(ctx, "prod", "", "skew"); err == nil || !strings.Contains(err.Error(), "matches 2 insights") {
		t.Errorf("ambiguous query should error with candidates, got %v", err)
	}
	// No match.
	if _, err := svc.ResolveInsightID(ctx, "prod", "", "nonexistent-xyz"); err == nil || !strings.Contains(err.Error(), "no insight matches") {
		t.Errorf("no-match query should error, got %v", err)
	}
}

func TestUpgradeCheck_Skew(t *testing.T) {
	mock := &mocks.EKSAPI{
		DescribeClusterFn: func(_ context.Context, _ *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
			return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{Name: aws.String("prod"), Version: aws.String("1.31"),
				UpgradePolicy: &ekstypes.UpgradePolicyResponse{SupportType: ekstypes.SupportTypeStandard}}}, nil
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
	if report.SupportType != "STANDARD" {
		t.Errorf("support type = %q, want STANDARD", report.SupportType)
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

// --status PASSING asks for PASSING insights, so it must not need
// --show-passing as well (REF-168).
func TestListInsights_StatusPassingShowsPassing(t *testing.T) {
	api := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithInsight("prod", "Kubelet version skew", ekstypes.InsightStatusValuePassing, "1.33").
		WithInsight("prod", "Deprecated APIs", ekstypes.InsightStatusValueError, "1.33").
		Build()
	svc := &ServiceImpl{eksClient: api}

	got, err := svc.ListInsights(context.Background(), "prod", UpgradeCheckOptions{Statuses: []string{"passing"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != InsightStatusPassing {
		t.Fatalf("--status PASSING returned %+v, want the one PASSING insight", got)
	}

	// Without the status filter, PASSING stays hidden.
	got, err = svc.ListInsights(context.Background(), "prod", UpgradeCheckOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != InsightStatusError {
		t.Fatalf("default returned %+v, want only the ERROR insight", got)
	}
}

// --id resolves within --category: a MISCONFIGURATION insight is found when
// that category is asked for (REF-168).
func TestResolveInsightID_UsesCategory(t *testing.T) {
	mock := &mocks.EKSAPI{
		ListInsightsFn: func(_ context.Context, in *eks.ListInsightsInput, _ ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
			if in.Filter == nil || len(in.Filter.Categories) != 1 || in.Filter.Categories[0] != ekstypes.CategoryMisconfiguration {
				return &eks.ListInsightsOutput{}, nil
			}
			return &eks.ListInsightsOutput{Insights: []ekstypes.InsightSummary{
				{Id: aws.String("mis12345-eeee"), Name: aws.String("Cluster security group rules"), Category: ekstypes.CategoryMisconfiguration, InsightStatus: &ekstypes.InsightStatus{Status: ekstypes.InsightStatusValueWarning}},
			}}, nil
		},
	}
	svc := &ServiceImpl{eksClient: mock}

	got, err := svc.ResolveInsightID(context.Background(), "prod", "MISCONFIGURATION", "security group")
	if err != nil {
		t.Fatalf("ResolveInsightID in MISCONFIGURATION: %v", err)
	}
	if got != "mis12345-eeee" {
		t.Errorf("ResolveInsightID = %q, want mis12345-eeee", got)
	}
	if _, err := svc.ResolveInsightID(context.Background(), "prod", "", "security group"); err == nil {
		t.Error("the default category should not find a MISCONFIGURATION insight")
	}
}

// A cluster EKS has not evaluated yet has no upgrade-readiness insights at
// all (seen on a real 20-minute-old cluster). That is not "nothing to
// address": the report says so and the verdict is REVIEW, not READY. Only
// PASSING insights, or a status filter, is a real empty list.
func TestUpgradeCheck_InsightsNotEvaluated(t *testing.T) {
	for _, tc := range []struct {
		name     string
		insights []ekstypes.InsightSummary
		opts     UpgradeCheckOptions
		want     bool
	}{
		{name: "none returned", want: true},
		{name: "only passing", insights: []ekstypes.InsightSummary{{Name: aws.String("Kubelet skew"),
			InsightStatus: &ekstypes.InsightStatus{Status: ekstypes.InsightStatusValuePassing}}}},
		{name: "status filter", opts: UpgradeCheckOptions{Statuses: []string{"ERROR"}}},
		{name: "other category", opts: UpgradeCheckOptions{Category: "MISCONFIGURATION"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := mocks.NewEKSAPI().WithCluster("prod", "1.34").Build()
			m.ListInsightsFn = func(context.Context, *eks.ListInsightsInput, ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
				return &eks.ListInsightsOutput{Insights: tc.insights}, nil
			}
			r, err := (&ServiceImpl{eksClient: m}).UpgradeCheck(context.Background(), "prod", tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if r.InsightsNotEvaluated != tc.want || len(r.Insights) != 0 {
				t.Fatalf("notEvaluated = %v, insights %v", r.InsightsNotEvaluated, r.Insights)
			}
			level, why := r.Readiness()
			if tc.want && (level != ReadinessReview || !strings.Contains(strings.Join(why, ";"), "not evaluated")) {
				t.Errorf("readiness = %v %v, want REVIEW", level, why)
			}
			if !tc.want && level != ReadinessReady {
				t.Errorf("readiness = %v %v, want READY", level, why)
			}
		})
	}
}
