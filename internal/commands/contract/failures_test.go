// Package contract holds the cross-command failure contract test of the
// mutating commands (docs/concepts/output.md, "Failures"): for each one,
// one injected fault shows up the same way in the three places a failure is
// reported.
package contract

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/addon"
	"github.com/dantech2000/refresh/internal/commands/cluster"
	"github.com/dantech2000/refresh/internal/commands/nodegroup"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/commands/statuscmd"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui"
	"github.com/dantech2000/refresh/internal/ui/plaintest"
)

// contractCase is one command run with one injected fault.
type contractCase struct {
	name string
	// world builds the fake AWS world, fault included; setup, when set,
	// adds server-level faults.
	world []*fakeaws.Cluster
	setup func(*fakeaws.Server)
	// args is the command line after "refresh", without -o.
	args []string
	// code is the exit code for every format.
	code int
	// failure is the keys the document's matching failures entry must
	// have. kind, name, and reason are required.
	failure map[string]any
	// stderr is the failure line, which must appear exactly once on stderr
	// with -o json, yaml, or plain. The table view lists the same text,
	// without "warning: ", once under INCOMPLETE DATA instead.
	stderr string
	// formats are the -o values to run: "" is the default table view,
	// "json" and "yaml" check the one document, "plain" checks pure TSV
	// with plainHeaders. nil means the command has no -o flag: only the
	// table view runs.
	formats      []string
	plainHeaders []string
}

// app has every command group, with refresh's global flags.
func app() *cli.Command {
	return fakeaws.App(statuscmd.Command(), cluster.Command(), nodegroup.Command(), addon.Command())
}

func prod(ngs ...*fakeaws.Nodegroup) *fakeaws.Cluster {
	return &fakeaws.Cluster{Name: "prod", Version: "1.31", Nodegroups: ngs}
}

func withAddons(addons ...*fakeaws.Addon) *fakeaws.Cluster {
	c := prod()
	c.Addons = addons
	return c
}

// cases are the rows of the contract for the mutating commands. Add a row
// for every new way a mutating command can fail. The read commands have
// their contract tests next to them, with the command-specific checks
// (partial rows, INCOMPLETE DATA): TestStatus_FailuresContract (statuscmd),
// TestList_FailuresContract and TestDescribe_FailuresContract (cluster,
// nodegroup, addon), and the cluster upgrade-check exit tests.
var cases = []contractCase{
	{
		name:    "nodegroup update: an update that can't start",
		world:   []*fakeaws.Cluster{prod(&fakeaws.Nodegroup{Name: "web", Version: "1.31", FailUpdate: true})},
		args:    []string{"nodegroup", "update", "prod", "--skip-health-check", "--yes"},
		code:    runner.ExitIncomplete,
		failure: map[string]any{"kind": "Nodegroup", "name": "web", "cluster": "prod", "region": "us-east-1", "reason": "InvalidRequest", "operation": "eks:UpdateNodegroupVersion", "retryable": false},
		stderr:  "warning: nodegroup prod/web (us-east-1): InvalidRequest: InvalidRequestException: fake update failure for web",
		formats: []string{"", "json", "yaml"},
	},
	{
		name:    "nodegroup update --dry-run: a nodegroup that can't be described",
		world:   []*fakeaws.Cluster{prod(&fakeaws.Nodegroup{Name: "web", Version: "1.31", DescribeNodegroupError: "AccessDeniedException"})},
		args:    []string{"nodegroup", "update", "prod", "--dry-run"},
		code:    runner.ExitIncomplete,
		failure: map[string]any{"kind": "Nodegroup", "name": "web", "cluster": "prod", "reason": "AccessDenied", "operation": "eks:DescribeNodegroup", "retryable": false},
		stderr:  "warning: nodegroup prod/web (us-east-1): AccessDenied: AccessDeniedException: fake DescribeNodegroup failure for web",
		formats: []string{"", "json", "yaml"},
	},
	{
		name:  "nodegroup update --all-clusters: a region that can't be listed",
		world: []*fakeaws.Cluster{prod(&fakeaws.Nodegroup{Name: "busy", Version: "1.31", Status: "UPDATING"})},
		setup: func(s *fakeaws.Server) {
			s.FailRegions(func(r string) string {
				if r == "eu-west-1" {
					return "AccessDeniedException"
				}
				return ""
			})
		},
		args:    []string{"nodegroup", "update", "--all-clusters", "-r", "us-east-1", "-r", "eu-west-1", "--skip-health-check", "--yes"},
		code:    runner.ExitIncomplete,
		failure: map[string]any{"kind": "Region", "name": "eu-west-1", "region": "eu-west-1", "reason": "AccessDenied", "operation": "eks:ListClusters", "retryable": false},
		stderr:  "warning: region eu-west-1: AccessDenied: AccessDeniedException: fakeaws: region eu-west-1 answers AccessDeniedException",
		formats: []string{"", "json", "yaml"},
	},
	{
		name:    "nodegroup update --all-clusters --dry-run: a cluster that can't be previewed",
		world:   []*fakeaws.Cluster{{Name: "prod", Version: "1.31", ListNodegroupsError: "AccessDeniedException"}},
		args:    []string{"nodegroup", "update", "--all-clusters", "-r", "us-east-1", "--dry-run"},
		code:    runner.ExitIncomplete,
		failure: map[string]any{"kind": "Cluster", "name": "prod", "reason": "AccessDenied", "operation": "eks:ListNodegroups"},
		stderr:  "warning: cluster prod (us-east-1): AccessDenied: AccessDeniedException: fake ListNodegroups failure for prod",
		formats: []string{"", "json", "yaml"},
	},
	{
		name:    "nodegroup scale --check-pdbs --force: PDBs that can't be read",
		world:   []*fakeaws.Cluster{prod(&fakeaws.Nodegroup{Name: "web", Version: "1.31", Desired: 3, Min: 1, Max: 5})},
		args:    []string{"nodegroup", "scale", "prod", "-n", "web", "--desired", "1", "--check-pdbs", "--force", "--yes"},
		code:    runner.ExitIncomplete,
		stderr:  "warning: cluster prod (us-east-1): Unknown: listing PodDisruptionBudgets: no Kubernetes client configured",
		formats: nil, // no -o flag
	},
	{
		name:         "addon update --wait: an update that EKS ends Failed",
		world:        []*fakeaws.Cluster{withAddons(&fakeaws.Addon{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0"}, UpdateStatus: "Failed"})},
		args:         []string{"addon", "update", "prod", "vpc-cni", "--wait", "--yes"},
		code:         runner.ExitError,
		failure:      map[string]any{"kind": "Update", "name": "vpc-cni", "cluster": "prod", "reason": "UpdateFailed", "retryable": false},
		stderr:       "warning: update prod/vpc-cni (us-east-1): UpdateFailed: addon vpc-cni update update-1 Failed: AdmissionRequestDenied: fake update failure",
		formats:      []string{"", "json", "yaml", "plain"},
		plainHeaders: []string{"ADDON", "PREVIOUS", "NEW", "STATUS", "UPDATE ID"},
	},
	{
		name:         "addon update --all --wait: an add-on that can't be read after its update",
		world:        []*fakeaws.Cluster{withAddons(&fakeaws.Addon{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0"}, DescribeErrorAfterUpdate: "AccessDeniedException"})},
		args:         []string{"addon", "update", "prod", "--all", "--wait", "--yes"},
		code:         runner.ExitIncomplete,
		failure:      map[string]any{"kind": "Addon", "name": "vpc-cni", "cluster": "prod", "reason": "AccessDenied", "operation": "eks:DescribeAddon"},
		stderr:       "warning: addon prod/vpc-cni (us-east-1): AccessDenied: AccessDeniedException: fake DescribeAddon failure for vpc-cni",
		formats:      []string{"", "json", "yaml", "plain"},
		plainHeaders: []string{"ADDON", "PREVIOUS", "NEW", "STATUS", "UPDATE ID"},
	},
	{
		name:         "cluster upgrade --dry-run: a version check that can't be read",
		world:        []*fakeaws.Cluster{prod(&fakeaws.Nodegroup{Name: "web", Version: "1.31"})},
		setup:        func(s *fakeaws.Server) { s.FailClusterVersions("AccessDeniedException") },
		args:         []string{"cluster", "upgrade", "prod", "--to", "1.32", "--dry-run"},
		code:         runner.ExitIncomplete,
		failure:      map[string]any{"kind": "Cluster", "name": "prod", "reason": "AccessDenied", "operation": "eks:DescribeClusterVersions"},
		stderr:       "warning: cluster prod (us-east-1): AccessDenied: AccessDeniedException: fake DescribeClusterVersions failure",
		formats:      []string{"", "json", "yaml", "plain"},
		plainHeaders: []string{"HOP", "STEP", "TYPE", "TARGET", "VERSION", "STATUS", "DESCRIPTION", "REASON"},
	},
	{
		name:    "cluster upgrade: a control-plane update that EKS ends Failed",
		world:   []*fakeaws.Cluster{{Name: "prod", Version: "1.31", UpdateStatus: "Failed", Nodegroups: []*fakeaws.Nodegroup{{Name: "web", Version: "1.31"}}}},
		args:    []string{"cluster", "upgrade", "prod", "--to", "1.32", "--yes", "--poll-interval", "5ms"},
		code:    runner.ExitError,
		failure: map[string]any{"kind": "Update", "name": "prod", "reason": "UpdateFailed", "retryable": false},
		stderr:  "warning: update prod (us-east-1): UpdateFailed: control plane upgrade to 1.32 failed: fake update failure",
		formats: []string{"", "json", "yaml"},
	},
}

// TestFailureContract runs every row and checks the three places a failure
// is reported: the exit code, the document's failures (-o json/yaml), and
// one named stderr line; -o plain keeps stdout pure TSV.
func TestFailureContract(t *testing.T) {
	for _, tc := range cases {
		formats := tc.formats
		if len(formats) == 0 {
			formats = []string{""}
		}
		for _, format := range formats {
			t.Run(tc.name+"/"+orDefault(format, "table"), func(t *testing.T) {
				srv := fakeaws.New(t, cloneWorld(tc.world)...)
				if tc.setup != nil {
					tc.setup(srv)
				}
				args := append([]string{"refresh"}, tc.args...)
				if format != "" {
					args = append(args, "-o", format)
				}
				stdout, stderr, err := fakeaws.Run(t, app(), args...)

				if got := runner.ExitCodeOf(err); got != tc.code {
					t.Fatalf("exit code = %d (err %v), want %d\nstderr:\n%s", got, err, tc.code, stderr)
				}
				if format == "" {
					// The table view lists the failure in its INCOMPLETE DATA
					// section on stdout, not on stderr.
					text := strings.TrimPrefix(tc.stderr, "warning: ")
					out := ui.StripANSI(stdout)
					if !strings.Contains(out, "INCOMPLETE DATA") || strings.Count(out, text) != 1 || strings.Contains(stderr, tc.stderr) {
						t.Errorf("the table view does not list the failure once under INCOMPLETE DATA:\n  %s\nstdout:\n%s\nstderr:\n%s", text, out, stderr)
					}
				} else if n := strings.Count(stderr, tc.stderr+"\n"); n != 1 {
					t.Errorf("stderr has the failure line %d time(s), want once:\n  %s\nstderr:\n%s", n, tc.stderr, stderr)
				}
				switch format {
				case "json", "yaml":
					doc, ok := fakeaws.RequireOneDocument(t, format, stdout).(map[string]any)
					if !ok {
						t.Fatalf("the document is not an object")
					}
					requireFailure(t, doc, tc.failure)
				case "plain":
					if strings.TrimSpace(stdout) != "" {
						plaintest.Check(t, stdout, tc.plainHeaders...)
					}
				}
			})
		}
	}
}

// requireFailure fails t unless doc's failures has an entry with every key
// of want, and every entry is a well-formed failure.
func requireFailure(t *testing.T, doc, want map[string]any) {
	t.Helper()
	fs, ok := doc["failures"].([]any)
	if !ok {
		t.Fatalf("failures = %#v, want a list", doc["failures"])
	}
	for i, f := range fs {
		m, ok := f.(map[string]any)
		if !ok {
			t.Fatalf("failures[%d] = %#v, want an object", i, f)
		}
		for _, key := range []string{"kind", "name", "reason", "retryable", "error"} {
			if _, ok := m[key]; !ok {
				t.Errorf("failures[%d] has no %q: %v", i, key, m)
			}
		}
		if matches(m, want) {
			return
		}
	}
	t.Errorf("failures = %v\nwant an entry with %v", fs, want)
}

func matches(got, want map[string]any) bool {
	for k, v := range want {
		if fmt.Sprint(got[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// cloneWorld copies the fake clusters, so each run starts from the row's
// world and not from the state an earlier run left.
func cloneWorld(world []*fakeaws.Cluster) []*fakeaws.Cluster {
	out := make([]*fakeaws.Cluster, 0, len(world))
	for _, c := range world {
		cc := *c
		cc.Nodegroups = nil
		for _, ng := range c.Nodegroups {
			n := *ng
			cc.Nodegroups = append(cc.Nodegroups, &n)
		}
		cc.Addons = nil
		for _, a := range c.Addons {
			ac := *a
			ac.Available = slices.Clone(a.Available)
			cc.Addons = append(cc.Addons, &ac)
		}
		out = append(out, &cc)
	}
	return out
}
