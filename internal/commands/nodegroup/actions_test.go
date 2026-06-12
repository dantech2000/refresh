package nodegroup

import (
	"context"
	"testing"

	"github.com/urfave/cli/v3"
)

// parseUpdateTestCommand runs a throwaway command through the real v3 parser
// (flags after positionals are parsed natively) and returns the parsed
// *cli.Command captured from the action.
func parseUpdateTestCommand(t *testing.T, args []string, clusterFlag, nodegroupFlag string) *cli.Command {
	t.Helper()
	var captured *cli.Command
	cmd := &cli.Command{
		Name: "test",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}},
			&cli.StringFlag{Name: "nodegroup", Aliases: []string{"n"}},
			&cli.BoolFlag{Name: "health-only", Aliases: []string{"H"}},
		},
		Action: func(_ context.Context, c *cli.Command) error {
			captured = c
			return nil
		},
	}
	argv := []string{"test"}
	if clusterFlag != "" {
		argv = append(argv, "--cluster", clusterFlag)
	}
	if nodegroupFlag != "" {
		argv = append(argv, "--nodegroup", nodegroupFlag)
	}
	argv = append(argv, args...)
	if err := cmd.Run(context.Background(), argv); err != nil {
		t.Fatal(err)
	}
	if captured == nil {
		t.Fatal("command action was not invoked")
	}
	return captured
}

func TestUpdateClusterAndNodegroupPatterns(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		clusterFlag   string
		nodegroupFlag string
		wantCluster   string
		wantNodegroup string
	}{
		{
			name:        "positional cluster",
			args:        []string{"develop"},
			wantCluster: "develop",
		},
		{
			name:          "positional cluster and nodegroup",
			args:          []string{"develop", "groupC"},
			wantCluster:   "develop",
			wantNodegroup: "groupC",
		},
		{
			name:          "cluster flag and positional nodegroup",
			args:          []string{"groupC"},
			clusterFlag:   "develop",
			wantCluster:   "develop",
			wantNodegroup: "groupC",
		},
		{
			name:          "flags win",
			args:          []string{"ignored-cluster", "ignored-nodegroup"},
			clusterFlag:   "develop",
			nodegroupFlag: "groupD",
			wantCluster:   "develop",
			wantNodegroup: "groupD",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := parseUpdateTestCommand(t, tt.args, tt.clusterFlag, tt.nodegroupFlag)
			gotCluster, gotNodegroup := updateClusterAndNodegroupPatterns(cmd)
			if gotCluster != tt.wantCluster || gotNodegroup != tt.wantNodegroup {
				t.Fatalf("updateClusterAndNodegroupPatterns() = %q, %q; want %q, %q",
					gotCluster, gotNodegroup, tt.wantCluster, tt.wantNodegroup)
			}
		})
	}
}

func TestReadUpdateAMIFlagsReadsTrailingHealthOnly(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "short flag after cluster", args: []string{"develop", "-H"}, want: true},
		{name: "long flag after cluster", args: []string{"develop", "--health-only"}, want: true},
		{name: "not set", args: []string{"develop"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := parseUpdateTestCommand(t, tt.args, "", "")
			if got := readUpdateAMIFlags(cmd).healthOnly; got != tt.want {
				t.Fatalf("readUpdateAMIFlags().healthOnly = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTrailingValueFlagNotMistakenForPositional(t *testing.T) {
	// `update-ami my-cluster --nodegroup groupC` parses the trailing flag; the
	// nodegroup slot must read groupC from the flag, and the flag's value must
	// not be parsed as a positional.
	cmd := parseUpdateTestCommand(t, []string{"develop", "--nodegroup", "groupC"}, "", "")
	gotCluster, gotNodegroup := updateClusterAndNodegroupPatterns(cmd)
	if gotCluster != "develop" || gotNodegroup != "groupC" {
		t.Fatalf("updateClusterAndNodegroupPatterns() = %q, %q; want %q, %q",
			gotCluster, gotNodegroup, "develop", "groupC")
	}
}
