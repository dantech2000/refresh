package addon_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// The versions of the update tests: each add-on has one newer version in
// the catalogue of Kubernetes 1.33, and vpc-cni has one that only 1.34
// can run.
const (
	cniOld, cniNew, cniNext = "v1.19.0-eksbuild.1", "v1.20.0-eksbuild.1", "v1.21.0-eksbuild.1"
	// cniPatch is a version between the installed one and the latest.
	cniPatch       = "v1.19.5-eksbuild.1"
	dnsOld, dnsNew = "v1.11.3-eksbuild.2", "v1.11.4-eksbuild.2"
	proxy          = "v1.33.0-eksbuild.2"
)

// updateWorld returns a world with the cluster prod: vpc-cni and coredns
// one version behind, and kube-proxy at the latest. edit changes it.
func updateWorld(edit func(c *fleettest.Cluster)) *fleettest.World {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		prod := &fleettest.Cluster{
			Name: "prod", Region: east, Version: "1.33",
			Nodegroups: []*fleettest.Nodegroup{{Name: "web", Version: "1.33"}},
			Addons: []*fleettest.Addon{
				{Name: "coredns", Version: dnsOld},
				{Name: "kube-proxy", Version: proxy},
				{Name: "vpc-cni", Version: cniOld},
			},
		}
		if edit != nil {
			edit(prod)
		}
		s.Clusters = append(s.Clusters, prod)
		s.AddonVersions = []fleettest.AddonVersion{
			{Addon: "vpc-cni", Version: cniNext, ClusterVersions: []string{"1.34"}},
			{Addon: "vpc-cni", Version: cniNew, ClusterVersions: []string{"1.33", "1.34"}},
			{Addon: "vpc-cni", Version: cniPatch, ClusterVersions: []string{"1.33"}},
			{Addon: "vpc-cni", Version: cniOld, ClusterVersions: []string{"1.32", "1.33"}},
			{Addon: "coredns", Version: dnsNew, ClusterVersions: []string{"1.33"}},
			{Addon: "coredns", Version: dnsOld, ClusterVersions: []string{"1.32", "1.33"}},
			{Addon: "kube-proxy", Version: proxy, ClusterVersions: []string{"1.33"}},
		}
	})
	return world
}

// addon returns the add-on of the cluster.
func addon(c *fleettest.Cluster, name string) *fleettest.Addon { return c.Addon(name) }

// A script of an update that takes 30 seconds, and of one that fails
// after that long.
var (
	slow  = fleettest.UpdateScript{After: 30 * time.Second}
	fails = fleettest.UpdateScript{
		After: 30 * time.Second, Result: fleettest.UpdateFailed, ResourceStatus: fleettest.StatusUpdateFailed,
		Errors: []fleettest.UpdateError{{Code: "ConfigurationConflict", Message: "Conflicts found when trying to apply", ResourceIDs: []string{"kube-system/aws-node"}}},
	}
)

// updateCase is one run of "refresh addon update" against a world.
type updateCase struct {
	name string
	// seed changes the cluster before the run, and prepare changes the
	// world after it is built, such as starting an update elsewhere.
	seed    func(c *fleettest.Cluster)
	prepare func(t *testing.T, w *fleettest.World)
	args    []string
	// answer is what a person types at the prompt. Not empty means that
	// stdin is a terminal.
	answer string
	exit   int
	// updates are the add-ons that UpdateAddon was sent for, in order.
	updates []string
}

// runUpdate runs the case with the extra arguments and returns the fake
// and the output of the run as one text: the exit code, stdout, and
// stderr.
func runUpdate(t *testing.T, tc updateCase, extra ...string) (*cmdtest.Fake, string) {
	t.Helper()
	world := updateWorld(tc.seed)
	if tc.prepare != nil {
		tc.prepare(t, world)
	}
	e := cmdtest.NewFake(t, world, east)
	e.UseWorldClock()
	if tc.answer != "" {
		e.Factory.IOStreams.InTTY = true
		e.Factory.IOStreams.In = strings.NewReader(tc.answer)
	}
	args := append([]string{"addon", "update", "-c", "prod"}, tc.args...)
	err := e.Run(cmdutiltest.Context(t), append(args, extra...)...)
	if got := cmdutil.ExitCode(err); got != tc.exit {
		t.Errorf("exit code = %d, want %d (error: %v)", got, tc.exit, err)
	}
	var updates []string
	for _, r := range e.Server.Requests() {
		if r.Operation == fleettest.EKSUpdateAddon {
			updates = append(updates, fmt.Sprint(r.Input["addonName"]))
		}
	}
	if diff := cmp.Diff(tc.updates, updates); diff != "" {
		t.Errorf("UpdateAddon requests (-want +got):\n%s", diff)
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return e, fmt.Sprintf("exit: %d\nerror: %s\n--- stdout ---\n%s--- stderr ---\n%s", cmdutil.ExitCode(err), msg, e.Out, e.Err)
}

// startElsewhere starts an update of an add-on in the world, as another
// process would, with the script.
func startElsewhere(name, version string, script fleettest.UpdateScript) func(t *testing.T, w *fleettest.World) {
	return func(t *testing.T, w *fleettest.World) {
		t.Helper()
		w.Edit(func(s *fleettest.State) {
			a := s.Cluster(east, "prod").Addon(name)
			a.UpdateScripts = append([]fleettest.UpdateScript{script}, a.UpdateScripts...)
		})
		if _, err := w.UpdateAddon(east, "prod", name, fleettest.AddonRequest{Version: &version}); err != nil {
			t.Fatalf("starting the update of %s: %v", name, err)
		}
	}
}

func updateCases() []updateCase {
	never := fleettest.UpdateScript{Never: true}
	return []updateCase{
		{name: "success", args: []string{"vpc-cni", "--yes"}, updates: []string{"vpc-cni"},
			seed: func(c *fleettest.Cluster) { addon(c, "vpc-cni").UpdateScripts = []fleettest.UpdateScript{slow} }},
		{name: "no-wait", args: []string{"vpc-cni", "--yes", "--no-wait"}, updates: []string{"vpc-cni"},
			seed: func(c *fleettest.Cluster) { addon(c, "vpc-cni").UpdateScripts = []fleettest.UpdateScript{slow} }},
		{name: "all", args: []string{"--all", "--yes"}, updates: []string{"vpc-cni", "coredns"}},
		{name: "skip-addon", args: []string{"--all", "--skip-addon", "coredns", "--yes"}, updates: []string{"vpc-cni"}},
		{name: "pinned-version", args: []string{"coredns", "--version", dnsNew, "--yes"}, updates: []string{"coredns"}},
		{name: "dry-run", args: []string{"--all", "--dry-run"}},
		{name: "dry-run-ask", args: []string{"coredns", "--dry-run"}, exit: cmdutil.ExitNeedsAttention,
			seed: func(c *fleettest.Cluster) {
				a := addon(c, "coredns")
				a.Status = fleettest.StatusDegraded
				a.HealthIssues = []fleettest.Issue{{Code: "InsufficientNumberOfReplicas", Message: "The add-on is unhealthy because it has no replicas"}}
			}},
		{name: "dry-run-blocked", args: []string{"vpc-cni", "--dry-run"}, exit: cmdutil.ExitBlocked,
			seed: func(c *fleettest.Cluster) { c.Status = fleettest.StatusUpdating }},
		{name: "needs-yes", args: []string{"vpc-cni"}, exit: cmdutil.ExitFailure},
		{name: "prompt-yes", args: []string{"vpc-cni"}, answer: "y\n", updates: []string{"vpc-cni"}},
		{name: "prompt-no", args: []string{"vpc-cni"}, answer: "n\n", exit: cmdutil.ExitFailure},
		{name: "prompt-degraded", args: []string{"coredns"}, answer: "yes\n", updates: []string{"coredns"},
			seed: func(c *fleettest.Cluster) { addon(c, "coredns").Status = fleettest.StatusDegraded }},
		{name: "require-healthy", args: []string{"coredns", "--yes", "--require-healthy"}, exit: cmdutil.ExitNeedsAttention,
			seed: func(c *fleettest.Cluster) { addon(c, "coredns").Status = fleettest.StatusDegraded }},
		{name: "cluster-busy", args: []string{"vpc-cni", "--yes"}, exit: cmdutil.ExitBlocked,
			seed: func(c *fleettest.Cluster) { c.Status = fleettest.StatusUpdating }},
		{name: "other-addon-busy", args: []string{"vpc-cni", "--yes"}, exit: cmdutil.ExitBlocked,
			seed: func(c *fleettest.Cluster) { addon(c, "kube-proxy").Status = fleettest.StatusUpdating }},
		{name: "busy-target", args: []string{"--all", "--yes"}, exit: cmdutil.ExitIncomplete, updates: []string{"coredns"},
			prepare: startElsewhere("vpc-cni", cniPatch, never)},
		{name: "busy-only-target", args: []string{"vpc-cni", "--yes"}, exit: cmdutil.ExitIncomplete,
			prepare: startElsewhere("vpc-cni", cniPatch, never)},
		{name: "attach", args: []string{"vpc-cni", "--yes"},
			prepare: startElsewhere("vpc-cni", cniNew, fleettest.UpdateScript{After: 4 * time.Minute})},
		{name: "attach-failed", args: []string{"vpc-cni", "--yes"}, exit: cmdutil.ExitFailure,
			prepare: startElsewhere("vpc-cni", cniNew, fails)},
		{name: "failed", args: []string{"vpc-cni", "--yes"}, exit: cmdutil.ExitFailure, updates: []string{"vpc-cni"},
			seed: func(c *fleettest.Cluster) { addon(c, "vpc-cni").UpdateScripts = []fleettest.UpdateScript{fails} }},
		{name: "partial-failure", args: []string{"--all", "--yes"}, exit: cmdutil.ExitIncomplete, updates: []string{"vpc-cni", "coredns"},
			seed: func(c *fleettest.Cluster) { addon(c, "vpc-cni").UpdateScripts = []fleettest.UpdateScript{fails} }},
		{name: "deadline", args: []string{"vpc-cni", "--yes", "--wait-timeout", "2m"}, exit: cmdutil.ExitIncomplete, updates: []string{"vpc-cni"},
			seed: func(c *fleettest.Cluster) { addon(c, "vpc-cni").UpdateScripts = []fleettest.UpdateScript{never} }},
		{name: "unhealthy-after", args: []string{"vpc-cni", "--yes"}, exit: cmdutil.ExitVerifyFailed, updates: []string{"vpc-cni"},
			seed: func(c *fleettest.Cluster) {
				addon(c, "vpc-cni").UpdateScripts = []fleettest.UpdateScript{{ResourceStatus: fleettest.StatusDegraded}}
			}},
		{name: "incompatible-version", args: []string{"vpc-cni", "--version", cniNext, "--yes"}, exit: cmdutil.ExitFailure},
		{name: "not-installed", args: []string{"vpc-cn", "--yes"}, exit: cmdutil.ExitFailure},
		{name: "up-to-date", args: []string{"kube-proxy", "--yes"}},
		{name: "quiet", args: []string{"--all", "--yes", "--quiet"}, updates: []string{"vpc-cni", "coredns"},
			seed: func(c *fleettest.Cluster) { addon(c, "coredns").Status = fleettest.StatusDegraded }},
		{name: "skip-health-check", args: []string{"coredns", "--yes", "--skip-health-check"}, updates: []string{"coredns"},
			seed: func(c *fleettest.Cluster) { addon(c, "coredns").Status = fleettest.StatusDegraded }},
		{name: "downgrade", args: []string{"vpc-cni", "--version", cniOld, "--yes"}, updates: []string{"vpc-cni"},
			seed: func(c *fleettest.Cluster) { addon(c, "vpc-cni").Version = cniNew }},
	}
}

// TestUpdate runs every case in the table format and compares the exit
// code, stdout, and stderr with a golden file.
func TestUpdate(t *testing.T) {
	for _, tc := range updateCases() {
		t.Run(tc.name, func(t *testing.T) {
			_, got := runUpdate(t, tc)
			cmdutiltest.Golden(t, "update-"+tc.name+".txt", got, *update)
		})
	}
}

// TestUpdate_Formats runs the cases that print a document in the three
// other formats. A machine document matches the schema of its kind, and
// stdout holds nothing else.
func TestUpdate_Formats(t *testing.T) {
	kinds := map[string]apidoc.Kind{
		"success": apidoc.KindRun, "no-wait": apidoc.KindRun, "all": apidoc.KindRun, "dry-run": apidoc.KindPlan,
		"dry-run-ask": apidoc.KindPlan, "dry-run-blocked": apidoc.KindPlan, "cluster-busy": apidoc.KindPlan,
		"busy-target": apidoc.KindRun, "attach": apidoc.KindRun, "attach-failed": apidoc.KindRun, "failed": apidoc.KindRun,
		"partial-failure": apidoc.KindRun, "deadline": apidoc.KindRun, "unhealthy-after": apidoc.KindRun, "up-to-date": apidoc.KindPlan,
		"busy-only-target": apidoc.KindPlan,
	}
	for _, tc := range updateCases() {
		kind, ok := kinds[tc.name]
		if !ok {
			continue
		}
		for _, format := range []string{"json", "yaml", "plain"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				e, got := runUpdate(t, tc, "-o", format)
				cmdutiltest.Golden(t, "update-"+tc.name+"."+format, got, *update)
				switch format {
				case "json":
					cmdutiltest.ValidateJSON(t, kind, e.Out.Bytes())
				case "yaml":
					cmdutiltest.ValidateYAML(t, kind, e.Out.Bytes())
				}
			})
		}
	}
}

// A run that cannot prompt and has no --yes refuses before it uses the
// network, and reads nothing from stdin.
func TestUpdate_UsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no add-on", []string{"--yes"}, "name at least one add-on, or pass --all"},
		{"names and all", []string{"vpc-cni", "--all", "--yes"}, "name the add-ons or pass --all, not both"},
		{"version for two", []string{"vpc-cni", "coredns", "--version", "v1", "--yes"}, "--version needs exactly one add-on"},
		{"version with all", []string{"--all", "--version", "v1", "--yes"}, "--version needs exactly one add-on"},
		{"skip without all", []string{"vpc-cni", "--skip-addon", "coredns", "--yes"}, "--skip-addon needs --all"},
		{"dry run with no wait", []string{"vpc-cni", "--dry-run", "--no-wait"}, "--dry-run changes nothing"},
		{"poll interval", []string{"vpc-cni", "--yes", "--poll-interval", "0s"}, "invalid --poll-interval"},
		{"skip and require", []string{"vpc-cni", "--yes", "--skip-health-check", "--require-healthy"}, "cannot be used together"},
		{"invalid before yes", []string{"vpc-cni", "coredns", "--version", "v1"}, "--version needs exactly one add-on"},
		{"json without yes", []string{"vpc-cni", "-o", "json"}, "pass --yes to confirm the change"},
		{"no terminal without yes", []string{"vpc-cni"}, "no terminal to confirm the change; pass --yes"},
		{"no force flag", []string{"vpc-cni", "--yes", "--force"}, "unknown flag: --force"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := cmdtest.NewFake(t, updateWorld(nil), east)
			in := strings.NewReader("y\n")
			e.Factory.IOStreams.In = in
			err := e.Run(cmdutiltest.Context(t), append([]string{"addon", "update", "-c", "prod"}, tt.args...)...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
				t.Errorf("exit code = %d, want 1", got)
			}
			if n := len(e.Server.Requests()); n != 0 {
				t.Errorf("%d requests, want none: %v", n, e.Operations())
			}
			if e.Out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", e.Out)
			}
			if in.Len() != 2 {
				t.Error("the command read from stdin")
			}
		})
	}
}

// A change never takes its cluster from the kubeconfig, with or without
// --dry-run.
func TestUpdate_NoClusterFromKubeconfig(t *testing.T) {
	for _, args := range [][]string{{"vpc-cni", "--yes"}, {"vpc-cni", "--dry-run"}} {
		e := cmdtest.NewFake(t, updateWorld(nil), east)
		err := e.Run(cmdutiltest.Context(t), append([]string{"addon", "update"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "no cluster specified") {
			t.Errorf("%v: error = %v, want the no-cluster error", args, err)
		}
		if n := len(e.Server.Requests()); n != 0 {
			t.Errorf("%v: %d requests, want none", args, n)
		}
	}
}

// The reads of a run with --yes: two waves for the plan, then for each
// add-on a fresh read, the request, the polls of its update, and the read
// that confirms it.
func TestUpdate_Requests(t *testing.T) {
	tc := updateCase{args: []string{"vpc-cni", "--yes"}, updates: []string{"vpc-cni"}}
	e, _ := runUpdate(t, tc)
	var got []string
	for _, r := range e.Server.Requests()[5:] {
		got = append(got, string(r.Operation))
	}
	want := []string{"eks:DescribeAddonVersions", "eks:DescribeAddon", "eks:UpdateAddon", "eks:DescribeUpdate", "eks:DescribeAddon"}
	// The plan describes three add-ons and one nodegroup beside the
	// catalogue read, in one wave, in any order.
	tail := got[len(got)-4:]
	if diff := cmp.Diff(want[1:], tail); diff != "" {
		t.Errorf("the requests after the plan (-want +got):\n%s\nall: %v", diff, e.Operations())
	}
	if n := e.Count(fleettest.EKSDescribeAddonVersions); n != 1 {
		t.Errorf("%d DescribeAddonVersions calls, want 1: the target alone", n)
	}
	var sent map[string]any
	for _, r := range e.Server.Requests() {
		if r.Operation == fleettest.EKSUpdateAddon {
			sent = r.Input
		}
	}
	delete(sent, "clientRequestToken")
	if diff := cmp.Diff(map[string]any{"clusterName": "prod", "addonName": "vpc-cni", "addonVersion": cniNew}, sent); diff != "" {
		t.Errorf("the UpdateAddon request (-want +got):\n%s", diff)
	}
}

// A response that is lost after EKS accepted the update is retried with
// the same token, so one update starts.
func TestUpdate_LostResponse(t *testing.T) {
	tc := updateCase{
		args: []string{"vpc-cni", "--yes"}, updates: []string{"vpc-cni", "vpc-cni"},
		prepare: func(_ *testing.T, w *fleettest.World) {
			w.SetFault(fleettest.EKSUpdateAddon, fleettest.Fault{LoseResponse: 1})
		},
	}
	e, _ := runUpdate(t, tc)
	e.World.View(func(s *fleettest.State) {
		c := s.Cluster(east, "prod")
		if n := len(c.Updates); n != 1 {
			t.Errorf("%d updates in the world, want 1", n)
		}
		if a := c.Addon("vpc-cni"); a.Version != cniNew || a.Status != fleettest.StatusActive {
			t.Errorf("vpc-cni = %s %s, want %s ACTIVE", a.Version, a.Status, cniNew)
		}
	})
	var tokens []string
	for _, r := range e.Server.Requests() {
		if r.Operation == fleettest.EKSUpdateAddon {
			tokens = append(tokens, fmt.Sprint(r.Input["clientRequestToken"]))
		}
	}
	if len(tokens) != 2 || tokens[0] != tokens[1] || tokens[0] == "" {
		t.Errorf("tokens = %v, want the same token twice", tokens)
	}
}

// An interrupt during the wait exits 1, and the result says that the
// update may still be running.
func TestUpdate_Interrupt(t *testing.T) {
	world := updateWorld(func(c *fleettest.Cluster) {
		addon(c, "vpc-cni").UpdateScripts = []fleettest.UpdateScript{{Never: true}}
	})
	e := cmdtest.NewFake(t, world, east)
	e.UseWorldClock()
	ctx, cancel := context.WithCancel(cmdutiltest.Context(t))
	defer cancel()
	// The third poll is the handshake: the wait is under way.
	var polls atomic.Int32
	e.Server.OnRequest = func(_ context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeUpdate && polls.Add(1) == 3 {
			cancel()
		}
	}
	err := e.Run(ctx, "addon", "update", "vpc-cni", "-c", "prod", "--yes", "-o", "json")
	if !errors.Is(err, context.Canceled) || cmdutil.ExitCode(err) != cmdutil.ExitFailure {
		t.Fatalf("error = %v (exit %d), want an interrupt with exit 1", err, cmdutil.ExitCode(err))
	}
	cmdutiltest.ValidateJSON(t, apidoc.KindRun, e.Out.Bytes())
	for _, want := range []string{`"status": "Interrupted"`, `"status": "InProgress"`, `"reason": "Interrupted"`} {
		if !strings.Contains(e.Out.String(), want) {
			t.Errorf("stdout has no %s:\n%s", want, e.Out)
		}
	}
}
