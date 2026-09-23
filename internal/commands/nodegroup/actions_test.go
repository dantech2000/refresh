package nodegroup

import (
	"context"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	appconfig "github.com/dantech2000/refresh/internal/config"
)

// parseUpdateTestCommand runs a throwaway command through the real v3 parser
// (flags after positionals are parsed natively) and returns the parsed
// *cli.Command captured from the action.
func parseUpdateTestCommand(t *testing.T, args []string, clusterFlag, nodegroupFlag string) *cli.Command {
	t.Helper()
	t.Setenv(clusterEnvVar, "") // an exported EKS_CLUSTER_NAME must not change these cases
	var captured *cli.Command
	cmd := &cli.Command{
		Name: "test",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}},
			&cli.StringFlag{Name: "nodegroup", Aliases: []string{"n"}},
			&cli.BoolFlag{Name: "health-only", Aliases: []string{"H"}},
			&cli.DurationFlag{Name: "poll-interval", Aliases: []string{"p"}, Value: appconfig.DefaultPollInterval},
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
			flags, err := readUpdateAMIFlags(cmd)
			if err != nil {
				t.Fatalf("readUpdateAMIFlags: %v", err)
			}
			if got := flags.healthOnly; got != tt.want {
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

// A zero or negative --poll-interval would panic the monitor's ticker after
// the roll started; it must fail in flag parsing, before any AWS call.
func TestReadUpdateAMIFlagsRejectsNonPositivePollInterval(t *testing.T) {
	for _, v := range []string{"0s", "-5s"} {
		cmd := parseUpdateTestCommand(t, []string{"develop", "--poll-interval", v}, "", "")
		if _, err := readUpdateAMIFlags(cmd); err == nil || !strings.Contains(err.Error(), "--poll-interval must be greater than 0") {
			t.Errorf("--poll-interval %s: err = %v, want a validation error", v, err)
		}
	}
	flags, err := readUpdateAMIFlags(parseUpdateTestCommand(t, []string{"develop"}, "", ""))
	if err != nil || flags.pollInterval != appconfig.DefaultPollInterval {
		t.Errorf("default: pollInterval = %v, err = %v; want %v, nil", flags.pollInterval, err, appconfig.DefaultPollInterval)
	}
}

// The real update command must reject --poll-interval 0 before it touches AWS.
func TestUpdateCommandRejectsZeroPollIntervalBeforeAWS(t *testing.T) {
	err := updateAMICommand().Run(context.Background(), []string{"update", "develop", "--poll-interval", "0"})
	if err == nil || !strings.Contains(err.Error(), "--poll-interval must be greater than 0") {
		t.Fatalf("err = %v, want the poll-interval validation error", err)
	}
}
