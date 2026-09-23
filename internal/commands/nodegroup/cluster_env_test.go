package nodegroup

import (
	"context"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
)

// parseRealSubcommand parses argv with the real `nodegroup <sub>` definition
// (its flags and their Sources) and returns the parsed command without
// running the action.
func parseRealSubcommand(t *testing.T, sub string, argv ...string) *cli.Command {
	t.Helper()
	root := Command()
	var captured *cli.Command
	found := false
	for _, c := range root.Commands {
		if c.Name == sub {
			found = true
			c.Action = func(_ context.Context, cmd *cli.Command) error {
				captured = cmd
				return nil
			}
		}
	}
	if !found {
		t.Fatalf("no nodegroup subcommand %q", sub)
	}
	if err := root.Run(t.Context(), append([]string{"nodegroup", sub}, argv...)); err != nil {
		t.Fatal(err)
	}
	if captured == nil {
		t.Fatal("action was not invoked")
	}
	return captured
}

// EKS_CLUSTER_NAME is a fallback: a cluster typed on the command line (flag
// or positional) always wins, and an env cluster never takes the positional
// slot, so `nodegroup update prod --nodegroup ng-a` with EKS_CLUSTER_NAME=
// staging rolls ng-a on prod, not staging.
func TestClusterEnvVarPrecedence(t *testing.T) {
	type resolver func(*cli.Command) (cluster, nodegroup string)
	update := func(cmd *cli.Command) (string, string) { return updateClusterAndNodegroupPatterns(cmd) }
	describe := func(cmd *cli.Command) (string, string) {
		return runner.RequestedCluster(cmd), runner.PositionalSlot(cmd, "nodegroup", "cluster")
	}

	tests := []struct {
		name          string
		sub           string // update is mutating, describe is read-only
		resolve       resolver
		env           string
		argv          []string
		wantCluster   string
		wantNodegroup string
	}{
		{name: "update env+positional", sub: "update", resolve: update, env: "staging",
			argv: []string{"prod", "--nodegroup", "ng-a"}, wantCluster: "prod", wantNodegroup: "ng-a"},
		{name: "update env+positional, no nodegroup", sub: "update", resolve: update, env: "staging",
			argv: []string{"prod"}, wantCluster: "prod", wantNodegroup: ""},
		{name: "update env+positional cluster and nodegroup", sub: "update", resolve: update, env: "staging",
			argv: []string{"prod", "ng-a"}, wantCluster: "prod", wantNodegroup: "ng-a"},
		{name: "update env+flag", sub: "update", resolve: update, env: "staging",
			argv: []string{"-c", "prod", "ng-a"}, wantCluster: "prod", wantNodegroup: "ng-a"},
		{name: "update env only", sub: "update", resolve: update, env: "staging",
			argv: []string{"--nodegroup", "ng-a"}, wantCluster: "staging", wantNodegroup: "ng-a"},
		{name: "update positional only", sub: "update", resolve: update,
			argv: []string{"prod", "ng-a"}, wantCluster: "prod", wantNodegroup: "ng-a"},

		{name: "describe env+positional", sub: "describe", resolve: describe, env: "staging",
			argv: []string{"prod", "ng-a"}, wantCluster: "prod", wantNodegroup: "ng-a"},
		{name: "describe env+flag", sub: "describe", resolve: describe, env: "staging",
			argv: []string{"--cluster", "prod", "ng-a"}, wantCluster: "prod", wantNodegroup: "ng-a"},
		{name: "describe env only", sub: "describe", resolve: describe, env: "staging",
			argv: []string{"--nodegroup", "ng-a"}, wantCluster: "staging", wantNodegroup: "ng-a"},
		{name: "describe positional only", sub: "describe", resolve: describe,
			argv: []string{"prod", "ng-a"}, wantCluster: "prod", wantNodegroup: "ng-a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(runner.ClusterEnvVar, tt.env)
			cmd := parseRealSubcommand(t, tt.sub, tt.argv...)
			gotCluster, gotNodegroup := tt.resolve(cmd)
			if gotCluster != tt.wantCluster || gotNodegroup != tt.wantNodegroup {
				t.Fatalf("cluster, nodegroup = %q, %q; want %q, %q",
					gotCluster, gotNodegroup, tt.wantCluster, tt.wantNodegroup)
			}
		})
	}
}
