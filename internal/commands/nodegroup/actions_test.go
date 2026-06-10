package nodegroup

import (
	"flag"
	"testing"

	"github.com/urfave/cli/v2"
)

func newUpdateParseTestContext(t *testing.T, args []string, clusterFlag, nodegroupFlag string) *cli.Context {
	t.Helper()
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	set.String("cluster", "", "")
	set.String("nodegroup", "", "")
	set.Bool("health-only", false, "")
	if clusterFlag != "" {
		if err := set.Set("cluster", clusterFlag); err != nil {
			t.Fatal(err)
		}
	}
	if nodegroupFlag != "" {
		if err := set.Set("nodegroup", nodegroupFlag); err != nil {
			t.Fatal(err)
		}
	}
	if err := set.Parse(args); err != nil {
		t.Fatal(err)
	}
	ctx := cli.NewContext(cli.NewApp(), set, nil)
	// Attach flag definitions so the runner's trailing-flag handling knows
	// which tokens are flags and whether they take values (the real command
	// dispatcher populates this).
	ctx.Command = &cli.Command{Flags: []cli.Flag{
		&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}},
		&cli.StringFlag{Name: "nodegroup", Aliases: []string{"n"}},
		&cli.BoolFlag{Name: "health-only", Aliases: []string{"H"}},
	}}
	return ctx
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
			ctx := newUpdateParseTestContext(t, tt.args, tt.clusterFlag, tt.nodegroupFlag)
			gotCluster, gotNodegroup := updateClusterAndNodegroupPatterns(ctx)
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
			ctx := newUpdateParseTestContext(t, tt.args, "", "")
			if got := readUpdateAMIFlags(ctx).healthOnly; got != tt.want {
				t.Fatalf("readUpdateAMIFlags().healthOnly = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTrailingValueFlagNotMistakenForPositional(t *testing.T) {
	// `update-ami my-cluster --nodegroup groupC` leaves the flag tokens in
	// Args; the nodegroup slot must read groupC from the flag, and the flag's
	// value must not be parsed as a positional.
	ctx := newUpdateParseTestContext(t, []string{"develop", "--nodegroup", "groupC"}, "", "")
	gotCluster, gotNodegroup := updateClusterAndNodegroupPatterns(ctx)
	if gotCluster != "develop" || gotNodegroup != "groupC" {
		t.Fatalf("updateClusterAndNodegroupPatterns() = %q, %q; want %q, %q",
			gotCluster, gotNodegroup, "develop", "groupC")
	}
}
