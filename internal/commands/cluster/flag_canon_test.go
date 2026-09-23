package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// cluster describe shows health and add-ons by default. --no-health and
// --no-addons turn them off. The old --show-health / --include-addons (and
// -H) still work for one release, hidden, with a one-line deprecation
// warning; =false maps to the new flags. (REF-164)
func TestDescribeSectionFlags(t *testing.T) {
	cases := []struct {
		name           string
		args           []string
		health, addons bool
		warn           string
	}{
		{"defaults", nil, true, true, ""},
		{"--no-health", []string{"--no-health"}, false, true, ""},
		{"--no-addons", []string{"--no-addons"}, true, false, ""},
		{"deprecated --show-health", []string{"--show-health"}, true, true,
			"warning: --show-health on 'cluster describe' is deprecated and will be removed in 0.12.0; it is on by default (use --no-health to turn it off)"},
		{"deprecated -H", []string{"-H"}, true, true, "--show-health on 'cluster describe' is deprecated"},
		{"deprecated --show-health=false", []string{"--show-health=false"}, false, true,
			"warning: --show-health=false on 'cluster describe' is deprecated and will be removed in 0.12.0; use --no-health"},
		{"deprecated --include-addons", []string{"--include-addons"}, true, true,
			"warning: --include-addons on 'cluster describe' is deprecated and will be removed in 0.12.0; it is on by default (use --no-addons to turn it off)"},
		{"deprecated --include-addons=false", []string{"--include-addons=false"}, true, false,
			"warning: --include-addons=false on 'cluster describe' is deprecated and will be removed in 0.12.0; use --no-addons"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := describeCommand()
			var health, addons bool
			d.Action = func(_ context.Context, cmd *cli.Command) error {
				health, addons = describeSections(cmd)
				return nil
			}
			app := fakeaws.App(&cli.Command{Name: "cluster", Commands: []*cli.Command{d}})
			_, stderr, err := fakeaws.Run(t, app, append([]string{"refresh", "cluster", "describe", "prod"}, tc.args...)...)
			if err != nil {
				t.Fatalf("describe: %v", err)
			}
			if health != tc.health || addons != tc.addons {
				t.Errorf("health, addons = %v, %v; want %v, %v", health, addons, tc.health, tc.addons)
			}
			if tc.warn == "" && strings.Contains(stderr, "deprecated") {
				t.Errorf("unexpected warning: %q", stderr)
			}
			if tc.warn != "" && !strings.Contains(stderr, tc.warn) {
				t.Errorf("stderr = %q, want %q", stderr, tc.warn)
			}
			if strings.Count(stderr, "\n") > 1 {
				t.Errorf("warning is more than one line: %q", stderr)
			}
		})
	}
}

// Removed shorthands on cluster describe fail with their replacement.
func TestDescribeRemovedShorthands(t *testing.T) {
	for letter, want := range map[string]string{
		"-d": "-d was removed from 'cluster describe' in 0.11.0; use --detailed",
		"-s": "-s was removed from 'cluster describe' in 0.11.0; use --show-security",
		"-a": "-a was removed from 'cluster describe' in 0.11.0; add-ons are shown by default; use --no-addons to hide them",
	} {
		t.Run(letter, func(t *testing.T) {
			srv := fakeaws.New(t, upgradeWorld())
			_, stderr, err := runCluster(t, "describe", "prod", letter)
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
			if strings.Contains(stderr, "Incorrect Usage") {
				t.Errorf("stderr has the generic usage error: %q", stderr)
			}
			if len(srv.Calls()) != 0 {
				t.Errorf("AWS called: %v", srv.Calls())
			}
		})
	}
}

// Without a terminal, cluster upgrade can't ask before each phase, so a run
// without --yes fails before any AWS call and names --yes. --dry-run is fine.
func TestUpgrade_NoTTYRequiresYes(t *testing.T) {
	orig := runner.StdinIsTerminal
	runner.StdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { runner.StdinIsTerminal = orig })

	srv := fakeaws.New(t, upgradeWorld())
	_, _, err := runCluster(t, "upgrade", "prod", "--to", "1.32")
	if err == nil || !strings.Contains(err.Error(), "cluster upgrade needs confirmation but there is no interactive terminal; add --yes") {
		t.Fatalf("err = %v, want the --yes error", err)
	}
	if len(srv.Calls()) != 0 {
		t.Errorf("AWS called before the --yes check: %v", srv.Calls())
	}
	if _, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "-d"); err != nil {
		t.Fatalf("--dry-run without a terminal: %v\nstderr:\n%s", err, stderr)
	}
}
