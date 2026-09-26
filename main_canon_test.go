package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/flagcanon"
)

// walkFlags calls fn for every flag of every command in the tree, with the
// command's path without the root name ("cluster describe"; "" for the root).
func walkFlags(app *cli.Command, fn func(path string, c *cli.Command, f cli.Flag)) {
	var walk func(path string, c *cli.Command)
	walk = func(path string, c *cli.Command) {
		for _, f := range c.Flags {
			fn(path, c, f)
		}
		for _, sub := range c.Commands {
			p := sub.Name
			if path != "" {
				p = path + " " + sub.Name
			}
			walk(p, sub)
		}
	}
	walk("", app)
}

// shortNames returns the one-letter names of f.
func shortNames(f cli.Flag) []string {
	var out []string
	for _, n := range f.Names() {
		if len(n) == 1 {
			out = append(out, n)
		}
	}
	return out
}

// Each shorthand letter has one meaning across the whole CLI: a canon letter
// is bound to its canon long name, and any other letter needs an allowlist
// entry (with a reason) that binds it to one long name. A flag with a canon
// long name carries the canon letter. (REF-164)
func TestFlagShorthandCanon(t *testing.T) {
	app := newApp()
	usedExtra := map[string]bool{}
	walkFlags(app, func(path string, _ *cli.Command, f cli.Flag) {
		long := f.Names()[0]
		where := strings.TrimSpace("refresh " + path)
		for _, s := range shortNames(f) {
			if want, ok := flagcanon.Canon[s]; ok {
				if long != want {
					t.Errorf("%s: -%s is bound to --%s, but the canon letter -%s means --%s", where, s, long, s, want)
				}
				continue
			}
			if b, ok := flagcanon.BuiltIn[s]; ok {
				t.Errorf("%s: -%s on --%s reuses urfave/cli's built-in -%s (--%s)", where, s, long, s, b)
				continue
			}
			extra, ok := flagcanon.Allowed[s]
			switch {
			case !ok:
				t.Errorf("%s: shorthand -%s (--%s) is not in the canon; remove it, or add it to flagcanon.Allowed with a reason", where, s, long)
			case extra.Long != long:
				t.Errorf("%s: -%s is bound to --%s, but flagcanon.Allowed binds it to --%s", where, s, long, extra.Long)
			default:
				usedExtra[s] = true
			}
		}
		for letter, canonLong := range flagcanon.Canon {
			if long == canonLong && !slices.Contains(f.Names(), letter) {
				t.Errorf("%s: --%s has no -%s; the canon letter must be added where the meaning exists", where, long, letter)
			}
		}
	})
	for s, extra := range flagcanon.Allowed {
		if strings.TrimSpace(extra.Reason) == "" {
			t.Errorf("flagcanon.Allowed[%q] has no reason", s)
		}
		if !usedExtra[s] {
			t.Errorf("flagcanon.Allowed[%q] (--%s) no longer matches a flag; remove it", s, extra.Long)
		}
	}
}

// Every removed-shorthand entry names a real command and a letter that is
// really gone from it, so the helpful error can fire.
func TestRemovedShorthandTableMatchesTree(t *testing.T) {
	app := newApp()
	paths := map[string]*cli.Command{}
	var walk func(path string, c *cli.Command)
	walk = func(path string, c *cli.Command) {
		paths[path] = c
		for _, sub := range c.Commands {
			p := sub.Name
			if path != "" {
				p = path + " " + sub.Name
			}
			walk(p, sub)
		}
	}
	walk("", app)
	for path, letters := range flagcanon.Removed() {
		c, ok := paths[path]
		if !ok {
			t.Errorf("removed-shorthand table names unknown command %q", path)
			continue
		}
		for letter := range letters {
			for _, anc := range []*cli.Command{c, app} {
				for _, f := range anc.Flags {
					if slices.Contains(f.Names(), letter) {
						t.Errorf("%s: -%s is listed as removed but --%s still defines it", path, letter, f.Names()[0])
					}
				}
			}
		}
	}
}

// A removed shorthand fails with a message that names its replacement, and
// exits 1 through main's error path.
func TestRemovedShorthandErrors(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"cluster", "describe", "prod", "-d"}, "-d was removed from 'cluster describe' in 0.11.0; use --detailed"},
		{[]string{"cluster", "describe", "prod", "-s"}, "-s was removed from 'cluster describe' in 0.11.0; use --show-security"},
		{[]string{"cluster", "describe", "prod", "-a"}, "-a was removed from 'cluster describe' in 0.11.0; add-ons are shown by default; use --no-addons to hide them"},
		{[]string{"cluster", "upgrade", "prod", "--to", "1.33", "-s", "vpc-cni"}, "-s was removed from 'cluster upgrade' in 0.11.0; use --skip"},
		{[]string{"cluster", "upgrade", "prod", "--to", "1.33", "-p", "5s"}, "-p was removed from 'cluster upgrade' in 0.11.0; use --poll-interval"},
		{[]string{"nodegroup", "update", "prod", "-f"}, "-f was removed from 'nodegroup update' in 0.11.0; use --force"},
		{[]string{"ng", "update", "prod", "-s"}, "-s was removed from 'nodegroup update' in 0.11.0; use --skip-health-check"},
		{[]string{"nodegroup", "update", "prod", "-p", "5s"}, "-p was removed from 'nodegroup update' in 0.11.0; use --poll-interval"},
		{[]string{"addon", "update", "prod", "--all", "-p"}, "-p was removed from 'addon update' in 0.11.0; use --parallel"},
		{[]string{"addon", "update", "prod", "--all", "-s", "vpc-cni"}, "-s was removed from 'addon update' in 0.11.0; use --skip"},
		{[]string{"addon", "update-all", "prod", "-p"}, "-p was removed from 'addon update-all' in 0.11.0; use --parallel"},
		{[]string{"context", "add", "prod", "-c", "x", "-p", "ops"}, "-p was removed from 'context add' in 0.11.0; use --profile"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := run(context.Background(), append([]string{"refresh"}, tc.args...), &out, &errOut)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if strings.Contains(errOut.String(), "Incorrect Usage") {
				t.Errorf("stderr shows the generic usage error:\n%s", errOut.String())
			}
		})
	}
}

// An unknown flag that was never a shorthand keeps urfave/cli's usage error
// and help.
func TestUnknownFlagKeepsDefaultUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	err := run(context.Background(), []string{"refresh", "cluster", "describe", "--bogus"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined: -bogus") {
		t.Fatalf("err = %v, want the undefined-flag error", err)
	}
	if !strings.Contains(err.Error(), "run 'refresh cluster describe --help' for usage") {
		t.Errorf("err = %v, want a pointer to --help", err)
	}
	// Stdout stays clean for -o json|yaml: no help page anywhere.
	if out.Len() != 0 || strings.Contains(errOut.String(), "USAGE") {
		t.Errorf("stdout = %q, stderr = %q", out.String(), errOut.String())
	}
}

// probe runs args against the real tree with the target command's action
// replaced, and returns the API and wait timeouts the action would use.
func probeTimeouts(t *testing.T, path []string, deprecated string, args ...string) (api, wait time.Duration, stderr string) {
	t.Helper()
	app := newApp()
	cmd := app
	for _, name := range path {
		cmd = cmd.Command(name)
	}
	cmd.Action = func(_ context.Context, c *cli.Command) error {
		api, wait = runner.APITimeout(c), runner.WaitTimeout(c, deprecated)
		return nil
	}
	var errOut bytes.Buffer
	app.ErrWriter = &errOut
	stderr = captureStderr(t, func() {
		if err := app.Run(context.Background(), append([]string{"refresh"}, args...)); err != nil {
			t.Fatalf("run %v: %v", args, err)
		}
	})
	return api, wait, stderr
}

// --wait-timeout is the canonical wait on nodegroup update, cluster upgrade,
// and nodegroup scale. The old local --timeout/-t (and --op-timeout) still
// set it for one release, with a deprecation warning. The global --timeout
// before the subcommand is the API timeout.
func TestWaitTimeoutAndDeprecatedAliases(t *testing.T) {
	t.Setenv("REFRESH_TIMEOUT", "")
	cases := []struct {
		name       string
		path       []string
		deprecated string
		args       []string
		api, wait  time.Duration
		warn       string
	}{
		{"upgrade default", []string{"cluster", "upgrade"}, "timeout", []string{"cluster", "upgrade", "--to", "1.33"}, 60 * time.Second, 4 * time.Hour, ""},
		{"upgrade --wait-timeout", []string{"cluster", "upgrade"}, "timeout", []string{"cluster", "upgrade", "--to", "1.33", "--wait-timeout", "2h"}, 60 * time.Second, 2 * time.Hour, ""},
		{"upgrade global -t", []string{"cluster", "upgrade"}, "timeout", []string{"-t", "5s", "cluster", "upgrade", "--to", "1.33"}, 5 * time.Second, 4 * time.Hour, ""},
		{"upgrade deprecated --timeout", []string{"cluster", "upgrade"}, "timeout", []string{"cluster", "upgrade", "--to", "1.33", "--timeout", "3h"}, 60 * time.Second, 3 * time.Hour, "warning: --timeout on 'cluster upgrade' is deprecated and will be removed in 0.12.0; use --wait-timeout"},
		{"upgrade deprecated -t", []string{"cluster", "upgrade"}, "timeout", []string{"cluster", "upgrade", "--to", "1.33", "-t", "3h"}, 60 * time.Second, 3 * time.Hour, "use --wait-timeout"},
		{"update default", []string{"nodegroup", "update"}, "timeout", []string{"nodegroup", "update"}, 60 * time.Second, 40 * time.Minute, ""},
		{"update --wait-timeout", []string{"nodegroup", "update"}, "timeout", []string{"nodegroup", "update", "--wait-timeout", "1h"}, 60 * time.Second, time.Hour, ""},
		{"update deprecated --timeout", []string{"nodegroup", "update"}, "timeout", []string{"nodegroup", "update", "--timeout", "1h"}, 60 * time.Second, time.Hour, "warning: --timeout on 'nodegroup update' is deprecated"},
		{"scale deprecated --op-timeout", []string{"nodegroup", "scale"}, "op-timeout", []string{"nodegroup", "scale", "-n", "x", "--op-timeout", "9m"}, 60 * time.Second, 9 * time.Minute, "warning: --op-timeout on 'nodegroup scale' is deprecated and will be removed in 0.12.0; use --wait-timeout"},
		{"scale --wait-timeout", []string{"nodegroup", "scale"}, "op-timeout", []string{"nodegroup", "scale", "-n", "x", "--wait-timeout", "7m"}, 60 * time.Second, 7 * time.Minute, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, wait, stderr := probeTimeouts(t, tc.path, tc.deprecated, tc.args...)
			if api != tc.api || wait != tc.wait {
				t.Errorf("api, wait = %v, %v; want %v, %v", api, wait, tc.api, tc.wait)
			}
			if tc.warn == "" && strings.Contains(stderr, "deprecated") {
				t.Errorf("unexpected deprecation warning: %q", stderr)
			}
			if tc.warn != "" && !strings.Contains(stderr, tc.warn) {
				t.Errorf("stderr = %q, want %q", stderr, tc.warn)
			}
		})
	}
}

// The mutating commands share --dry-run/-d and --yes/-y; the ones that wait
// have --wait-timeout; the ones that reach the cluster API take --kubeconfig
// and --kube-context.
func TestMutatingCommandsShareFlags(t *testing.T) {
	app := newApp()
	has := func(c *cli.Command, name string) bool {
		for _, f := range c.Flags {
			if slices.Contains(f.Names(), name) {
				return true
			}
		}
		return false
	}
	for _, tc := range []struct {
		path []string
		want []string
	}{
		{[]string{"addon", "update"}, []string{"dry-run", "d", "yes", "y", "wait-timeout"}},
		{[]string{"addon", "update-all"}, []string{"dry-run", "d", "yes", "y", "wait-timeout"}},
		{[]string{"nodegroup", "scale"}, []string{"dry-run", "d", "yes", "y", "wait-timeout", "kubeconfig", "kube-context"}},
		{[]string{"nodegroup", "update"}, []string{"dry-run", "d", "yes", "y", "wait-timeout", "kubeconfig", "kube-context"}},
		{[]string{"cluster", "upgrade"}, []string{"dry-run", "d", "yes", "y", "wait-timeout", "kubeconfig", "kube-context"}},
	} {
		c := app
		for _, n := range tc.path {
			c = c.Command(n)
		}
		for _, name := range tc.want {
			if !has(c, name) {
				t.Errorf("%s: missing -%s", strings.Join(tc.path, " "), name)
			}
		}
	}
}

// captureStderr returns what fn wrote to os.Stderr (ui.Stderr resolves
// os.Stderr on each write).
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() { os.Stderr = orig; _ = w.Close() }()
		fn()
	}()
	out := <-done
	_ = r.Close()
	return out
}
