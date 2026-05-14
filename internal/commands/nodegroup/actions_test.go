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
	return cli.NewContext(cli.NewApp(), set, nil)
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

func TestUpdateBoolFlagReadsTrailingHealthOnly(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "parsed flag before args", args: []string{"--health-only", "develop"}, want: true},
		{name: "short flag after cluster", args: []string{"develop", "-H"}, want: true},
		{name: "long flag after cluster", args: []string{"develop", "--health-only"}, want: true},
		{name: "not set", args: []string{"develop"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newUpdateParseTestContext(t, tt.args, "", "")
			if got := updateBoolFlag(ctx, "health-only", "H"); got != tt.want {
				t.Fatalf("updateBoolFlag() = %v, want %v", got, tt.want)
			}
		})
	}
}
