package nodegroup

import (
	"bytes"
	"context"
	"strings"
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

// On `nodegroup update`, EKS_CLUSTER_NAME never overrides a cluster that is
// unambiguously on the command line: `nodegroup update prod --nodegroup ng-a`
// with EKS_CLUSTER_NAME=staging rolls ng-a on prod, not staging. When a lone
// positional could be the nodegroup, the old meaning stays (env cluster,
// positional nodegroup) and a note names the env cluster.
func TestUpdateClusterEnvVarPrecedence(t *testing.T) {
	tests := []struct {
		name          string
		env           string
		argv          []string
		wantCluster   string
		wantNodegroup string
		wantNote      bool
	}{
		{name: "env + positional + --nodegroup", env: "staging",
			argv: []string{"prod", "--nodegroup", "ng-a"}, wantCluster: "prod", wantNodegroup: "ng-a"},
		{name: "env + positional + -n before it", env: "staging",
			argv: []string{"-n", "ng-a", "prod"}, wantCluster: "prod", wantNodegroup: "ng-a"},
		{name: "env + two positionals", env: "staging",
			argv: []string{"prod", "ng-a"}, wantCluster: "prod", wantNodegroup: "ng-a"},
		{name: "env + --cluster", env: "staging",
			argv: []string{"-c", "prod", "ng-a"}, wantCluster: "prod", wantNodegroup: "ng-a"},
		{name: "env + one positional is the nodegroup", env: "staging",
			argv: []string{"ng-a"}, wantCluster: "staging", wantNodegroup: "ng-a", wantNote: true},
		{name: "env + --nodegroup only", env: "staging",
			argv: []string{"--nodegroup", "ng-a"}, wantCluster: "staging", wantNodegroup: "ng-a", wantNote: true},
		{name: "env only", env: "staging",
			argv: nil, wantCluster: "staging", wantNodegroup: "", wantNote: true},
		{name: "one positional, no env",
			argv: []string{"prod"}, wantCluster: "prod", wantNodegroup: ""},
		{name: "two positionals, no env",
			argv: []string{"prod", "ng-a"}, wantCluster: "prod", wantNodegroup: "ng-a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(clusterEnvVar, tt.env)
			var note bytes.Buffer
			prev := clusterEnvNoteOut
			clusterEnvNoteOut = &note
			t.Cleanup(func() { clusterEnvNoteOut = prev })

			cmd := parseRealSubcommand(t, "update", tt.argv...)
			gotCluster, gotNodegroup := updateClusterAndNodegroupPatterns(cmd)
			if gotCluster != tt.wantCluster || gotNodegroup != tt.wantNodegroup {
				t.Fatalf("cluster, nodegroup = %q, %q; want %q, %q",
					gotCluster, gotNodegroup, tt.wantCluster, tt.wantNodegroup)
			}
			wantNote := ""
			if tt.wantNote {
				wantNote = "Using cluster staging from EKS_CLUSTER_NAME\n"
			}
			// The note is a neutral status line: a glyph (Unicode or ASCII,
			// by locale) and then the text.
			got := note.String()
			if (wantNote == "") != (got == "") || !strings.HasSuffix(got, wantNote) || strings.Count(got, "\n") > 1 {
				t.Errorf("stderr note = %q, want a status line ending in %q", got, wantNote)
			}
		})
	}
}

// Only `nodegroup update` reads EKS_CLUSTER_NAME. A read-only command must
// not pick it up, so an env var exported for other tools cannot pick a target.
func TestDescribeIgnoresClusterEnvVar(t *testing.T) {
	t.Setenv(clusterEnvVar, "staging")
	for _, tc := range []struct {
		argv        []string
		wantCluster string
	}{
		{argv: []string{"--nodegroup", "ng-a"}, wantCluster: ""},
		{argv: []string{"prod", "ng-a"}, wantCluster: "prod"},
	} {
		cmd := parseRealSubcommand(t, "describe", tc.argv...)
		if got := runner.RequestedCluster(cmd); got != tc.wantCluster {
			t.Errorf("describe %v: cluster = %q, want %q", tc.argv, got, tc.wantCluster)
		}
	}
}
