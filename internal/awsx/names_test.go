package awsx

import (
	"strings"
	"testing"
)

func TestCheckNames(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	tests := []struct {
		name    string
		check   func(string) error
		value   string
		wantErr string // "" for a valid name
	}{
		{name: "cluster", check: CheckClusterName, value: "prod-eks_1"},
		{name: "cluster that starts with a digit", check: CheckClusterName, value: "1-18"},
		{name: "cluster of 100 characters", check: CheckClusterName, value: long(100)},
		{
			name: "cluster of 101 characters", check: CheckClusterName, value: long(101),
			wantErr: `invalid cluster name "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"... (101 characters): a name has 1 to 100 characters, only letters, digits, hyphens, and underscores, and starts with a letter or digit`,
		},
		{
			name: "cluster with a space", check: CheckClusterName, value: "my cluster",
			wantErr: `invalid cluster name "my cluster": a name has 1 to 100 characters`,
		},
		{name: "cluster with Unicode", check: CheckClusterName, value: "prod-é", wantErr: `invalid cluster name "prod-é"`},
		{name: "cluster that starts with a hyphen", check: CheckClusterName, value: "-prod", wantErr: "invalid cluster name"},
		{name: "empty cluster", check: CheckClusterName, value: "", wantErr: `invalid cluster name ""`},
		{name: "nodegroup", check: CheckNodegroupName, value: "web_1-a"},
		{name: "nodegroup of 63 characters", check: CheckNodegroupName, value: long(63)},
		{
			name: "nodegroup of 64 characters", check: CheckNodegroupName, value: long(64),
			wantErr: "a name has 1 to 63 characters",
		},
		{name: "nodegroup with a dot", check: CheckNodegroupName, value: "web.1", wantErr: `invalid nodegroup name "web.1"`},
		{name: "add-on", check: CheckAddonName, value: "aws-ebs-csi-driver"},
		{name: "marketplace add-on", check: CheckAddonName, value: "solo-io_istio-distro"},
		{name: "add-on with a slash", check: CheckAddonName, value: "core/dns", wantErr: `invalid add-on name "core/dns": a name has 1 to 100 characters`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.check(tt.value)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want one that contains %q", err, tt.wantErr)
			}
		})
	}
}
