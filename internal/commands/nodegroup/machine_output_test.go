package nodegroup

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// These tests run `refresh nodegroup update` end to end against the fake AWS
// endpoint and hold it to the machine-output contract: with -o json/yaml,
// stdout is exactly one document and every human line goes to stderr.

func runNodegroup(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return fakeaws.Run(t, fakeaws.App(Command()), append([]string{"refresh", "nodegroup"}, args...)...)
}

func prodCluster(ngs ...*fakeaws.Nodegroup) *fakeaws.Cluster {
	return &fakeaws.Cluster{Name: "prod", Version: "1.31", Nodegroups: ngs}
}

func TestUpdateMachineOutput_SkipPath(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			fakeaws.New(t, prodCluster(
				&fakeaws.Nodegroup{Name: "busy", Version: "1.31", Status: "UPDATING"},
				&fakeaws.Nodegroup{Name: "custom", Version: "1.31", AmiType: "CUSTOM"},
			))
			stdout, stderr, err := runNodegroup(t, "update", "prod", "--skip-health-check", "--yes", "-o", format)
			if err != nil {
				t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, format, stdout).(map[string]any)
			if doc["cluster"] != "prod" {
				t.Errorf("cluster = %v, want prod", doc["cluster"])
			}
			if got := doc["skipped"]; !containsItem(got, "busy") {
				t.Errorf("skipped = %v, want [busy]", got)
			}
			if got := doc["customUnmanaged"]; !containsItem(got, "custom") {
				t.Errorf("customUnmanaged = %v, want [custom]", got)
			}
			for _, want := range []string{"already UPDATING", "custom AMI"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr missing notice %q; got:\n%s", want, stderr)
				}
			}
		})
	}
}

func TestUpdateMachineOutput_FailurePath(t *testing.T) {
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31", FailUpdate: true}))
	stdout, stderr, err := runNodegroup(t, "update", "prod", "--skip-health-check", "--yes", "-o", "json")
	if code := exitCodeOf(err); code != 4 {
		t.Fatalf("exit code = %d (err %v), want 4 (update failed to start)", code, err)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if !containsItem(doc["failed"], "web") {
		t.Errorf("failed = %v, want [web]", doc["failed"])
	}
	if !strings.Contains(stderr, "Failed to update nodegroup web") {
		t.Errorf("stderr missing the failure notice; got:\n%s", stderr)
	}
}

// A started roll is monitored quietly: the monitor's progress tree must not
// reach stdout.
func TestUpdateMachineOutput_StartedAndMonitored(t *testing.T) {
	srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	stdout, stderr, err := runNodegroup(t, "update", "prod", "--skip-health-check", "--yes", "--poll-interval", "5ms", "-o", "yaml")
	if err != nil {
		t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "yaml", stdout).(map[string]any)
	if !containsItem(doc["started"], "web") {
		t.Errorf("started = %v, want [web]", doc["started"])
	}
	if _, ok := doc["verification"]; !ok {
		t.Errorf("document has no verification block: %v", doc)
	}
	if !calledPath(srv, "/clusters/prod/updates/") {
		t.Error("the update was never monitored (no DescribeUpdate call)")
	}
}

func TestUpdateMachineOutput_DryRun(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			srv := fakeaws.New(t, prodCluster(
				&fakeaws.Nodegroup{Name: "web", Version: "1.31"},
				&fakeaws.Nodegroup{Name: "busy", Version: "1.31", Status: "UPDATING"},
			))
			stdout, stderr, err := runNodegroup(t, "update", "prod", "--dry-run", "-o", format)
			if err != nil {
				t.Fatalf("dry-run: %v\nstderr:\n%s", err, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, format, stdout).(map[string]any)
			if doc["dryRun"] != true || doc["cluster"] != "prod" {
				t.Errorf("dry-run document = %v", doc)
			}
			actions := map[string]any{}
			for _, ng := range doc["nodegroups"].([]any) {
				m := ng.(map[string]any)
				actions[m["name"].(string)] = m["action"]
			}
			if actions["web"] != "update" || actions["busy"] != "skip-updating" {
				t.Errorf("actions = %v, want web=update busy=skip-updating", actions)
			}
			if calledPath(srv, "/update-version") {
				t.Error("a dry run must not start an update")
			}
		})
	}
}

// --health-only emits the verdict as the one document, for json and yaml
// alike, with the verdict in the exit code (0/2/3).
func TestUpdateMachineOutput_HealthOnly(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
			stdout, stderr, err := runNodegroup(t, "update", "prod", "--health-only", "-o", format)
			if code := exitCodeOf(err); code != 0 && code != 2 && code != 3 {
				t.Fatalf("exit code = %d (err %v), want a verdict code 0/2/3\nstderr:\n%s", code, err, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, format, stdout).(map[string]any)
			if _, ok := doc["decision"]; !ok {
				t.Errorf("health document has no decision: %v", doc)
			}
		})
	}
}

// Without --skip-health-check the pre-flight report runs too (against the
// fake, with no Kubernetes access, it warns). The report must stay off
// stdout, and the auto-accepted warning goes to stderr.
func TestUpdateMachineOutput_HealthGateStaysOffStdout(t *testing.T) {
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31", Status: "UPDATING"}))
	stdout, stderr, err := runNodegroup(t, "update", "prod", "--yes", "-o", "json")
	if err != nil {
		t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if !containsItem(doc["skipped"], "web") {
		t.Errorf("skipped = %v, want [web]", doc["skipped"])
	}
	if !strings.Contains(stderr, "Health check reported warnings; proceeding") {
		t.Errorf("stderr missing the auto-accepted warning; got:\n%s", stderr)
	}
}

// Without --yes, a warning verdict can't be confirmed with -o json: the run
// stops with an error and stdout stays empty.
func TestUpdateMachineOutput_HealthWarnNeedsYes(t *testing.T) {
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	stdout, _, err := runNodegroup(t, "update", "prod", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "-o json does not prompt") {
		t.Fatalf("err = %v, want a --yes hint that names -o json", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

func TestUpdateMachineOutput_HealthOnlyWithoutACheckFails(t *testing.T) {
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	stdout, _, err := runNodegroup(t, "update", "prod", "--health-only", "--skip-health-check", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "no -o json result") {
		t.Fatalf("err = %v, want the no-result error", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

// An ambiguous pattern never prompts with -o json/yaml: it needs --yes.
func TestUpdateMachineOutput_AmbiguousPatternNeedsYes(t *testing.T) {
	fakeaws.New(t, prodCluster(
		&fakeaws.Nodegroup{Name: "web-a", Version: "1.31"},
		&fakeaws.Nodegroup{Name: "web-b", Version: "1.31"},
	))
	stdout, _, err := runNodegroup(t, "update", "prod", "web", "--skip-health-check", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "-o json does not prompt") {
		t.Fatalf("err = %v, want a --yes hint that names -o json", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

// Fleet mode with no clusters found still emits one (empty) document.
func TestFleetMachineOutput_NoClusters(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			fakeaws.New(t)
			stdout, stderr, err := runNodegroup(t, "update", "--all-clusters", "-r", "us-east-1", "-o", format)
			if err != nil {
				t.Fatalf("fleet: %v\nstderr:\n%s", err, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, format, stdout).(map[string]any)
			if clusters, ok := doc["clusters"].([]any); !ok || len(clusters) != 0 {
				t.Errorf("clusters = %v, want an empty list", doc["clusters"])
			}
		})
	}
}

func TestFleetMachineOutput_DryRun(t *testing.T) {
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	stdout, stderr, err := runNodegroup(t, "update", "--all-clusters", "-r", "us-east-1", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatalf("fleet dry-run: %v\nstderr:\n%s", err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	clusters := doc["clusters"].([]any)
	if len(clusters) != 1 {
		t.Fatalf("clusters = %v, want one entry", clusters)
	}
	entry := clusters[0].(map[string]any)
	plan, ok := entry["plan"].(map[string]any)
	if entry["cluster"] != "prod" || !ok || plan["dryRun"] != true {
		t.Errorf("fleet dry-run entry = %v", entry)
	}
}

// Fleet --health-only with -o yaml collects one verdict per cluster into the
// one fleet document instead of printing a document per cluster. The exit
// code stays the worst per-cluster verdict.
func TestFleetMachineOutput_HealthOnlyCollectsVerdicts(t *testing.T) {
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	stdout, stderr, err := runNodegroup(t, "update", "--all-clusters", "-r", "us-east-1", "--health-only", "--yes", "-o", "yaml")
	if code := exitCodeOf(err); code != 0 && code != 3 {
		t.Fatalf("exit code = %d (err %v), want 0 or 3\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "yaml", stdout).(map[string]any)
	clusters := doc["clusters"].([]any)
	if len(clusters) != 1 {
		t.Fatalf("clusters = %v, want one entry", clusters)
	}
	if _, ok := clusters[0].(map[string]any)["health"]; !ok {
		t.Errorf("fleet --health-only entry has no health verdict: %v", clusters[0])
	}
}

func containsItem(list any, want string) bool {
	items, _ := list.([]any)
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}

func calledPath(srv *fakeaws.Server, fragment string) bool {
	for _, c := range srv.Calls() {
		if strings.Contains(c, fragment) {
			return true
		}
	}
	return false
}
