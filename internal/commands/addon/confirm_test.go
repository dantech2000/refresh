package addon

import (
	"context"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// withPrompt is withTTY that also counts the prompts asked.
func withPrompt(t *testing.T, tty bool, answer string) *int {
	t.Helper()
	asked := new(int)
	origTTY, origPrompt := runner.StdinIsTerminal, runner.PromptLine
	runner.StdinIsTerminal = func() bool { return tty }
	runner.PromptLine = func(context.Context) (string, error) { *asked++; return answer, nil }
	t.Cleanup(func() { runner.StdinIsTerminal, runner.PromptLine = origTTY, origPrompt })
	return asked
}

// addon update asks before it submits an update (REF-164). The prompt names
// both versions and the cluster; "no" submits nothing. --yes skips it; with
// -o json or without a terminal --yes is required, checked before any AWS
// call; --dry-run and an add-on already at the target never prompt.
func TestUpdate_Confirmation(t *testing.T) {
	world := func() *fakeaws.Cluster {
		return addonCluster(&fakeaws.Addon{Name: "coredns", Version: "v1.11.1", Available: []string{"v1.11.4", "v1.11.1"}})
	}
	cases := []struct {
		name       string
		tty        bool
		answer     string
		args       []string
		upToDate   bool
		wantErr    string
		wantAsked  int
		wantUpdate bool
		wantNoAWS  bool
	}{
		{name: "tty yes", tty: true, answer: "y", wantAsked: 1, wantUpdate: true},
		{name: "tty no", tty: true, answer: "n", wantAsked: 1, wantErr: "cancelled"},
		{name: "--yes skips the prompt", tty: true, args: []string{"--yes"}, wantUpdate: true},
		{name: "no tty needs --yes", wantErr: "no interactive terminal; add --yes", wantNoAWS: true},
		{name: "-o json needs --yes even on a tty", tty: true, args: []string{"-o", "json"}, wantErr: "-o json does not prompt for confirmation; add --yes", wantNoAWS: true},
		{name: "-o json with -y", args: []string{"-o", "json", "-y"}, wantUpdate: true},
		{name: "dry run never prompts", args: []string{"-d"}},
		{name: "up to date is not asked about", tty: true, answer: "n", upToDate: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asked := withPrompt(t, tc.tty, tc.answer)
			w := world()
			if tc.upToDate {
				w.Addons[0].Version = "v1.11.4"
			}
			srv := fakeaws.New(t, w)
			_, stderr, err := runAddon(t, append([]string{"update", "prod", "coredns"}, tc.args...)...)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q\nstderr:\n%s", err, tc.wantErr, stderr)
				}
			} else if err != nil {
				t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
			}
			if *asked != tc.wantAsked {
				t.Errorf("prompts = %d, want %d", *asked, tc.wantAsked)
			}
			if tc.wantAsked > 0 && !strings.Contains(stderr, "Update coredns v1.11.1 → v1.11.4 on prod? [y/N]") {
				t.Errorf("stderr = %q, want the update prompt", stderr)
			}
			if got := updateCalls(srv) == 1; got != tc.wantUpdate {
				t.Errorf("UpdateAddon calls = %d, want update %v", updateCalls(srv), tc.wantUpdate)
			}
			if tc.wantNoAWS && len(srv.Calls()) != 0 {
				t.Errorf("AWS called before the --yes check: %v", srv.Calls())
			}
		})
	}
}

// A pinned downgrade shows its warning once, before the prompt.
func TestUpdate_ConfirmationShowsDowngradeWarningOnce(t *testing.T) {
	withPrompt(t, true, "y")
	fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "vpc-cni", Version: "v1.19.0", Available: []string{"v1.19.0", "v1.18.0"}}))
	_, stderr, err := runAddon(t, "update", "prod", "vpc-cni", "v1.18.0")
	if err != nil {
		t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
	}
	warn := "warning: downgrading vpc-cni from v1.19.0 to v1.18.0"
	if n := strings.Count(stderr, warn); n != 1 {
		t.Errorf("downgrade warning shown %d times, want 1:\n%s", n, stderr)
	}
	if strings.Index(stderr, warn) > strings.Index(stderr, "[y/N]") {
		t.Errorf("warning comes after the prompt:\n%s", stderr)
	}
}

// addon update --all asks once, listing every add-on that would change.
func TestUpdateAll_Confirmation(t *testing.T) {
	world := func() *fakeaws.Cluster {
		return addonCluster(
			&fakeaws.Addon{Name: "coredns", Version: "v1.11.1", Available: []string{"v1.11.4", "v1.11.1"}},
			&fakeaws.Addon{Name: "vpc-cni", Version: "v1.19.0", Available: []string{"v1.19.0"}},
		)
	}
	t.Run("no", func(t *testing.T) {
		asked := withPrompt(t, true, "n")
		srv := fakeaws.New(t, world())
		_, stderr, err := runAddon(t, "update", "prod", "--all")
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("err = %v, want cancelled\nstderr:\n%s", err, stderr)
		}
		if *asked != 1 {
			t.Errorf("prompts = %d, want 1", *asked)
		}
		for _, want := range []string{"coredns v1.11.1 → v1.11.4", "Update 1 add-on(s) on prod? [y/N]"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr missing %q:\n%s", want, stderr)
			}
		}
		if strings.Contains(stderr, "vpc-cni v1.19.0 →") {
			t.Errorf("an up-to-date add-on is listed as a change:\n%s", stderr)
		}
		if n := updateCalls(srv); n != 0 {
			t.Errorf("UpdateAddon calls = %d, want 0", n)
		}
	})
	t.Run("yes", func(t *testing.T) {
		withPrompt(t, true, "y")
		srv := fakeaws.New(t, world())
		if _, stderr, err := runAddon(t, "update", "prod", "--all"); err != nil {
			t.Fatalf("update --all: %v\nstderr:\n%s", err, stderr)
		}
		if n := updateCalls(srv); n != 1 {
			t.Errorf("UpdateAddon calls = %d, want 1", n)
		}
	})
	t.Run("nothing to change is not asked about", func(t *testing.T) {
		asked := withPrompt(t, true, "n")
		w := world()
		w.Addons[0].Version = "v1.11.4"
		fakeaws.New(t, w)
		if _, stderr, err := runAddon(t, "update", "prod", "--all"); err != nil {
			t.Fatalf("update --all: %v\nstderr:\n%s", err, stderr)
		}
		if *asked != 0 {
			t.Errorf("prompts = %d, want 0", *asked)
		}
	})
	// An add-on the preview could not read would be updated unseen by the
	// real run, so the command fails closed: no prompt, no update.
	t.Run("a failed preview fails closed", func(t *testing.T) {
		asked := withPrompt(t, true, "y")
		w := world()
		w.Addons[1].Version = "v1.18.0"
		w.Addons[1].DescribeAddonError = "AccessDeniedException"
		srv := fakeaws.New(t, w)
		_, stderr, err := runAddon(t, "update", "prod", "--all")
		if err == nil || !strings.Contains(err.Error(), "could not preview every add-on") || !strings.Contains(err.Error(), "vpc-cni") {
			t.Fatalf("err = %v, want the preview failure naming vpc-cni\nstderr:\n%s", err, stderr)
		}
		if *asked != 0 {
			t.Errorf("prompts = %d, want 0", *asked)
		}
		if n := updateCalls(srv); n != 0 {
			t.Errorf("UpdateAddon calls = %d, want 0", n)
		}
	})
	t.Run("hidden update-all needs --yes without a tty", func(t *testing.T) {
		withPrompt(t, false, "")
		srv := fakeaws.New(t, world())
		_, _, err := runAddon(t, "update-all", "prod")
		if err == nil || !strings.Contains(err.Error(), "add --yes") {
			t.Fatalf("err = %v, want the --yes error", err)
		}
		if len(srv.Calls()) != 0 {
			t.Errorf("AWS called before the --yes check: %v", srv.Calls())
		}
	})
}
