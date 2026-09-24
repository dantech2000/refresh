package status

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/types"
)

// fakeNodegroups implements NodegroupLister.
type fakeNodegroups struct {
	byCluster map[string][]nodegroup.NodegroupSummary
	failures  map[string][]diag.Failure
	// noVersion counts calls that did not pass the cluster version, so
	// ListDetailed would describe the cluster a second time.
	noVersion atomic.Int64
}

func (f *fakeNodegroups) ListDetailed(_ context.Context, cluster string, opts nodegroup.ListOptions) (nodegroup.ListResult, error) {
	if opts.ClusterVersion == "" {
		f.noVersion.Add(1)
	}
	return nodegroup.ListResult{Summaries: f.byCluster[cluster], Failures: f.failures[cluster]}, nil
}

// failureText joins the rows' failures as their one-line text, for asserts.
func failureText(fs []diag.Failure) string {
	lines := make([]string, len(fs))
	for i, f := range fs {
		lines[i] = fmt.Sprintf("%s %s/%s %s %s: %s", f.Kind, f.Cluster, f.Name, f.Operation, f.Reason, f.Error)
	}
	return strings.Join(lines, "; ")
}

// fakeAddons implements AddonAnalyzer.
type fakeAddons struct {
	installed map[string][]addons.AddonSummary
	available map[string][]addons.AddonVersionInfo
	// versionErr makes GetAvailableVersions fail for an addon with an API
	// error (throttling, AccessDenied, network), as the real service does
	// once its retries give up.
	versionErr map[string]error
	// versionCalls counts GetAvailableVersions calls.
	versionCalls atomic.Int64
}

func (f *fakeAddons) ListDetailed(_ context.Context, cluster string, _ addons.ListOptions) (addons.ListResult, error) {
	return addons.ListResult{Summaries: f.installed[cluster]}, nil
}

// GetAvailableVersions mirrors the real service: an API failure is returned
// wrapped, no versions is an ErrNoVersionsFound error (never an empty slice
// with a nil error), and versions come back sorted newest first whatever
// order the fixture lists them in.
func (f *fakeAddons) GetAvailableVersions(_ context.Context, addonName, _ string) ([]addons.AddonVersionInfo, error) {
	f.versionCalls.Add(1)
	if err := f.versionErr[addonName]; err != nil {
		return nil, fmt.Errorf("describing addon versions: %w", err)
	}
	v := slices.Clone(f.available[addonName])
	if len(v) == 0 {
		return nil, fmt.Errorf("%w for addon %s", addons.ErrNoVersionsFound, addonName)
	}
	slices.SortStableFunc(v, func(a, b addons.AddonVersionInfo) int {
		return addons.CompareVersions(b.Version, a.Version)
	})
	return v, nil
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
	if prod.NodegroupsBehindControlPlane != 0 {
		t.Errorf("prod nodegroups behind control plane = %d, want 0", prod.NodegroupsBehindControlPlane)
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
	fs := statuses[0].Failures
	if len(fs) != 1 || fs[0].Kind != diag.KindCluster || fs[0].Name != "ghost" || fs[0].Operation != diag.OpDescribeCluster || fs[0].Region != "us-east-1" {
		t.Errorf("failures = %s, want the DescribeCluster failure of ghost", failureText(fs))
	}
	if statuses[0].Support.Tier != SupportUnknown {
		t.Errorf("support = %s, want unknown for undescribable cluster", statuses[0].Support.Tier)
	}
}

// TestAssembleCluster_HealthIssues verifies AWS-reported control-plane health
// issues are counted onto the row and flip NeedsAttention, even when AMIs and
// addons are current.
func TestAssembleCluster_HealthIssues(t *testing.T) {
	api := &fakeClusterAPI{
		clusters: []string{"prod"},
		describe: map[string]*ekstypes.Cluster{
			"prod": {
				Name:    aws.String("prod"),
				Version: aws.String("1.32"),
				Health: &ekstypes.ClusterHealth{Issues: []ekstypes.ClusterIssue{
					{Code: ekstypes.ClusterIssueCodeInternalFailure, Message: aws.String("IAM role failure")},
					{Code: ekstypes.ClusterIssueCodeResourceLimitExceeded, Message: aws.String("ENI limit")},
				}},
			},
		},
		versions: map[string]ekstypes.ClusterVersionInformation{
			"1.32": {ClusterVersion: aws.String("1.32"), EndOfStandardSupportDate: timePtr(date(2027, 3, 23))},
		},
	}
	svc := newTestService(api, &fakeNodegroups{}, &fakeAddons{})
	statuses, err := svc.ListClusterStatuses(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	c := statuses[0]
	if c.HealthIssues != 2 {
		t.Errorf("health issues = %d, want 2", c.HealthIssues)
	}
	// No stale AMIs/addons, but health issues alone must flag attention.
	if c.StaleAMI.Behind != 0 || c.AddonsBehind.Behind != 0 {
		t.Fatalf("test precondition: expected no stale AMIs/addons, got %+v / %+v", c.StaleAMI, c.AddonsBehind)
	}
	if !c.NeedsAttention() {
		t.Error("a cluster with control-plane health issues should need attention")
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

// A half-finished upgrade (control plane 1.32, nodegroup on the newest 1.31
// AMI) has no stale AMI, but must still count as behind and need attention.
func TestAssembleCluster_NodegroupBehindControlPlane(t *testing.T) {
	api := &fakeClusterAPI{
		clusters: []string{"prod"},
		describe: map[string]*ekstypes.Cluster{
			"prod": {Name: aws.String("prod"), Version: aws.String("1.32")},
		},
	}
	ng := &fakeNodegroups{byCluster: map[string][]nodegroup.NodegroupSummary{
		"prod": {
			{Name: "ng-lag", AMIStatus: types.AMILatest, K8sVersion: "1.31", VersionBehind: true},
			{Name: "ng-cur", AMIStatus: types.AMILatest, K8sVersion: "1.32"},
		},
	}}
	ng.failures = map[string][]diag.Failure{"prod": {diag.New(diag.KindNodegroup, "ng-broken", diag.ReasonUnknown, "boom")}}
	svc := newTestService(api, ng, &fakeAddons{})
	cs := svc.assembleCluster(context.Background(), svc.newSweep(), "prod")

	// A failed nodegroup makes the row incomplete, but the behind count from
	// the nodegroups that did resolve still stands.
	if !cs.Incomplete {
		t.Error("row with a failed nodegroup should be incomplete")
	}
	if cs.StaleAMI.Behind != 0 {
		t.Errorf("stale AMI behind = %d, want 0", cs.StaleAMI.Behind)
	}
	if cs.NodegroupsBehindControlPlane != 1 {
		t.Errorf("nodegroups behind control plane = %d, want 1", cs.NodegroupsBehindControlPlane)
	}
	if !cs.NeedsAttention() {
		t.Error("a nodegroup behind the control plane should need attention")
	}
}
