package change

import (
	"errors"
	"testing"
)

// An add-on version that is empty or does not parse is never at the goal
// and never behind it: addonDone refuses it with an error that names the
// add-on and the value.
func TestAddonDone_RefusesBadVersions(t *testing.T) {
	tests := []struct {
		kind          Kind
		version, goal string
		bad           string
	}{
		{ClusterRollback, "garbage", "garbage", "garbage"},
		{ClusterUpgrade, "garbage", "garbage", "garbage"},
		{ClusterUpgrade, "", "v1.19.0-eksbuild.1", ""},
		{ClusterRollback, "", "v1.19.0-eksbuild.1", ""},
		{ClusterUpgrade, "v1.19.0-eksbuild.1", "", ""},
		{ClusterRollback, "v1.19.0-eksbuild.1", "latest", "latest"},
		{ClusterUpgrade, "v", "v1.19.0-eksbuild.1", "v"},
	}
	for _, tt := range tests {
		done, err := addonDone(tt.kind, "vpc-cni", tt.version, tt.goal)
		var bad *AddonVersionError
		if done || !errors.As(err, &bad) || bad.Addon != "vpc-cni" || bad.Version != tt.bad {
			t.Errorf("addonDone(%s, %q, %q) = %v, %v; want false and an AddonVersionError for %q", tt.kind, tt.version, tt.goal, done, err, tt.bad)
		}
	}
}

func TestAddonDone(t *testing.T) {
	tests := []struct {
		kind          Kind
		version, goal string
		want          bool
	}{
		{ClusterUpgrade, "v1.19.0-eksbuild.1", "v1.19.0-eksbuild.1", true},
		{ClusterUpgrade, "v1.20.0-eksbuild.1", "v1.19.0-eksbuild.1", true},
		{ClusterUpgrade, "v1.18.0-eksbuild.1", "v1.19.0-eksbuild.1", false},
		{ClusterRollback, "v1.19.0-eksbuild.1", "v1.19.0-eksbuild.1", true},
		{ClusterRollback, "v1.18.0-eksbuild.1", "v1.19.0-eksbuild.1", false},
		{ClusterRollback, "v1.20.0-eksbuild.1", "v1.19.0-eksbuild.1", false},
		{ClusterUpgrade, "1.19.0", "v1.19.0", true},
	}
	for _, tt := range tests {
		if done, err := addonDone(tt.kind, "vpc-cni", tt.version, tt.goal); done != tt.want || err != nil {
			t.Errorf("addonDone(%s, %q, %q) = %v, %v; want %v, nil", tt.kind, tt.version, tt.goal, done, err, tt.want)
		}
	}
}
