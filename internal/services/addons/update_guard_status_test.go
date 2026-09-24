package addons

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// UP_TO_DATE needs an ACTIVE add-on: a broken add-on at the target is
// re-applied (the repair path), and one already updating at the target is
// reported IN_PROGRESS instead of up to date.
func TestUpdate_VersionGuardHonorsStatus(t *testing.T) {
	started := StatusStarted // the update was submitted
	cases := []struct {
		name       string
		status     ekstypes.AddonStatus
		installed  string
		version    string
		wantStatus string
		wantUpdate bool
	}{
		{name: "active at target", status: ekstypes.AddonStatusActive, installed: "v1.19.0", version: "v1.19.0", wantStatus: StatusUpToDate},
		{name: "degraded at target", status: ekstypes.AddonStatusDegraded, installed: "v1.19.0", version: "v1.19.0", wantStatus: started, wantUpdate: true},
		{name: "update failed at latest", status: ekstypes.AddonStatusUpdateFailed, installed: "v1.19.0", version: "latest", wantStatus: started, wantUpdate: true},
		{name: "create failed at target", status: ekstypes.AddonStatusCreateFailed, installed: "v1.19.0", version: "v1.19.0", wantStatus: started, wantUpdate: true},
		{name: "updating at target", status: ekstypes.AddonStatusUpdating, installed: "v1.19.0", version: "latest", wantStatus: StatusInProgress},
		{name: "degraded above latest is not downgraded", status: ekstypes.AddonStatusDegraded, installed: "v1.20.0", version: "latest", wantStatus: StatusUpToDate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := waitMock(tc.installed, tc.status, "v1.19.0", "v1.18.0").Build()
			res, err := NewService(m, logger()).Update(t.Context(), "prod", "vpc-cni", UpdateOptions{Version: tc.version})
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			if res.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s", res.Status, tc.wantStatus)
			}
			if got := m.Calls.UpdateAddon == 1; got != tc.wantUpdate {
				t.Errorf("UpdateAddon calls = %d, want update %v", m.Calls.UpdateAddon, tc.wantUpdate)
			}
		})
	}
}

// With Wait, an add-on already UPDATING at the target is waited on (no new
// UpdateAddon) and reported COMPLETED once it settles ACTIVE.
func TestUpdate_UpdatingAtTargetWaits(t *testing.T) {
	m := waitMock("v1.19.0", ekstypes.AddonStatusActive, "v1.19.0").Build()
	var n atomic.Int32
	m.DescribeAddonFn = func(_ context.Context, in *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
		status := ekstypes.AddonStatusActive
		if n.Add(1) <= 2 {
			status = ekstypes.AddonStatusUpdating
		}
		return &eks.DescribeAddonOutput{Addon: &ekstypes.Addon{AddonName: in.AddonName, AddonVersion: aws.String("v1.19.0"), Status: status}}, nil
	}

	res, err := NewService(m, logger()).Update(t.Context(), "prod", "vpc-cni", waitOpts("latest"))
	if err != nil || res.Status != StatusCompleted {
		t.Fatalf("Update = %+v, %v; want COMPLETED", res, err)
	}
	if m.Calls.UpdateAddon != 0 {
		t.Errorf("UpdateAddon calls = %d, want 0 (attached to the in-flight update)", m.Calls.UpdateAddon)
	}
}

// --all applies the same rule: a DEGRADED add-on at latest is re-applied.
func TestUpdateAll_ReappliesDegradedAtTarget(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithAddon("coredns", "v1.11.3", ekstypes.AddonStatusDegraded).
		WithAddon("vpc-cni", "v1.19.0", ekstypes.AddonStatusActive).
		WithAddonVersions("coredns", []string{"v1.11.3"}, "1.32").
		WithAddonVersions("vpc-cni", []string{"v1.19.0"}, "1.32").
		WithUpdateAddon("u-1").
		Build()

	results, err := NewService(m, logger()).UpdateAll(t.Context(), "prod", UpdateAllOptions{})
	if err != nil {
		t.Fatalf("UpdateAll: %v", err)
	}
	for _, r := range results {
		want := StatusUpToDate
		if r.AddonName == "coredns" {
			want = StatusStarted
		}
		if r.Status != want {
			t.Errorf("%s status = %s, want %s", r.AddonName, r.Status, want)
		}
	}
	if m.Calls.UpdateAddon != 1 {
		t.Errorf("UpdateAddon calls = %d, want 1 (coredns only)", m.Calls.UpdateAddon)
	}
}
