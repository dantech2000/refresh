package change

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/fleet"
)

// TestDecideAddon is the step table of an add-on update, row for row,
// with the cases of the old version guard beside the rows they belong to.
func TestDecideAddon(t *testing.T) {
	latest := func(v string) addonTarget { return addonTarget{Version: v, Known: true} }
	pinned := func(v string, supported ...string) addonTarget {
		return addonTarget{Version: v, Pinned: true, Known: true, Supported: supported}
	}
	tests := []struct {
		name      string
		installed string
		status    string
		target    addonTarget
		want      addonDecision
		wantErr   string
	}{
		{
			name: "1: --version that the cluster cannot run", installed: "v1.19.0", status: "ACTIVE",
			target:  pinned("v1.21.0", "v1.20.0", "v1.19.0"),
			wantErr: "add-on vpc-cni version v1.21.0 is not compatible with Kubernetes 1.33; supported versions (newest first): v1.20.0, v1.19.0",
		},
		{
			name: "2: the latest version could not be read", installed: "v1.19.0", status: "ACTIVE",
			target: addonTarget{},
			want:   addonDecision{Action: Blocked, Unread: "could not read the versions of add-on vpc-cni that Kubernetes 1.33 can run"},
		},
		{
			name: "3: at target and ACTIVE", installed: "v1.19.0", status: "ACTIVE", target: latest("v1.19.0"),
			want: addonDecision{Action: Skip, Reason: AlreadyAtTarget, To: "v1.19.0"},
		},
		{
			name: "3: newer than the latest and ACTIVE", installed: "v1.20.0", status: "ACTIVE", target: latest("v1.19.0"),
			want: addonDecision{Action: Skip, Reason: AlreadyAtTarget, To: "v1.20.0"},
		},
		{
			name: "4: at target and UPDATING", installed: "v1.19.0", status: "UPDATING", target: latest("v1.19.0"),
			want: addonDecision{Action: Attach, Reason: InFlight, To: "v1.19.0"},
		},
		{
			name: "4: at the pinned version and CREATING", installed: "v1.19.0", status: "CREATING", target: pinned("v1.19.0", "v1.19.0"),
			want: addonDecision{Action: Attach, Reason: InFlight, To: "v1.19.0"},
		},
		{
			name: "5: degraded above the latest is not downgraded", installed: "v1.20.0", status: "DEGRADED", target: latest("v1.19.0"),
			want: addonDecision{
				Action: Skip, Reason: NewerThanLatest, To: "v1.20.0",
				Notice: "vpc-cni is DEGRADED at v1.20.0, newer than the latest catalogue version v1.19.0; not re-applied",
			},
		},
		{
			name: "6: degraded at target", installed: "v1.19.0", status: "DEGRADED", target: pinned("v1.19.0", "v1.19.0"),
			want: addonDecision{Action: Submit, Reason: Repair, To: "v1.19.0"},
		},
		{
			name: "6: update failed at the latest", installed: "v1.19.0", status: "UPDATE_FAILED", target: latest("v1.19.0"),
			want: addonDecision{Action: Submit, Reason: Repair, To: "v1.19.0"},
		},
		{
			name: "6: create failed at target", installed: "v1.19.0", status: "CREATE_FAILED", target: latest("v1.19.0"),
			want: addonDecision{Action: Submit, Reason: Repair, To: "v1.19.0"},
		},
		{
			name: "7: UPDATING to another version", installed: "v1.18.0", status: "UPDATING", target: latest("v1.19.0"),
			want: addonDecision{Action: Skip, Reason: InFlight, To: "v1.19.0"},
		},
		{
			name: "7: CREATING behind the target", installed: "v1.18.0", status: "CREATING", target: latest("v1.19.0"),
			want: addonDecision{Action: Skip, Reason: InFlight, To: "v1.19.0"},
		},
		{
			name: "7: DELETING at target", installed: "v1.19.0", status: "DELETING", target: latest("v1.19.0"),
			want: addonDecision{Action: Skip, Reason: InFlight, To: "v1.19.0"},
		},
		{
			name: "7: DELETING above the latest", installed: "v1.20.0", status: "DELETING", target: latest("v1.19.0"),
			want: addonDecision{Action: Skip, Reason: InFlight, To: "v1.19.0"},
		},
		{
			name: "8: behind the latest", installed: "v1.18.0", status: "ACTIVE", target: latest("v1.19.0"),
			want: addonDecision{Action: Submit, Reason: VersionBehind, To: "v1.19.0"},
		},
		{
			name: "8: degraded and behind", installed: "v1.18.0", status: "DEGRADED", target: latest("v1.19.0"),
			want: addonDecision{Action: Submit, Reason: VersionBehind, To: "v1.19.0"},
		},
		{
			name: "8: a pinned version that is older", installed: "v1.20.0", status: "ACTIVE", target: pinned("v1.19.0", "v1.20.0", "v1.19.0"),
			want: addonDecision{Action: Submit, Reason: VersionAhead, To: "v1.19.0", Notice: "downgrading vpc-cni from v1.20.0 to v1.19.0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := fleet.Addon{Name: "vpc-cni", State: fleet.Collected, Version: tt.installed, Status: tt.status}
			got := decideAddon(a, tt.target, "1.33")
			if tt.wantErr != "" {
				if got.Err == nil || got.Err.Error() != tt.wantErr {
					t.Fatalf("error = %v, want %q", got.Err, tt.wantErr)
				}
				return
			}
			if got.Err != nil {
				t.Fatalf("error = %v", got.Err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("decision (-want +got):\n%s", diff)
			}
		})
	}
}

func TestVersionList(t *testing.T) {
	if got := versionList(nil); got != "none" {
		t.Errorf("versionList(nil) = %q", got)
	}
	many := []string{"v7", "v6", "v5", "v4", "v3", "v2", "v1"}
	if got, want := versionList(many), "v7, v6, v5, v4, v3 (and 2 more)"; got != want {
		t.Errorf("versionList = %q, want %q", got, want)
	}
}

// The dependency order: an add-on comes after the add-ons that it
// depends on, when they are in the list. A dependency that is not in the
// list is no edge, and the names that do not depend on one another keep
// their order, so the order is the same on every run.
func TestSortByDependency(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "every kind of add-on",
			in:   []string{"adot", "aws-ebs-csi-driver", "coredns", "kube-proxy", "my-addon", "vpc-cni"},
			want: []string{"my-addon", "vpc-cni", "coredns", "kube-proxy", "adot", "aws-ebs-csi-driver"},
		},
		{
			// coredns and kube-proxy depend on vpc-cni, which is not in
			// the list: they keep their order, before my-addon.
			name: "a dependency that is not in the list",
			in:   []string{"kube-proxy", "coredns", "my-addon"},
			want: []string{"kube-proxy", "coredns", "my-addon"},
		},
		{name: "nothing", in: nil, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, sortByDependency(tt.in)); diff != "" {
				t.Errorf("order (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAddonUpdateRequestValidate(t *testing.T) {
	prod := Target{Cluster: "prod"}
	tests := []struct {
		name string
		req  AddonUpdateRequest
		want string // a part of the error, or "" for none
	}{
		{name: "one add-on", req: AddonUpdateRequest{Target: prod, Addons: []string{"vpc-cni"}}},
		{name: "one add-on and a version", req: AddonUpdateRequest{Target: prod, Addons: []string{"vpc-cni"}, Version: "v1"}},
		{name: "all with skips", req: AddonUpdateRequest{Target: prod, All: true, Skip: []string{"coredns"}}},
		{name: "no cluster", req: AddonUpdateRequest{All: true}, want: "no cluster"},
		{name: "names and all", req: AddonUpdateRequest{Target: prod, All: true, Addons: []string{"vpc-cni"}}, want: "not both"},
		{name: "neither", req: AddonUpdateRequest{Target: prod}, want: "name at least one add-on"},
		{name: "a version for two", req: AddonUpdateRequest{Target: prod, Addons: []string{"a", "b"}, Version: "v1"}, want: "--version needs exactly one add-on"},
		{name: "a version with all", req: AddonUpdateRequest{Target: prod, All: true, Version: "v1"}, want: "--version needs exactly one add-on"},
		{name: "skip without all", req: AddonUpdateRequest{Target: prod, Addons: []string{"a"}, Skip: []string{"b"}}, want: "--skip-addon needs --all"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.validate()
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("error = %v, want none", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("error = %v, want %q", err, tt.want)
			}
		})
	}
}
