package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
)

// shadowAllowlist lists the subcommand flags that may reuse a root flag's
// name, each with the reason. urfave/cli resolves a name to the nearest
// declaration even when that one is unset, so any other duplicate silently
// ignores the global flag given before the subcommand
// (`refresh -t 5s cluster list` ran with the local 60s default).
var shadowAllowlist = map[string]string{
	// A repeatable "scan these regions" slice, not the single-region AWS
	// override. The action must read it through runner.Regions, which falls
	// back to the global --region only when the command is not sweeping (-A).
	"refresh status --region":       "repeatable scan regions; read via runner.Regions",
	"refresh cluster list --region": "repeatable scan regions; read via runner.Regions",
	// nodegroup update --all-clusters reads -r in fleet.go, which should adopt
	// runner.Regions in a follow-up.
	"refresh nodegroup update --region": "repeatable fleet discovery regions; fleet.go to adopt runner.Regions",
	// Different meaning and default from the global API timeout.
	"refresh nodegroup update --timeout": "wait for update completion (default 40m), not the API timeout",
	"refresh cluster upgrade --timeout":  "overall upgrade timeout, not the API timeout",
	"refresh addon update --timeout":     "update API budget (default 10m), not read from REFRESH_TIMEOUT",
	"refresh addon update-all --timeout": "update API budget (default 10m), not read from REFRESH_TIMEOUT",
	// Same meaning and default as the global flag. Drop these duplicates in a
	// follow-up to addon/command.go.
	"refresh addon list --timeout":     "duplicate of the global --timeout; remove in a follow-up",
	"refresh addon describe --timeout": "duplicate of the global --timeout; remove in a follow-up",
	// The values are saved into the context, not used as an override.
	"refresh context add --region":  "value saved into the context",
	"refresh context add --profile": "value saved into the context",
}

// Every subcommand flag whose name (or alias) matches a root flag must be on
// the allowlist, and every allowlist entry must still exist.
func TestNoSubcommandShadowsRootFlag(t *testing.T) {
	app := newApp()
	rootNames := map[string]string{} // name or alias -> primary name
	for _, f := range app.Flags {
		for _, n := range f.Names() {
			rootNames[n] = f.Names()[0]
		}
	}

	seen := map[string]bool{}
	var walk func(path string, cmds []*cli.Command)
	walk = func(path string, cmds []*cli.Command) {
		for _, c := range cmds {
			p := path + " " + c.Name
			for _, f := range c.Flags {
				for _, n := range f.Names() {
					if _, clash := rootNames[n]; !clash {
						continue
					}
					key := p + " --" + f.Names()[0]
					if seen[key] {
						continue
					}
					seen[key] = true
					if _, ok := shadowAllowlist[key]; !ok {
						t.Errorf("%s: flag %q shadows the global --%s; remove it, or allowlist it with a reason and read it through a lineage-aware helper", p, n, rootNames[n])
					}
				}
			}
			walk(p, c.Commands)
		}
	}
	walk(app.Name, app.Commands)

	for key := range shadowAllowlist {
		if !seen[key] {
			t.Errorf("allowlist entry %q no longer matches a flag; remove it", key)
		}
	}
}

// resolved is what a command action sees for the global flags.
type resolved struct {
	regions []string
	timeout time.Duration
	conc    int
}

// runCaptured runs args against the real CLI tree with the target command's
// action replaced by a probe, and returns what the probe saw.
func runCaptured(t *testing.T, path []string, args ...string) resolved {
	t.Helper()
	app := newApp()
	cmd := app
	for _, name := range path {
		cmd = cmd.Command(name)
		if cmd == nil {
			t.Fatalf("command %v not found", path)
		}
	}
	var got resolved
	cmd.Action = func(_ context.Context, c *cli.Command) error {
		got = resolved{regions: runner.Regions(c, c.Bool("all-regions")), timeout: c.Duration("timeout"), conc: c.Int("max-concurrency")}
		return nil
	}
	if err := app.Run(context.Background(), append([]string{"refresh"}, args...)); err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return got
}

// A global flag before the subcommand and the same flag after it must
// resolve identically.
func TestGlobalFlagsResolveBeforeOrAfterSubcommand(t *testing.T) {
	t.Setenv("REFRESH_TIMEOUT", "")
	t.Setenv("REFRESH_MAX_CONCURRENCY", "")
	cases := []struct {
		name          string
		path          []string
		before, after []string
		check         func(resolved) bool
	}{
		{
			name: "status --region", path: []string{"status"},
			before: []string{"--region", "eu-west-1", "status"}, after: []string{"status", "--region", "eu-west-1"},
			check: func(r resolved) bool { return slices.Equal(r.regions, []string{"eu-west-1"}) },
		},
		{
			name: "cluster list --region", path: []string{"cluster", "list"},
			before: []string{"--region", "eu-west-1", "cluster", "list"}, after: []string{"cluster", "list", "--region", "eu-west-1"},
			check: func(r resolved) bool { return slices.Equal(r.regions, []string{"eu-west-1"}) },
		},
		{
			name: "cluster list -t -C", path: []string{"cluster", "list"},
			before: []string{"-t", "5s", "-C", "1", "cluster", "list"}, after: []string{"cluster", "list", "-t", "5s", "-C", "1"},
			check: func(r resolved) bool { return r.timeout == 5*time.Second && r.conc == 1 },
		},
		{
			name: "cluster describe -t", path: []string{"cluster", "describe"},
			before: []string{"-t", "5s", "cluster", "describe"}, after: []string{"cluster", "describe", "-t", "5s"},
			check: func(r resolved) bool { return r.timeout == 5*time.Second },
		},
		{
			name: "cluster upgrade-check -t", path: []string{"cluster", "upgrade-check"},
			before: []string{"-t", "5s", "cluster", "upgrade-check"}, after: []string{"cluster", "upgrade-check", "-t", "5s"},
			check: func(r resolved) bool { return r.timeout == 5*time.Second },
		},
		{
			name: "status -t -C", path: []string{"status"},
			before: []string{"-t", "5s", "-C", "1", "status"}, after: []string{"status", "-t", "5s", "-C", "1"},
			check: func(r resolved) bool { return r.timeout == 5*time.Second && r.conc == 1 },
		},
		{
			name: "nodegroup list -t", path: []string{"nodegroup", "list"},
			before: []string{"-t", "5s", "nodegroup", "list"}, after: []string{"nodegroup", "list", "-t", "5s"},
			check: func(r resolved) bool { return r.timeout == 5*time.Second },
		},
		{
			name: "nodegroup describe -t", path: []string{"nodegroup", "describe"},
			before: []string{"-t", "5s", "nodegroup", "describe"}, after: []string{"nodegroup", "describe", "-t", "5s"},
			check: func(r resolved) bool { return r.timeout == 5*time.Second },
		},
		{
			name: "nodegroup scale -t", path: []string{"nodegroup", "scale"},
			before: []string{"-t", "5s", "nodegroup", "scale", "-n", "x"}, after: []string{"nodegroup", "scale", "-n", "x", "-t", "5s"},
			check: func(r resolved) bool { return r.timeout == 5*time.Second },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := runCaptured(t, tc.path, tc.before...)
			a := runCaptured(t, tc.path, tc.after...)
			if !tc.check(b) {
				t.Errorf("before the subcommand: got %+v", b)
			}
			if !tc.check(a) {
				t.Errorf("after the subcommand: got %+v", a)
			}
			if !slices.Equal(a.regions, b.regions) || a.timeout != b.timeout || a.conc != b.conc {
				t.Errorf("before %+v != after %+v", b, a)
			}
		})
	}
}

// Without any flag, the removed local duplicates keep their defaults (60s,
// 8) through the global flags.
func TestGlobalFlagDefaultsUnchanged(t *testing.T) {
	t.Setenv("REFRESH_TIMEOUT", "")
	t.Setenv("REFRESH_MAX_CONCURRENCY", "")
	got := runCaptured(t, []string{"cluster", "list"}, "cluster", "list")
	if got.timeout != 60*time.Second || got.conc != 8 || got.regions != nil {
		t.Errorf("cluster list defaults = %+v, want 60s, 8, no regions", got)
	}
	// The local repeatable -r wins over the global --region, and keeps
	// every value.
	got = runCaptured(t, []string{"status"}, "--region", "us-east-1", "status", "-r", "eu-west-1", "-r", "ap-south-1")
	if !slices.Equal(got.regions, []string{"eu-west-1", "ap-south-1"}) {
		t.Errorf("status regions = %v, want the local -r values", got.regions)
	}
}

// The global --region becomes the scan list only without -A. With -A it just
// sets the home region (and so the partition), so `refresh --region
// cn-north-1 cluster list -A` still sweeps the whole China partition. A local
// -r is always the scan list.
func TestRegionsGlobalVersusLocalWithAndWithoutSweep(t *testing.T) {
	for _, cmdPath := range [][]string{{"status"}, {"cluster", "list"}} {
		for _, tc := range []struct {
			name string
			args []string
			want []string
		}{
			{"global", []string{"--region", "eu-west-1"}, []string{"eu-west-1"}},
			{"global -A", []string{"--region", "cn-north-1", "-A"}, nil},
			{"local", []string{"-r", "eu-west-1"}, []string{"eu-west-1"}},
			{"local -A", []string{"-r", "eu-west-1", "-A"}, []string{"eu-west-1"}},
		} {
			t.Run(strings.Join(cmdPath, " ")+"/"+tc.name, func(t *testing.T) {
				// Global flags go before the subcommand, local ones after.
				var argv []string
				if tc.args[0] == "--region" {
					argv = append(append(append([]string{}, tc.args[:2]...), cmdPath...), tc.args[2:]...)
				} else {
					argv = append(append([]string{}, cmdPath...), tc.args...)
				}
				got := runCaptured(t, cmdPath, argv...)
				if !slices.Equal(got.regions, tc.want) {
					t.Errorf("%v: regions = %v, want %v", argv, got.regions, tc.want)
				}
			})
		}
	}
}
