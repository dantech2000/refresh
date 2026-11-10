package health_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

func mustVersion(t *testing.T, s string) fleet.Version {
	t.Helper()
	v, err := fleet.ParseVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The table of the version skew check: a nodegroup at the kubelet skew
// limit of three minor versions blocks the next upgrade, one behind it
// warns, and so does an add-on behind its latest version. A part that was
// not read makes the check unreadable, unless a nodegroup at the limit
// decides it already.
func TestVersionSkew(t *testing.T) {
	ng := func(name, version string) fleet.Nodegroup {
		return fleet.Nodegroup{Name: name, State: fleet.Collected, Version: mustVersion(t, version)}
	}
	addon := func(name, version, latest string) fleet.Addon {
		return fleet.Addon{Name: name, State: fleet.Collected, LatestState: fleet.Collected, Version: version, Latest: latest}
	}
	unreadNodegroup := fleet.Nodegroup{Name: "ng-x", State: fleet.Failed}
	tests := []struct {
		name       string
		cp         string
		nodegroups []fleet.Nodegroup
		addons     []fleet.Addon
		outcome    health.Outcome
		message    string
		findings   []string
	}{
		{
			name: "everything current", cp: "1.33",
			nodegroups: []fleet.Nodegroup{ng("ng-a", "1.33")}, addons: []fleet.Addon{addon("vpc-cni", "v1.20.0-eksbuild.1", "v1.20.0-eksbuild.1")},
			outcome: health.Pass, message: "every nodegroup on 1.33 and every add-on at its latest",
		},
		{
			name: "two minor versions behind", cp: "1.33", nodegroups: []fleet.Nodegroup{ng("ng-a", "1.31")},
			outcome: health.Warn, message: "1 nodegroup behind the control plane (ng-a (1.31))", findings: []string{"ng-a"},
		},
		{
			name: "at the kubelet skew limit", cp: "1.33", nodegroups: []fleet.Nodegroup{ng("ng-a", "1.30"), ng("ng-b", "1.32")},
			outcome:  health.Fail,
			message:  "1 nodegroup at the kubelet skew limit (ng-a (1.30, 3 behind)); upgrade the nodes before the control plane; 1 nodegroup behind the control plane (ng-b (1.32))",
			findings: []string{"ng-a", "ng-b"},
		},
		{
			name: "an add-on behind its latest", cp: "1.33",
			addons:  []fleet.Addon{addon("coredns", "v1.11.4-eksbuild.2", "v1.12.0-eksbuild.1")},
			outcome: health.Warn, message: "1 add-on behind latest (coredns (v1.11.4-eksbuild.2, latest v1.12.0-eksbuild.1))", findings: []string{"coredns"},
		},
		{
			name: "a nodegroup ahead or of another major version is not behind", cp: "1.33",
			nodegroups: []fleet.Nodegroup{ng("ng-a", "1.34"), ng("ng-b", "2.30")},
			outcome:    health.Pass, message: "every nodegroup on 1.33 and every add-on at its latest",
		},
		{
			name: "a part that was not read", cp: "1.33",
			nodegroups: []fleet.Nodegroup{ng("ng-a", "1.32"), unreadNodegroup},
			addons:     []fleet.Addon{{Name: "coredns", State: fleet.Collected, LatestState: fleet.Failed}},
			outcome:    health.Unreadable, message: "not read: nodegroup ng-x, add-on coredns", findings: []string{"ng-a"},
		},
		{
			name: "the limit decides with a part that was not read", cp: "1.33",
			nodegroups: []fleet.Nodegroup{ng("ng-a", "1.29"), unreadNodegroup},
			outcome:    health.Fail,
			message:    "1 nodegroup at the kubelet skew limit (ng-a (1.29, 4 behind)); upgrade the nodes before the control plane; not read: nodegroup ng-x",
			findings:   []string{"ng-a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := &fleet.Snapshot{Cluster: fleet.Cluster{Name: "prod", Version: mustVersion(t, tt.cp)}, Nodegroups: tt.nodegroups, Addons: tt.addons}
			snap.Have.Cluster = fleet.Collected
			got := health.VersionSkew().Run(context.Background(), &health.Input{Snapshot: snap})
			if got.Outcome != tt.outcome || got.Message != tt.message {
				t.Errorf("result = %s %q,\nwant %s %q", got.Outcome, got.Message, tt.outcome, tt.message)
			}
			if diff := cmp.Diff(tt.findings, got.Findings); diff != "" {
				t.Errorf("findings (-want +got):\n%s", diff)
			}
		})
	}
	t.Run("the control plane was not read", func(t *testing.T) {
		snap := &fleet.Snapshot{Cluster: fleet.Cluster{Name: "prod"}}
		snap.Have.Cluster = fleet.Failed
		got := health.VersionSkew().Run(context.Background(), &health.Input{Snapshot: snap})
		if got.Outcome != health.Unreadable || got.Message != "the version of the control plane is not known" {
			t.Errorf("result = %s %q, want Unreadable", got.Outcome, got.Message)
		}
	})
}
