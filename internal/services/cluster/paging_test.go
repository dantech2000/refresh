package cluster

import (
	"context"
	"slices"
	"testing"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// UpgradeCheck lists insights, nodegroups, addons and addon versions. With
// one item per page, every list must still come back whole, and the latest
// addon version (listed last, so on the last page) must be the one compared.
func TestUpgradeCheck_FollowsNextToken(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithPageSize(1).
		WithCluster("prod", "1.32").
		WithNodegroup("ng-a", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("ng-b", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("ng-c", "1.29", ekstypes.AMITypesAl2023X8664Standard).
		WithAddon("vpc-cni", "v1.18.0", ekstypes.AddonStatusActive).
		WithAddon("coredns", "v1.11.4", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.17.0", "v1.18.0", "v1.19.2"}, "1.32").
		WithAddonVersions("coredns", []string{"v1.11.1", "v1.11.4"}, "1.32").
		WithInsight("prod", "Deprecated APIs", ekstypes.InsightStatusValueWarning, "1.33").
		WithInsight("prod", "Kubelet skew", ekstypes.InsightStatusValueError, "1.33").
		Build()
	svc := &ServiceImpl{eksClient: m}

	report, err := svc.UpgradeCheck(context.Background(), "prod", UpgradeCheckOptions{})
	if err != nil {
		t.Fatalf("UpgradeCheck: %v", err)
	}

	var insights []string
	for _, i := range report.Insights {
		insights = append(insights, i.Name)
	}
	slices.Sort(insights)
	if !slices.Equal(insights, []string{"Deprecated APIs", "Kubelet skew"}) {
		t.Errorf("insights = %v, want both pages", insights)
	}

	var ngs []string
	for _, ng := range report.Skew.Nodegroups {
		ngs = append(ngs, ng.Name)
		if ng.Name == "ng-c" && (!ng.Blocking || ng.MinorsBehind != 3) {
			t.Errorf("ng-c skew = %+v, want 3 minors behind and blocking", ng)
		}
	}
	if !slices.Equal(ngs, []string{"ng-a", "ng-b", "ng-c"}) {
		t.Errorf("nodegroups = %v, want all three pages", ngs)
	}

	got := map[string]AddonSkew{}
	for _, a := range report.Skew.Addons {
		got[a.Name] = a
	}
	if a := got["vpc-cni"]; a.Latest != "v1.19.2" || !a.Behind {
		t.Errorf("vpc-cni = %+v, want latest v1.19.2 (last page) and behind", a)
	}
	if a := got["coredns"]; a.Latest != "v1.11.4" || a.Behind {
		t.Errorf("coredns = %+v, want latest v1.11.4 and current", a)
	}
	if len(got) != 2 {
		t.Errorf("addons = %v, want both pages", report.Skew.Addons)
	}
}

// An unknown cluster is EKS's typed not-found error, surfaced as a failure.
func TestUpgradeCheck_UnknownCluster(t *testing.T) {
	m := mocks.NewEKSAPI().WithCluster("prod", "1.32").Build()
	svc := &ServiceImpl{eksClient: m}

	if _, err := svc.UpgradeCheck(context.Background(), "staging", UpgradeCheckOptions{}); err == nil {
		t.Fatal("UpgradeCheck on an unknown cluster returned no error")
	}
}
