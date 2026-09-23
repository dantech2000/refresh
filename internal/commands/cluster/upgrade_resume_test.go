package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/services/upgrade"
)

// resumeFor parses args as `refresh ... cluster upgrade ...` with the real
// upgrade flags and returns the resume command for the parsed invocation.
func resumeFor(t *testing.T, clusterName string, args ...string) string {
	t.Helper()
	up := upgradeCommand()
	var got string
	up.Action = func(_ context.Context, cmd *cli.Command) error {
		got = resumeCommand(cmd, clusterName, &upgrade.Plan{TargetVersion: "1.33"})
		return nil
	}
	app := fakeaws.App(&cli.Command{Name: "cluster", Commands: []*cli.Command{up}})
	if err := app.Run(t.Context(), append([]string{"refresh"}, args...)); err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return got
}

func TestResumeCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "minimal",
			args: []string{"cluster", "upgrade", "prod", "--to", "1.33"},
			want: "refresh cluster upgrade -c prod --to 1.33",
		},
		{
			name: "skip addons, repeated and short alias",
			args: []string{"cluster", "upgrade", "prod", "--to", "1.33", "--skip", "vpc-cni", "-s", "coredns"},
			want: "refresh cluster upgrade -c prod --to 1.33 --skip vpc-cni --skip coredns",
		},
		{
			name: "skip nodegroups",
			args: []string{"cluster", "upgrade", "prod", "--to", "1.33", "--skip-nodegroup", "gpu", "--skip-nodegroup", "batch"},
			want: "refresh cluster upgrade -c prod --to 1.33 --skip-nodegroup gpu --skip-nodegroup batch",
		},
		{
			name: "force",
			args: []string{"cluster", "upgrade", "prod", "--to", "1.33", "--force"},
			want: "refresh cluster upgrade -c prod --to 1.33 --force",
		},
		{
			name: "yes kept for an unattended run",
			args: []string{"cluster", "upgrade", "prod", "--to", "1.33", "-y"},
			want: "refresh cluster upgrade -c prod --to 1.33 --yes",
		},
		{
			name: "root region and profile before the subcommand",
			args: []string{"--region", "eu-west-1", "--profile", "ops", "cluster", "upgrade", "prod", "--to", "1.33"},
			want: "refresh --profile ops --region eu-west-1 cluster upgrade -c prod --to 1.33",
		},
		{
			name: "root flags given after the subcommand move before it",
			args: []string{"cluster", "upgrade", "prod", "--to", "1.33", "--region", "us-west-2"},
			want: "refresh --region us-west-2 cluster upgrade -c prod --to 1.33",
		},
		{
			name: "values with spaces and quotes are shell-quoted",
			args: []string{"--profile", "my profile", "cluster", "upgrade", "prod", "--to", "1.33", "--skip-nodegroup", "it's", "--skip-nodegroup", "a;b"},
			want: `refresh --profile 'my profile' cluster upgrade -c prod --to 1.33 --skip-nodegroup 'it'\''s' --skip-nodegroup 'a;b'`,
		},
		{
			name: "flags left at their defaults are omitted",
			args: []string{"cluster", "upgrade", "prod", "--to", "1.33", "--dry-run", "--quiet", "--timeout", "1h", "--poll-interval", "5s"},
			want: "refresh cluster upgrade -c prod --to 1.33",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resumeFor(t, "prod", tc.args...); got != tc.want {
				t.Errorf("resume command:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// The resume command names the resolved cluster, not the pattern typed.
func TestResumeCommand_UsesResolvedClusterName(t *testing.T) {
	got := resumeFor(t, "prod-east", "cluster", "upgrade", "-c", "east", "--to", "1.33")
	if want := "refresh cluster upgrade -c prod-east --to 1.33"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":                "''",
		"prod-east_1":     "prod-east_1",
		"1.33":            "1.33",
		"a b":             "'a b'",
		"it's":            `'it'\''s'`,
		"$HOME":           "'$HOME'",
		"x*":              "'x*'",
		"arn:aws:eks:x/y": "arn:aws:eks:x/y",
		"back`tick`":      "'back`tick`'",
		"new\nline":       "'new\nline'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

// End to end: a failed nodegroup roll prints a resume command that carries
// the root --region and every flag that shapes the mutation.
func TestUpgrade_FailedRunPrintsFullResumeCommand(t *testing.T) {
	const want = "refresh --region us-west-2 cluster upgrade -c prod --to 1.32 --skip vpc-cni --skip-nodegroup 'batch jobs' --force --yes"
	world := func() *fakeaws.Cluster {
		return &fakeaws.Cluster{Name: "prod", Version: "1.31", Nodegroups: []*fakeaws.Nodegroup{
			{Name: "web", Version: "1.31", FailUpdate: true},
		}}
	}
	args := []string{"--region", "us-west-2", "cluster", "upgrade", "prod", "--to", "1.32", "--yes", "--force",
		"--skip", "vpc-cni", "--skip-nodegroup", "batch jobs", "--poll-interval", "5ms"}

	t.Run("json", func(t *testing.T) {
		fakeaws.New(t, world())
		stdout, stderr, err := fakeaws.Run(t, fakeaws.App(Command()), append(append([]string{"refresh"}, args...), "-o", "json")...)
		if err == nil {
			t.Fatalf("upgrade succeeded; want the nodegroup roll to fail\nstderr:\n%s", stderr)
		}
		fakeaws.RequireOneDocument(t, "json", stdout)
		if !strings.Contains(err.Error(), "resume with: "+want+")") {
			t.Errorf("error does not carry the full resume command:\n got %v\nwant it to contain %s", err, want)
		}
	})
	t.Run("table", func(t *testing.T) {
		fakeaws.New(t, world())
		stdout, stderr, err := fakeaws.Run(t, fakeaws.App(Command()), append([]string{"refresh"}, args...)...)
		if err == nil {
			t.Fatalf("upgrade succeeded; want the nodegroup roll to fail\nstderr:\n%s", stderr)
		}
		if !strings.Contains(stdout, "Resume with: "+want+"\n") {
			t.Errorf("stdout does not show the full resume command %q:\n%s", want, stdout)
		}
	})
}
