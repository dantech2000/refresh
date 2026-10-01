package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/services/upgrade"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui"
)

// --only control-plane moves the control plane alone, and the plan says
// what to run for the rest; --only nodegroups, then --only addons, finish
// the job one part at a time.
func TestUpgrade_OnlyRunsEachPartSeparately(t *testing.T) {
	srv := fakeaws.New(t, orderWorld())
	stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--only", "control-plane", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\nstderr:\n%s", err, stderr)
	}
	out := ui.StripANSI(stdout + stderr)
	for _, want := range []string{"--only nodegroups", "refresh addon update --all -c prod", "kube-proxy"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}

	for _, step := range []struct{ only, want string }{
		{"control-plane", "cp"},
		{"nodegroups", "cp,ng web"},
		{"addons", "cp,ng web,addon kube-proxy,addon vpc-cni"},
	} {
		if _, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--only", step.only, "--yes", "--poll-interval", "5ms", "-o", "json"); err != nil {
			t.Fatalf("--only %s: %v\nstderr:\n%s", step.only, err, stderr)
		}
		if got := strings.Join(mutations(srv.Calls()), ","); got != step.want {
			t.Fatalf("after --only %s: mutations = %s, want %s", step.only, got, step.want)
		}
	}
}

// A bad --only value fails before any AWS call.
func TestUpgrade_OnlyRejectsUnknownParts(t *testing.T) {
	srv := fakeaws.New(t, orderWorld())
	_, _, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--only", "workers", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "control-plane, addons, nodegroups") {
		t.Fatalf("err = %v", err)
	}
	if n := len(srv.Calls()); n != 0 {
		t.Fatalf("%d AWS calls before the flag check:\n%s", n, strings.Join(srv.Calls(), "\n"))
	}
}

// The resume command a stopped --only run prints keeps --only, so a resume
// never widens the run to every part.
func TestUpgrade_ResumeKeepsOnly(t *testing.T) {
	fakeaws.New(t, orderWorld())
	var got string
	cmd := upgradeCommand()
	cmd.Action = func(_ context.Context, c *cli.Command) error {
		got = resumeCommand(c, "prod", &upgrade.Plan{TargetVersion: "1.32"})
		return nil
	}
	if err := cmd.Run(t.Context(), []string{"upgrade", "prod", "--to", "1.32", "--only", "control-plane"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "--only control-plane") {
		t.Fatalf("resume command %q lacks --only", got)
	}
}

// An empty --only (an unset variable) fails instead of running every part.
func TestUpgrade_EmptyOnlyFails(t *testing.T) {
	srv := fakeaws.New(t, orderWorld())
	_, _, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--only", "", "--yes", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "--only needs at least one") {
		t.Fatalf("err = %v", err)
	}
	if got := mutations(srv.Calls()); len(got) != 0 {
		t.Fatalf("mutations = %v", got)
	}
}

// -n rolls one nodegroup to the control plane's version; the other stays.
func TestUpgrade_OneNodegroup(t *testing.T) {
	w := orderWorld()
	w.Version = "1.32"
	w.Nodegroups = append(w.Nodegroups, &fakeaws.Nodegroup{Name: "batch", Version: "1.31"})
	srv := fakeaws.New(t, w)
	if _, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--only", "nodegroups", "-n", "batch", "--yes", "--poll-interval", "5ms", "-o", "json"); err != nil {
		t.Fatalf("upgrade: %v\nstderr:\n%s", err, stderr)
	}
	if got := strings.Join(mutations(srv.Calls()), ","); got != "ng batch" {
		t.Fatalf("mutations = %s, want ng batch", got)
	}
}
