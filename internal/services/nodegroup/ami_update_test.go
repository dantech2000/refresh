package nodegroup

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
	"github.com/dantech2000/refresh/internal/types"
)

func TestDecideAMIUpdate(t *testing.T) {
	managed := &ekstypes.Nodegroup{AmiType: ekstypes.AMITypesAl2023X8664Standard, Status: ekstypes.NodegroupStatusActive}
	custom := &ekstypes.Nodegroup{AmiType: ekstypes.AMITypesCustom, Status: ekstypes.NodegroupStatusActive}
	updating := &ekstypes.Nodegroup{AmiType: ekstypes.AMITypesAl2023X8664Standard, Status: ekstypes.NodegroupStatusUpdating}

	cases := []struct {
		name            string
		ng              *ekstypes.Nodegroup
		opts            AMIUpdateOptions
		current, latest string
		want            types.DryRunAction
		wantLookup      bool
	}{
		{"custom skips even with force", custom, AMIUpdateOptions{Force: true}, "", "", types.ActionSkipCustom, false},
		{"updating skips even with force", updating, AMIUpdateOptions{Force: true}, "", "", types.ActionSkipUpdating, false},
		{"outdated", managed, AMIUpdateOptions{}, "ami-old", "ami-new", types.ActionUpdate, true},
		{"on latest", managed, AMIUpdateOptions{}, "ami-new", "ami-new", types.ActionSkipLatest, true},
		{"unknown current", managed, AMIUpdateOptions{}, "", "ami-new", types.ActionUpdate, true},
		{"unknown latest", managed, AMIUpdateOptions{}, "ami-old", "", types.ActionUpdate, true},
		{"force without lookup", managed, AMIUpdateOptions{Force: true}, "ami-new", "ami-new", types.ActionForceUpdate, false},
		{"reroll without lookup", managed, AMIUpdateOptions{Reroll: true}, "ami-new", "ami-new", types.ActionUpdate, false},
		{"preview force looks up", managed, AMIUpdateOptions{Force: true, Preview: true}, "ami-new", "ami-new", types.ActionForceUpdate, true},
		{"preview reroll on latest", managed, AMIUpdateOptions{Reroll: true, Preview: true}, "ami-new", "ami-new", types.ActionUpdate, true},
		{"preview on latest", managed, AMIUpdateOptions{Preview: true}, "ami-new", "ami-new", types.ActionSkipLatest, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookups := 0
			d := DecideAMIUpdate(context.Background(), tc.ng, tc.opts, func(context.Context, *ekstypes.Nodegroup) (string, string) {
				lookups++
				return tc.current, tc.latest
			})
			if d.Action != tc.want {
				t.Errorf("action = %v, want %v (reason %q)", d.Action, tc.want, d.Reason)
			}
			if d.Reason == "" {
				t.Error("reason is empty")
			}
			if got := lookups > 0; got != tc.wantLookup || lookups > 1 {
				t.Errorf("AMI lookups = %d, want lookup %v (at most once)", lookups, tc.wantLookup)
			}
			if d.Starts() != (tc.want == types.ActionUpdate || tc.want == types.ActionForceUpdate) {
				t.Errorf("Starts() = %v for %v", d.Starts(), d.Action)
			}
		})
	}
}

// With the control plane on 1.32 and a nodegroup still on 1.31, the update
// (Version pinned to the nodegroup's 1.31) can only roll to the latest 1.31
// AMI. A nodegroup already on that AMI must be skipped; one on an older 1.31
// AMI must not. A nodegroup with no version falls back to the cluster's.
func TestAMIUpdateDecider_UsesNodegroupVersion(t *testing.T) {
	api := mocks.NewEKSAPI().WithCluster("prod", "1.32").Build()
	svc := newTestService(api)
	svc.latestAMIFn = func(_ context.Context, v string, _ ekstypes.AMITypes) (string, error) {
		return map[string]string{"1.31": "ami-131-latest", "1.32": "ami-132-latest"}[v], nil
	}
	current := map[string]string{
		"ng-latest":     "ami-131-latest",
		"ng-old":        "ami-131-older",
		"ng-no-version": "ami-132-latest",
	}
	svc.currentAMIFn = func(_ context.Context, ng *ekstypes.Nodegroup) string { return current[aws.ToString(ng.NodegroupName)] }
	decide := svc.AMIUpdateDecider("prod", AMIUpdateOptions{})

	cases := []struct {
		name    string
		version *string
		want    types.DryRunAction
	}{
		{"ng-latest", aws.String("1.31"), types.ActionSkipLatest},
		{"ng-old", aws.String("1.31"), types.ActionUpdate},
		{"ng-no-version", nil, types.ActionSkipLatest},
	}
	for _, tc := range cases {
		ng := &ekstypes.Nodegroup{NodegroupName: aws.String(tc.name), Version: tc.version, AmiType: ekstypes.AMITypesAl2023X8664Standard}
		if got := decide(context.Background(), ng).Action; got != tc.want {
			t.Errorf("%s: action = %v, want %v", tc.name, got, tc.want)
		}
	}
	if n := api.Calls.DescribeCluster; n != 1 {
		t.Errorf("DescribeCluster calls = %d, want 1 across three nodegroups", n)
	}
}

// If the cluster can't be described, the AMI status is unknown: the
// nodegroup is rolled, never skipped as "already latest".
func TestAMIUpdateDecider_UndescribableClusterRolls(t *testing.T) {
	// The mock knows no cluster "prod", so DescribeCluster is NotFound.
	svc := newTestService(mocks.NewEKSAPI().WithCluster("other", "1.31").Build())
	svc.latestAMIFn = func(context.Context, string, ekstypes.AMITypes) (string, error) { return "ami-x", nil }
	svc.currentAMIFn = func(context.Context, *ekstypes.Nodegroup) string { return "ami-x" }

	d := svc.AMIUpdateDecider("prod", AMIUpdateOptions{})(context.Background(),
		&ekstypes.Nodegroup{Version: aws.String("1.31"), AmiType: ekstypes.AMITypesAl2023X8664Standard})
	if d.Action != types.ActionUpdate {
		t.Errorf("action = %v, want Update when the cluster version is unknown", d.Action)
	}
}
