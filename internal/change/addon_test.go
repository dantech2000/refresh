package change

import (
	"slices"
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
			want: addonDecision{Action: Submit, Reason: VersionBehind, To: "v1.19.0", Notice: "downgrading vpc-cni from v1.20.0 to v1.19.0"},
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

// The tests of the dependency order, as the old tree had them.

func TestSortByDependency_VPCCNIFirst(t *testing.T) {
	ordered := sortByDependency([]string{"coredns", "kube-proxy", "vpc-cni"})
	cni, dns, proxy := slices.Index(ordered, "vpc-cni"), slices.Index(ordered, "coredns"), slices.Index(ordered, "kube-proxy")
	if cni == -1 || dns == -1 || proxy == -1 {
		t.Fatalf("an add-on is missing from %v", ordered)
	}
	if cni > dns {
		t.Errorf("vpc-cni (%d) must come before coredns (%d)", cni, dns)
	}
	if cni > proxy {
		t.Errorf("vpc-cni (%d) must come before kube-proxy (%d)", cni, proxy)
	}
}

func TestSortByDependency_StorageAfterCoreDNS(t *testing.T) {
	ordered := sortByDependency([]string{"aws-ebs-csi-driver", "coredns", "vpc-cni"})
	dns, ebs := slices.Index(ordered, "coredns"), slices.Index(ordered, "aws-ebs-csi-driver")
	if dns == -1 || ebs == -1 {
		t.Fatalf("an add-on is missing from %v", ordered)
	}
	if dns > ebs {
		t.Errorf("coredns (%d) must come before aws-ebs-csi-driver (%d)", dns, ebs)
	}
}

func TestSortByDependency_UnknownAddonAppended(t *testing.T) {
	if got := sortByDependency([]string{"my-custom-addon", "vpc-cni"}); len(got) != 2 {
		t.Fatalf("got %v, want 2 add-ons", got)
	}
}

func TestSortByDependency_MissingDepSkipped(t *testing.T) {
	// coredns depends on vpc-cni, which is not in the list.
	if got := sortByDependency([]string{"coredns", "kube-proxy"}); len(got) != 2 {
		t.Fatalf("got %v, want 2 add-ons", got)
	}
}

func TestSortByDependency_PreservesAll(t *testing.T) {
	input := []string{"aws-ebs-csi-driver", "coredns", "kube-proxy", "vpc-cni", "amazon-cloudwatch-observability"}
	ordered := sortByDependency(input)
	if len(ordered) != len(input) {
		t.Errorf("got %d add-ons, want %d", len(ordered), len(input))
	}
	for _, name := range input {
		if !slices.Contains(ordered, name) {
			t.Errorf("%s is missing from %v", name, ordered)
		}
	}
}

func TestSortByDependency_EmptyInput(t *testing.T) {
	if got := sortByDependency(nil); len(got) != 0 {
		t.Errorf("got %v, want nothing", got)
	}
}

// The order is the same on every run: the names that do not depend on
// one another keep their order.
func TestSortByDependency_Order(t *testing.T) {
	got := sortByDependency([]string{"adot", "aws-ebs-csi-driver", "coredns", "kube-proxy", "my-addon", "vpc-cni"})
	want := []string{"my-addon", "vpc-cni", "coredns", "kube-proxy", "adot", "aws-ebs-csi-driver"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("order (-want +got):\n%s", diff)
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
