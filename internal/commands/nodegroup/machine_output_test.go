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
			ngs := nodegroupEntries(t, doc)
			if got := ngs["busy"]; got["status"] != "Skipped" || got["reason"] != "AlreadyUpdating" {
				t.Errorf("busy = %v, want Skipped/AlreadyUpdating", got)
			}
			if got := ngs["custom"]; got["status"] != "Skipped" || got["reason"] != "CustomAMI" {
				t.Errorf("custom = %v, want Skipped/CustomAMI", got)
			}
			requireNoFailures(t, doc)
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
	web := nodegroupEntries(t, doc)["web"]
	if web["status"] != "Failed" || web["updateId"] != nil {
		t.Errorf("web = %v, want Failed with no update ID", web)
	}
	want := failureWant{kind: "Nodegroup", name: "web", cluster: "prod", reason: "InvalidRequest", operation: "eks:UpdateNodegroupVersion"}
	want.check(t, "nodegroups[web].failure", web["failure"])
	want.check(t, "failures[0]", onlyFailure(t, doc))
	requireStderrLine(t, stderr, "warning: nodegroup prod/web (us-east-1): InvalidRequest: InvalidRequestException: fake update failure for web")
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
	if web := nodegroupEntries(t, doc)["web"]; web["status"] != "Succeeded" || web["updateId"] == nil {
		t.Errorf("web = %v, want Succeeded with its update ID", web)
	}
	requireNoFailures(t, doc)
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
			if actions["web"] != "Update" || actions["busy"] != "SkipUpdating" {
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
			// The fake cluster warns (no Kubernetes access): exit 2.
			if code := exitCodeOf(err); code != 2 {
				t.Fatalf("exit code = %d (err %v), want 2 (WARN)\nstderr:\n%s", code, err, stderr)
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
	if web := nodegroupEntries(t, doc)["web"]; web["status"] != "Skipped" {
		t.Errorf("web = %v, want Skipped", web)
	}
	if h, ok := doc["health"].(map[string]any); !ok || h["decision"] != "Warn" {
		t.Errorf("health = %v, want the WARN verdict in the run summary", doc["health"])
	}
	if !strings.Contains(stderr, "Health check reported warnings; proceeding: Node Health: Nodegroups still scaling") {
		t.Errorf("stderr missing the auto-accepted warnings; got:\n%s", stderr)
	}
}

// Without --yes, a warning verdict can't be confirmed with -o json. The run
// stops; the error names the warning checks, stderr has the health report,
// and stdout has the empty run summary with the verdict.
func TestUpdateMachineOutput_HealthWarnNeedsYes(t *testing.T) {
	srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	stdout, stderr, err := runNodegroup(t, "update", "prod", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "-o json does not prompt") {
		t.Fatalf("err = %v, want a --yes hint that names -o json", err)
	}
	if !strings.Contains(err.Error(), "Cluster Capacity:") {
		t.Errorf("error does not name the warning check: %v", err)
	}
	if !strings.Contains(stderr, "Cluster Health Assessment") {
		t.Errorf("stderr has no health report; got:\n%s", stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	h, ok := doc["health"].(map[string]any)
	if !ok || h["decision"] != "Warn" {
		t.Errorf("health = %v, want a WARN verdict", doc["health"])
	}
	if ngs, _ := doc["nodegroups"].([]any); len(ngs) != 0 || calledPath(srv, "/update-version") {
		t.Error("a run stopped by the health gate must not start an update")
	}
	requireNoFailures(t, doc)
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
	if entry["cluster"] != "prod" || entry["status"] != "Planned" || !ok || plan["dryRun"] != true {
		t.Errorf("fleet dry-run entry = %v", entry)
	}
	requireNoFailures(t, doc)
}

// Fleet --health-only with -o yaml collects one verdict per cluster into the
// one fleet document instead of printing a document per cluster. The exit
// code stays the worst per-cluster verdict: the fake cluster warns (no
// Kubernetes access), so it is 2, as for the same cluster on its own. It
// changes nothing, so it needs no --yes.
func TestFleetMachineOutput_HealthOnlyCollectsVerdicts(t *testing.T) {
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	stdout, stderr, err := runNodegroup(t, "update", "--all-clusters", "-r", "us-east-1", "--health-only", "-o", "yaml")
	if code := exitCodeOf(err); code != 2 {
		t.Fatalf("exit code = %d (err %v), want 2 (WARN)\nstderr:\n%s", code, err, stderr)
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

// A fleet roll records each cluster's pre-flight verdict too, not only with
// --health-only, so a blocked or warned cluster shows why.
func TestFleetMachineOutput_RollRecordsHealth(t *testing.T) {
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31", Status: "UPDATING"}))
	stdout, stderr, err := runNodegroup(t, "update", "--all-clusters", "-r", "us-east-1", "--yes", "-o", "json")
	if err != nil {
		t.Fatalf("fleet: %v\nstderr:\n%s", err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	entry := doc["clusters"].([]any)[0].(map[string]any)
	if h, ok := entry["health"].(map[string]any); !ok || h["decision"] != "Warn" {
		t.Errorf("fleet entry health = %v, want the WARN verdict", entry["health"])
	}
}

func calledPath(srv *fakeaws.Server, fragment string) bool {
	for _, c := range srv.Calls() {
		if strings.Contains(c, fragment) {
			return true
		}
	}
	return false
}
