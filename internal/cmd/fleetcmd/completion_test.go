package fleetcmd_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

const east = "us-east-1"

func completionFake(t *testing.T) *cmdtest.Fake {
	t.Helper()
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		s.Clusters = append(s.Clusters,
			&fleettest.Cluster{Name: "prod", Region: east, Version: "1.33",
				Nodegroups: []*fleettest.Nodegroup{{Name: "web", Version: "1.33"}, {Name: "api", Version: "1.33"}},
				Addons:     []*fleettest.Addon{{Name: "vpc-cni", Version: "v1"}, {Name: "coredns", Version: "v1"}}},
			&fleettest.Cluster{Name: "dev", Region: east, Version: "1.33"},
		)
	})
	return cmdtest.NewFake(t, world, east)
}

// complete runs the hidden command that a shell calls for completions and
// returns the names that it offers, without the directive line.
func complete(t *testing.T, e *cmdtest.Fake, args ...string) []string {
	t.Helper()
	if err := e.Run(cmdutiltest.Context(t), append([]string{"__complete"}, args...)...); err != nil {
		t.Fatalf("__complete: %v", err)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(e.Out.String()), "\n") {
		if line != "" && !strings.HasPrefix(line, ":") {
			names = append(names, line)
		}
	}
	return names
}

func TestCompletion(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  string // REFRESH_CLUSTER
		want []string
	}{
		{"the cluster argument", []string{"cluster", "describe", ""}, "", []string{"dev", "prod"}},
		{"only the first argument", []string{"cluster", "describe", "prod", ""}, "", nil},
		{"the cluster flag", []string{"nodegroup", "list", "-c", ""}, "", []string{"dev", "prod"}},
		{"the cluster flag of describe", []string{"addon", "describe", "--cluster", ""}, "", []string{"dev", "prod"}},
		{"the nodegroups of the cluster of the flag", []string{"nodegroup", "describe", "-c", "prod", ""}, "", []string{"api", "web"}},
		{"the add-ons of the cluster of the environment", []string{"addon", "describe", ""}, "prod", []string{"coredns", "vpc-cni"}},
		{"the formats", []string{"cluster", "list", "-o", ""}, "", []string{"table", "json", "yaml", "plain"}},
		{"the sort keys", []string{"cluster", "list", "--sort", ""}, "", []string{"name", "region", "status", "version"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := completionFake(t)
			t.Setenv(cmdutil.EnvCluster, tt.env)
			if diff := cmp.Diff(tt.want, complete(t, e, tt.args...)); diff != "" {
				t.Errorf("completions (-want +got):\n%s", diff)
			}
			if strings.Contains(e.Err.String(), "rror") {
				t.Errorf("stderr = %q, want no error", e.Err.String())
			}
		})
	}
}

// A completion that cannot get its names offers none. It prints no error:
// the shell would put the text on the command line.
func TestCompletion_SilentOnFailure(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		fault func(*fleettest.World)
	}{
		{"AWS denies the list", []string{"cluster", "describe", ""}, func(w *fleettest.World) {
			w.SetFault(fleettest.EKSListClusters, fleettest.Fault{Deny: true})
		}},
		{"the cluster does not exist", []string{"nodegroup", "describe", "-c", "nope", ""}, nil},
		// The kubeconfig is not asked for a cluster, and none is set.
		{"no cluster", []string{"nodegroup", "describe", ""}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := completionFake(t)
			if tt.fault != nil {
				tt.fault(e.World)
			}
			if got := complete(t, e, tt.args...); len(got) != 0 {
				t.Errorf("completions = %v, want none", got)
			}
			// Cobra writes one line about the directive to stderr, which
			// the shell does not show.
			if strings.Contains(e.Err.String(), "rror") || strings.Contains(e.Out.String(), "rror") {
				t.Errorf("the completion printed an error: stdout %q, stderr %q", e.Out.String(), e.Err.String())
			}
		})
	}
}
