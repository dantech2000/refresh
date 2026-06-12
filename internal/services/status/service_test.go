package status

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/types"
)

// fakeNodegroups implements NodegroupLister.
type fakeNodegroups struct {
	byCluster map[string][]nodegroup.NodegroupSummary
}

func (f *fakeNodegroups) List(_ context.Context, cluster string, _ nodegroup.ListOptions) ([]nodegroup.NodegroupSummary, error) {
	return f.byCluster[cluster], nil
}

// fakeAddons implements AddonAnalyzer.
type fakeAddons struct {
	installed map[string][]addons.AddonSummary
	available map[string][]addons.AddonVersionInfo
}

func (f *fakeAddons) List(_ context.Context, cluster string, _ addons.ListOptions) ([]addons.AddonSummary, error) {
	return f.installed[cluster], nil
}

func (f *fakeAddons) GetAvailableVersions(_ context.Context, addonName, _ string) ([]addons.AddonVersionInfo, error) {
	return f.available[addonName], nil
}

func newTestService(api *fakeClusterAPI, ng *fakeNodegroups, ad *fakeAddons) *Service {
	return &Service{
		region:     "us-east-1",
		clusterAPI: api,
		nodegroups: ng,
		addons:     ad,
		now:        func() time.Time { return date(2026, 6, 11) },
	}
}

func TestListClusterStatuses_Fleet(t *testing.T) {
	api := &fakeClusterAPI{
		clusters: []string{"prod", "auto"},
		describe: map[string]*ekstypes.Cluster{
			"prod": {Name: aws.String("prod"), Version: aws.String("1.32")},
			"auto": {
				Name:          aws.String("auto"),
				Version:       aws.String("1.32"),
				ComputeConfig: &ekstypes.ComputeConfigResponse{Enabled: aws.Bool(true)},
			},
		},
		versions: map[string]ekstypes.ClusterVersionInformation{
			"1.32": {
				ClusterVersion:           aws.String("1.32"),
				EndOfStandardSupportDate: timePtr(date(2027, 3, 23)),
				EndOfExtendedSupportDate: timePtr(date(2028, 3, 23)),
			},
		},
	}
	ng := &fakeNodegroups{byCluster: map[string][]nodegroup.NodegroupSummary{
		"prod": {
			{Name: "ng-a", AMIStatus: types.AMILatest, CurrentAMI: "ami-1"},
			{Name: "ng-b", AMIStatus: types.AMIOutdated, CurrentAMI: "ami-2"},
		},
		// "auto" has no managed nodegroups.
	}}
	ad := &fakeAddons{
		installed: map[string][]addons.AddonSummary{
			"prod": {{Name: "vpc-cni", Version: "v1.10.0"}, {Name: "coredns", Version: "v1.11.4"}},
		},
		available: map[string][]addons.AddonVersionInfo{
			"vpc-cni": {{Version: "v1.18.1"}, {Version: "v1.10.0"}}, // behind
			"coredns": {{Version: "v1.11.4"}},                       // current
		},
	}

	svc := newTestService(api, ng, ad)
	statuses, err := svc.ListClusterStatuses(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("got %d statuses, want 2", len(statuses))
	}

	byName := map[string]ClusterStatus{}
	for _, c := range statuses {
		byName[c.Name] = c
	}

	prod := byName["prod"]
	if prod.Compute != ComputeManaged {
		t.Errorf("prod compute = %s, want managed-nodegroups", prod.Compute)
	}
	if prod.StaleAMI.Behind != 1 || prod.StaleAMI.Total != 2 {
		t.Errorf("prod stale AMI = %d/%d, want 1/2", prod.StaleAMI.Behind, prod.StaleAMI.Total)
	}
	if prod.AddonsBehind.Behind != 1 {
		t.Errorf("prod addons behind = %d, want 1", prod.AddonsBehind.Behind)
	}
	if len(prod.AddonsBehind.Names) != 1 || prod.AddonsBehind.Names[0] != "vpc-cni" {
		t.Errorf("prod addons behind names = %v, want [vpc-cni]", prod.AddonsBehind.Names)
	}
	if prod.Support.Tier != SupportStandard {
		t.Errorf("prod support = %s, want standard", prod.Support.Tier)
	}
	if !prod.NeedsAttention() {
		t.Error("prod should need attention (stale AMI + addon behind)")
	}

	auto := byName["auto"]
	if auto.Compute != ComputeAutoMode {
		t.Errorf("auto compute = %s, want auto-mode", auto.Compute)
	}
	if auto.NodegroupCount != 0 {
		t.Errorf("auto nodegroup count = %d, want 0", auto.NodegroupCount)
	}
	if auto.NeedsAttention() {
		t.Error("auto (no managed nodegroups) should not need AMI attention")
	}
}

func TestAssembleCluster_DescribeErrorIsNonFatal(t *testing.T) {
	api := &fakeClusterAPI{
		clusters: []string{"ghost"},
		describe: map[string]*ekstypes.Cluster{}, // describe will error
	}
	svc := newTestService(api, &fakeNodegroups{}, &fakeAddons{})
	statuses, err := svc.ListClusterStatuses(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("sweep should not fail on a single bad cluster: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("got %d, want 1 partial row", len(statuses))
	}
	if len(statuses[0].Errors) == 0 {
		t.Error("expected the describe failure recorded on the row")
	}
	if statuses[0].Support.Tier != SupportUnknown {
		t.Errorf("support = %s, want unknown for undescribable cluster", statuses[0].Support.Tier)
	}
}

func TestListClusterStatuses_NameFilter(t *testing.T) {
	api := &fakeClusterAPI{
		clusters: []string{"prod-east", "staging", "prod-west"},
		describe: map[string]*ekstypes.Cluster{
			"prod-east": {Name: aws.String("prod-east"), Version: aws.String("1.32")},
			"prod-west": {Name: aws.String("prod-west"), Version: aws.String("1.32")},
			"staging":   {Name: aws.String("staging"), Version: aws.String("1.32")},
		},
		versions: map[string]ekstypes.ClusterVersionInformation{
			"1.32": {ClusterVersion: aws.String("1.32"), EndOfStandardSupportDate: timePtr(date(2027, 3, 23))},
		},
	}
	svc := newTestService(api, &fakeNodegroups{}, &fakeAddons{})
	statuses, err := svc.ListClusterStatuses(context.Background(), ListOptions{NamePattern: "prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("name filter returned %d, want 2", len(statuses))
	}
}
