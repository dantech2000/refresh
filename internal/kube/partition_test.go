package kube_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dantech2000/refresh/internal/kube"
)

// The ARN of a cluster in GovCloud or China, as "aws eks
// update-kubeconfig" writes it there, names the cluster and its region
// like a commercial one.
func TestCurrentCluster_PartitionARNs(t *testing.T) {
	tests := []struct{ arn, name, region string }{
		{"arn:aws-us-gov:eks:us-gov-west-1:123456789012:cluster/gov-prod", "gov-prod", "us-gov-west-1"},
		{"arn:aws-us-gov:eks:us-gov-east-1:123456789012:cluster/gov-east", "gov-east", "us-gov-east-1"},
		{"arn:aws-cn:eks:cn-north-1:123456789012:cluster/cn-prod", "cn-prod", "cn-north-1"},
		{"arn:aws-cn:eks:cn-northwest-1:123456789012:cluster/cn-nw", "cn-nw", "cn-northwest-1"},
	}
	for _, tt := range tests {
		t.Run(tt.region, func(t *testing.T) {
			config := "apiVersion: v1\nkind: Config\ncurrent-context: " + tt.arn + "\n" +
				"clusters:\n- name: " + tt.arn + "\n  cluster: {server: https://example.invalid}\n" +
				"contexts:\n- name: " + tt.arn + "\n  context: {cluster: \"" + tt.arn + "\", user: u}\n" +
				"users:\n- name: u\n  user: {token: t}\n"
			path := filepath.Join(t.TempDir(), "kubeconfig")
			if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			name, region, ok, err := kube.CurrentCluster(path, "")
			if err != nil || !ok || name != tt.name || region != tt.region {
				t.Errorf("CurrentCluster = %q, %q, %v, %v; want %q, %q", name, region, ok, err, tt.name, tt.region)
			}
		})
	}
}
