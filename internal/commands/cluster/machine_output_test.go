package cluster

import (
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
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

// A credential failure prints nothing on stdout, and the returned error
// carries the setup help once (main prints it to stderr).
func TestListMachineOutput_CredentialFailure(t *testing.T) {
	srv := fakeaws.New(t, upgradeWorld())
	srv.FailCredentials("InvalidClientTokenId")
	stdout, stderr, err := runCluster(t, "list", "-o", "json")
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
	if _, ok := err.(cli.ExitCoder); ok {
		t.Errorf("credential failure should exit 1 through main, got an exit coder: %v", err)
	}
}
