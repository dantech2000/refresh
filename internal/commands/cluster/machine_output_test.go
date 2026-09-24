package cluster

import (
	"slices"
	"strings"
	"testing"

	"github.com/pterm/pterm"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui"
	"github.com/dantech2000/refresh/internal/ui/plaintest"
)

// These tests run cluster commands end to end against the fake AWS endpoint
// and hold them to the machine-output contract: with -o json/yaml, stdout is
// exactly one document and every human line goes to stderr.

func runCluster(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return fakeaws.Run(t, fakeaws.App(Command()), append([]string{"refresh", "cluster"}, args...)...)
}

func upgradeWorld() *fakeaws.Cluster {
	return &fakeaws.Cluster{Name: "prod", Version: "1.31", Nodegroups: []*fakeaws.Nodegroup{
		{Name: "web", Version: "1.31"},
	}}
}

// An executed upgrade prints one {plan, report} document; the engine's
// progress lines go to stderr.
func TestUpgradeMachineOutput_Executed(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			srv := fakeaws.New(t, upgradeWorld())
			stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--poll-interval", "5ms", "-o", format)
			if err != nil {
				t.Fatalf("upgrade: %v\nstderr:\n%s", err, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, format, stdout).(map[string]any)
			plan, ok := doc["plan"].(map[string]any)
			if !ok || plan["targetVersion"] != "1.32" {
				t.Fatalf("plan = %v, want a plan to 1.32", doc["plan"])
			}
			report, ok := doc["report"].(map[string]any)
			if !ok {
				t.Fatalf("report = %v, want an object", doc["report"])
			}
			if completed, _ := report["completed"].([]any); len(completed) == 0 {
				t.Errorf("report.completed is empty: %v", report)
			}
			if !strings.Contains(stderr, "▸") {
				t.Errorf("stderr has no engine progress lines; got:\n%s", stderr)
			}
			if got := srv.Cluster("prod"); got.Version != "1.32" || got.Nodegroups[0].Version != "1.32" {
				t.Errorf("fake cluster after upgrade: control plane %s, web %s; want both 1.32", got.Version, got.Nodegroups[0].Version)
			}
			// The readiness gate refreshed insights before reading them.
			if !slices.Contains(srv.Calls(), "eks POST /clusters/prod/insights-refresh") {
				t.Errorf("no StartInsightsRefresh call; calls:\n%s", strings.Join(srv.Calls(), "\n"))
			}
			// The fake has no Kubernetes API: the roll proceeds and says the
			// PDB check was skipped.
			if !strings.Contains(stderr, "PDB drain-blocker checks before nodegroup rolls are skipped") {
				t.Errorf("stderr has no skipped-PDB-check warning; got:\n%s", stderr)
			}
		})
	}
}

// --quiet drops the progress lines; stdout still carries the document.
func TestUpgradeMachineOutput_Quiet(t *testing.T) {
	fakeaws.New(t, upgradeWorld())
	stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--quiet", "--poll-interval", "5ms", "-o", "json")
	if err != nil {
		t.Fatalf("upgrade: %v\nstderr:\n%s", err, stderr)
	}
	fakeaws.RequireOneDocument(t, "json", stdout)
	if strings.Contains(stderr, "▸") {
		t.Errorf("--quiet still printed progress:\n%s", stderr)
	}
}

// A rerun after success has nothing to do: the document has an empty report.
func TestUpgradeMachineOutput_NothingToDo(t *testing.T) {
	fakeaws.New(t, &fakeaws.Cluster{Name: "prod", Version: "1.32", Nodegroups: []*fakeaws.Nodegroup{{Name: "web", Version: "1.32"}}})
	stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "-o", "json")
	if err != nil {
		t.Fatalf("upgrade: %v\nstderr:\n%s", err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if _, ok := doc["report"].(map[string]any); !ok {
		t.Errorf("report = %v, want an (empty) object", doc["report"])
	}
}

// --dry-run keeps today's shape: the bare plan.
func TestUpgradeMachineOutput_DryRunPrintsBarePlan(t *testing.T) {
	srv := fakeaws.New(t, upgradeWorld())
	stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--dry-run", "-o", "yaml")
	if err != nil {
		t.Fatalf("upgrade --dry-run: %v\nstderr:\n%s", err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "yaml", stdout).(map[string]any)
	if doc["targetVersion"] != "1.32" || doc["clusterName"] != "prod" {
		t.Errorf("dry-run document = %v, want the bare plan", doc)
	}
	if got := srv.Cluster("prod"); got.Version != "1.31" {
		t.Errorf("dry run changed the control plane to %s", got.Version)
	}
	// StartInsightsRefresh is a write API; a dry run must not call it.
	if slices.Contains(srv.Calls(), "eks POST /clusters/prod/insights-refresh") {
		t.Errorf("dry run started an insights refresh")
	}
}

// -o plain writes only the plan's TSV rows to stdout, for --dry-run and for
// an executed run alike; the summary line, progress, and report go to stderr.
func TestUpgradePlainOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"dry-run", []string{"--dry-run"}},
		{"executed", []string{"--yes", "--poll-interval", "5ms"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { ui.SetPlainOutput(false); pterm.EnableColor() })
			fakeaws.New(t, upgradeWorld())
			args := append([]string{"upgrade", "prod", "--to", "1.32", "-o", "plain"}, tc.args...)
			stdout, stderr, err := runCluster(t, args...)
			if err != nil {
				t.Fatalf("upgrade -o plain: %v\nstderr:\n%s", err, stderr)
			}
			rows := plaintest.Check(t, stdout, upgradePlanPlainHeaders...)
			if len(rows) == 0 {
				t.Fatalf("no plan rows on stdout:\n%s", stdout)
			}
			if !strings.Contains(stderr, "upgrade plan: prod 1.31 -> 1.32") {
				t.Errorf("stderr missing the plan summary line; got:\n%s", stderr)
			}
			if tc.name == "executed" && !strings.Contains(stderr, "Upgrade complete") {
				t.Errorf("stderr missing the completion line; got:\n%s", stderr)
			}
		})
	}
}

// A run that leaves manual steps (a custom-AMI or --skip-nodegroup
// nodegroup) does not say "Upgrade complete": it names the steps left to the
// operator (REF-168).
func TestUpgrade_ManualStepsAreNotComplete(t *testing.T) {
	t.Cleanup(func() { ui.SetPlainOutput(false); pterm.EnableColor() })
	world := func() *fakeaws.Cluster {
		return &fakeaws.Cluster{Name: "prod", Version: "1.31", Nodegroups: []*fakeaws.Nodegroup{
			{Name: "web", Version: "1.31"},
			{Name: "gpu", Version: "1.31", AmiType: "CUSTOM"},
			{Name: "batch", Version: "1.31"},
		}}
	}
	fakeaws.New(t, world())
	_, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--poll-interval", "5ms", "--skip-nodegroup", "batch", "-o", "plain")
	if err != nil {
		t.Fatalf("upgrade: %v\nstderr:\n%s", err, stderr)
	}
	if strings.Contains(stderr, "Upgrade complete") {
		t.Errorf("a run with manual steps says it is complete:\n%s", stderr)
	}
	for _, want := range []string{
		"Upgrade not complete: 2 manual step(s) remain before prod is fully at 1.32.",
		"manual: 1.31 → 1.32: nodegroup gpu → 1.32: custom AMI nodegroup",
		"manual: 1.31 → 1.32: nodegroup batch → 1.32: skipped via --skip-nodegroup",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}

	// A rerun has nothing left for refresh to do, but still names them.
	_, stderr, err = runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--poll-interval", "5ms", "--skip-nodegroup", "batch", "-o", "plain")
	if err != nil {
		t.Fatalf("rerun: %v\nstderr:\n%s", err, stderr)
	}
	if strings.Contains(stderr, "Nothing to do:") || !strings.Contains(stderr, "Nothing left for refresh to do, but 2 manual step(s) remain") {
		t.Errorf("rerun does not name the manual steps:\n%s", stderr)
	}
}

// -o json/yaml can't prompt, so executing without --yes fails before any AWS
// call, with nothing on stdout.
func TestUpgradeMachineOutput_RequiresYes(t *testing.T) {
	srv := fakeaws.New(t, upgradeWorld())
	stdout, _, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "add --yes") {
		t.Fatalf("err = %v, want an error that asks for --yes", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if calls := srv.Calls(); len(calls) != 0 {
		t.Errorf("AWS was called before the flag check: %v", calls)
	}
}

// --poll-interval 0 (or less) is rejected before any AWS call, as in
// `nodegroup update`, instead of being silently ignored.
func TestUpgrade_RejectsNonPositivePollInterval(t *testing.T) {
	for _, pi := range []string{"0", "0s", "-1s"} {
		t.Run(pi, func(t *testing.T) {
			srv := fakeaws.New(t, upgradeWorld())
			_, _, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--dry-run", "--poll-interval", pi)
			if err == nil || !strings.Contains(err.Error(), "--poll-interval must be greater than 0") {
				t.Fatalf("err = %v, want the --poll-interval error", err)
			}
			if calls := srv.Calls(); len(calls) != 0 {
				t.Errorf("AWS was called before the flag check: %v", calls)
			}
		})
	}
}

// A credential failure prints nothing on stdout, and the returned error
// carries the setup help once (main prints it to stderr). Setup makes no STS
// call, so the keys fail on the first EKS call. In a region sweep, every
// region then looks closed, so the command asks STS once to name the cause.
func TestListMachineOutput_CredentialFailure(t *testing.T) {
	expire := func(s *fakeaws.Server) {
		s.FailRegions(func(string) string { return "ExpiredTokenException" })
	}
	for _, tc := range []struct {
		name     string
		regions  string // REFRESH_EKS_REGIONS
		args     []string
		expired  bool // every region answers ExpiredTokenException, not rejected keys
		stsCalls int
	}{
		{name: "home region", args: []string{"list", "-o", "json"}},
		{name: "default sweep", args: []string{"list", "-A", "-o", "json"}, stsCalls: 1},
		{name: "named regions", regions: "us-east-1,eu-west-1", args: []string{"list", "-A", "-o", "json"}, stsCalls: 1},
		// An expired token names itself: no STS call needed.
		{name: "expired, home region", args: []string{"list", "-o", "json"}, expired: true},
		{name: "expired, default sweep", args: []string{"list", "-A", "-o", "json"}, expired: true},
		{name: "expired, named regions", regions: "us-east-1,eu-west-1", args: []string{"list", "-A", "-o", "json"}, expired: true},
		{name: "expired, describe", args: []string{"describe", "prod", "-o", "json"}, expired: true},
		{name: "expired, upgrade-check", args: []string{"upgrade-check", "prod", "-o", "json"}, expired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeaws.New(t, upgradeWorld())
			t.Setenv("REFRESH_EKS_REGIONS", tc.regions)
			if tc.expired {
				expire(srv)
			} else {
				srv.FailCredentials("InvalidClientTokenId")
			}
			stdout, stderr, err := runCluster(t, tc.args...)
			if err == nil {
				t.Fatal("cluster list succeeded with bad credentials")
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			const help = "AWS credentials not configured or invalid"
			if n := strings.Count(err.Error(), help); n != 1 {
				t.Errorf("error carries the credential help %d times, want 1:\n%v", n, err)
			}
			if strings.Contains(stderr, help) {
				t.Errorf("credential help was printed as well as returned (main would print it twice):\n%s", stderr)
			}
			if _, ok := err.(cli.ExitCoder); ok { //nolint:errorlint // mirrors cli.HandleExitCoder, which only honors an unwrapped ExitCoder
				t.Errorf("credential failure should exit 1 through main, got an exit coder: %v", err)
			}
			if n := countCalls(srv.Calls(), "sts "); n != tc.stsCalls {
				t.Errorf("STS calls = %d, want %d: %v", n, tc.stsCalls, srv.Calls())
			}
		})
	}
}

// Command setup resolves credentials without an STS round trip.
func TestSetupMakesNoSTSCall(t *testing.T) {
	srv := fakeaws.New(t, upgradeWorld())
	if _, _, err := runCluster(t, "list", "-o", "json"); err != nil {
		t.Fatalf("cluster list: %v", err)
	}
	if n := countCalls(srv.Calls(), "sts "); n != 0 {
		t.Errorf("STS calls = %d, want 0: %v", n, srv.Calls())
	}
}

func countCalls(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}
