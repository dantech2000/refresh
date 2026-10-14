package addon_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// A read command that takes its cluster from the kubeconfig also takes
// the region of that cluster from it: the region of the AWS configuration
// may be another one. -r/--region still wins. A context that is not an
// EKS cluster names no cluster.
func TestClusterFromKubeconfig(t *testing.T) {
	const west = "eu-west-1"
	const eks = "apiVersion: v1\nkind: Config\ncurrent-context: c\n" +
		"clusters:\n- name: arn:aws:eks:eu-west-1:123456789012:cluster/prod-eu\n  cluster: {server: https://ABC.gr7.eu-west-1.eks.amazonaws.com}\n" +
		"contexts:\n- name: c\n  context: {cluster: \"arn:aws:eks:eu-west-1:123456789012:cluster/prod-eu\", user: u}\n" +
		"users:\n- name: u\n  user: {}\n"
	const kind = "apiVersion: v1\nkind: Config\ncurrent-context: kind-local\n" +
		"clusters:\n- name: kind-local\n  cluster: {server: https://127.0.0.1:6443}\n" +
		"contexts:\n- name: kind-local\n  context: {cluster: kind-local, user: u}\n" +
		"users:\n- name: u\n  user: {}\n"
	tests := []struct {
		name       string
		kubeconfig string
		args       []string
		wantRegion string
		wantErr    string
	}{
		{name: "the region of the kubeconfig", kubeconfig: eks, wantRegion: west},
		{name: "the region flag wins", kubeconfig: eks, args: []string{"-r", east}, wantErr: `cluster "prod-eu" not found in us-east-1`},
		{name: "a context that is not EKS", kubeconfig: kind, wantErr: "no cluster specified"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			world := fleettest.New()
			world.Edit(func(s *fleettest.State) {
				s.Clusters = append(s.Clusters,
					&fleettest.Cluster{Name: "prod", Region: east, Version: "1.33"},
					&fleettest.Cluster{Name: "prod-eu", Region: west, Version: "1.33"},
				)
			})
			// The AWS configuration of the run names us-east-1.
			e := cmdtest.NewFake(t, world, east)
			path := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(path, []byte(tt.kubeconfig), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KUBECONFIG", path)

			err := e.Run(cmdutiltest.Context(t), append([]string{"addon", "list", "-o", "plain"}, tt.args...)...)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one with %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			for _, r := range e.Server.Requests() {
				if r.Region != tt.wantRegion {
					t.Errorf("%s was sent to %s, want %s", r.Operation, r.Region, tt.wantRegion)
				}
			}
		})
	}
}
