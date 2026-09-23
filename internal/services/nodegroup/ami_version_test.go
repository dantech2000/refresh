package nodegroup

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/dantech2000/refresh/internal/mocks"
	"github.com/dantech2000/refresh/internal/types"
)

// Latest recommended AMIs per Kubernetes minor, as SSM would report them.
var latestAMIByVersion = map[string]string{
	"1.31": "ami-131-latest",
	"1.32": "ami-132-latest",
}

// newAMIVersionTestService wires fake AMI lookups: currentByNG maps nodegroup
// name to the AMI its nodes run; latest AMIs come from latestAMIByVersion.
func newAMIVersionTestService(api EKSAPI, currentByNG map[string]string, lookups *atomic.Int32) *ServiceImpl {
	svc := newTestService(api)
	svc.currentAMIFn = func(_ context.Context, ng *ekstypes.Nodegroup) string {
		return currentByNG[aws.ToString(ng.NodegroupName)]
	}
	svc.latestAMIFn = func(_ context.Context, v string, _ ekstypes.AMITypes) string {
		lookups.Add(1)
		return latestAMIByVersion[v]
	}
	return svc
}

// A nodegroup that lags the control plane (cluster 1.32, nodegroup 1.31) is
// compared against the latest 1.31 AMI, because an AMI-only update keeps it on
// 1.31. Before the fix it was compared against the 1.32 AMI and showed
// Outdated forever.
func TestList_ComparesAMIAgainstNodegroupVersion(t *testing.T) {
	api := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithNodegroup("ng-lag-latest", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("ng-lag-old", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("ng-lag-latest-2", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("ng-current", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	var lookups atomic.Int32
	svc := newAMIVersionTestService(api, map[string]string{
		"ng-lag-latest":   "ami-131-latest",
		"ng-lag-old":      "ami-131-older",
		"ng-lag-latest-2": "ami-131-latest",
		"ng-current":      "ami-132-latest",
	}, &lookups)

	summaries, err := svc.List(context.Background(), "prod", ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]types.AMIStatus{}
	for _, s := range summaries {
		got[s.Name] = s.AMIStatus
	}
	want := map[string]types.AMIStatus{
		"ng-lag-latest":   types.AMILatest,
		"ng-lag-old":      types.AMIOutdated,
		"ng-lag-latest-2": types.AMILatest,
		"ng-current":      types.AMILatest,
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s: AMIStatus = %v, want %v", name, got[name], w)
		}
	}
	if n := lookups.Load(); n != 2 {
		t.Errorf("latest-AMI lookups = %d, want 2 (one per distinct version/type)", n)
	}
}

func TestDescribe_ComparesAMIAgainstNodegroupVersion(t *testing.T) {
	api := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithNodegroup("ng-latest", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("ng-old", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	var lookups atomic.Int32
	svc := newAMIVersionTestService(api, map[string]string{
		"ng-latest": "ami-131-latest",
		"ng-old":    "ami-131-older",
	}, &lookups)

	for name, want := range map[string]types.AMIStatus{"ng-latest": types.AMILatest, "ng-old": types.AMIOutdated} {
		d, err := svc.Describe(context.Background(), "prod", name, DescribeOptions{})
		if err != nil {
			t.Fatalf("Describe %s: %v", name, err)
		}
		if d.AMIStatus != want {
			t.Errorf("%s: AMIStatus = %v, want %v", name, d.AMIStatus, want)
		}
		if d.LatestAMI != "ami-131-latest" {
			t.Errorf("%s: LatestAMI = %q, want the 1.31 latest", name, d.LatestAMI)
		}
	}
}
