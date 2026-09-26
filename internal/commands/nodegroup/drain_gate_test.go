package nodegroup

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// withDrainBlockers replaces the PDB read with a fixed answer per cluster
// and records the nodegroups each call asked about.
func withDrainBlockers(t *testing.T, byCluster map[string]drainBlockers, err error) *[][]string {
	t.Helper()
	var mu sync.Mutex
	asked := new([][]string)
	orig := drainBlockersFn
	drainBlockersFn = func(_ context.Context, _ aws.Config, _ *eks.Client, cluster string, nodegroups []string, _ updateAMIFlags, _ bool) (drainBlockers, error) {
		mu.Lock()
		defer mu.Unlock()
		*asked = append(*asked, slices.Clone(nodegroups))
		if err != nil {
			return nil, err
		}
		return byCluster[cluster], nil
	}
	t.Cleanup(func() { drainBlockersFn = orig })
	return asked
}

var webBlocked = map[string]drainBlockers{"prod": {"web": {"PDB default/web allows 0 disruptions"}}}

// A PDB that blocks the drain refuses the run before anything starts: exit
// 3, naming the PDB and the fixes. Only the nodegroups the run would roll
// are checked.
func TestUpdate_DrainBlockerRefuses(t *testing.T) {
	for _, format := range []string{"table", "json"} {
		t.Run(format, func(t *testing.T) {
			asked := withDrainBlockers(t, webBlocked, nil)
			srv := fakeaws.New(t, prodCluster(
				&fakeaws.Nodegroup{Name: "web", Version: "1.31"},
				&fakeaws.Nodegroup{Name: "custom", Version: "1.31", AmiType: "CUSTOM"},
			))
			stdout, stderr, err := runNodegroup(t, "update", "prod", "--yes", "-o", format)
			if code := exitCodeOf(err); code != 3 {
				t.Fatalf("exit code = %d (err %v), want 3\nstderr:\n%s", code, err, stderr)
			}
			for _, want := range []string{"web: PDB default/web allows 0 disruptions", "nothing was started", "--force"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if calledPath(srv, "/update-version") {
				t.Error("a drain-blocked run must not start an update")
			}
			if len(*asked) != 1 || !slices.Equal((*asked)[0], []string{"web"}) {
				t.Errorf("drain gate asked about %v, want [[web]]", *asked)
			}
			if format == "json" {
				doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
				ngs := nodegroupEntries(t, doc)
				if ngs["web"]["status"] != "DrainBlocked" || ngs["custom"]["status"] != "Skipped" {
					t.Errorf("nodegroups = %v", ngs)
				}
				if b, _ := ngs["web"]["drainBlockers"].([]any); len(b) != 1 {
					t.Errorf("web drainBlockers = %v", ngs["web"]["drainBlockers"])
				}
			}
		})
	}
}

// --force rolls past the blockers with a warning on stderr.
func TestUpdate_DrainBlockerForce(t *testing.T) {
	withDrainBlockers(t, webBlocked, nil)
	srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	_, stderr, err := runNodegroup(t, "update", "prod", "web", "--yes", "--force", "--poll-interval", "5ms", "-o", "json")
	if err != nil {
		t.Fatalf("update --force: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "--force: rolling prod despite these drain blockers: web: PDB default/web allows 0 disruptions") {
		t.Errorf("stderr does not warn about the blockers:\n%s", stderr)
	}
	if !calledPath(srv, "/update-version") {
		t.Error("--force must start the update")
	}
}

// Without Kubernetes access the gate cannot run and does not stop the run
// (the kube notice says so).
func TestUpdate_DrainGateWithoutKube(t *testing.T) {
	withDrainBlockers(t, nil, health.ErrNoKubeClient)
	srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	_, stderr, err := runNodegroup(t, "update", "prod", "web", "--yes", "--poll-interval", "5ms", "-o", "json")
	if err != nil {
		t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
	}
	if !calledPath(srv, "/update-version") {
		t.Error("the update did not start")
	}
}

// PDBs that cannot be read are not clear: the gate refuses (exit 3), as for
// a blocker, and --force rolls past it with a warning.
func TestUpdate_DrainGateReadErrorRefuses(t *testing.T) {
	withDrainBlockers(t, nil, context.DeadlineExceeded)
	srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	_, stderr, err := runNodegroup(t, "update", "prod", "web", "--yes", "--poll-interval", "5ms", "-o", "json")
	if code := exitCodeOf(err); code != 3 || !strings.Contains(err.Error(), "web: the PodDisruptionBudgets could not be read") {
		t.Fatalf("update = %v (exit %d), want exit 3\nstderr:\n%s", err, code, stderr)
	}
	if calledPath(srv, "/update-version") {
		t.Fatal("the update started with unread PDBs")
	}

	withDrainBlockers(t, nil, context.DeadlineExceeded)
	srv = fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	_, stderr, err = runNodegroup(t, "update", "prod", "web", "--yes", "--force", "--poll-interval", "5ms", "-o", "json")
	if err != nil {
		t.Fatalf("update --force: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "--force: rolling prod despite these drain blockers: web: the PodDisruptionBudgets could not be read") {
		t.Errorf("stderr does not warn:\n%s", stderr)
	}
	if !calledPath(srv, "/update-version") {
		t.Error("--force must start the update")
	}
}

// --skip-health-check skips the drain gate too, as in cluster upgrade.
func TestUpdate_SkipHealthCheckSkipsDrainGate(t *testing.T) {
	asked := withDrainBlockers(t, webBlocked, nil)
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	if _, stderr, err := runNodegroup(t, "update", "prod", "web", "--skip-health-check", "--poll-interval", "5ms", "-o", "json"); err != nil {
		t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
	}
	if len(*asked) != 0 {
		t.Errorf("drain gate ran with --skip-health-check: %v", *asked)
	}
}

// The dry run shows the blockers and exits as the real run would: 3, or the
// usual code with --force.
func TestUpdate_DryRunShowsDrainBlockers(t *testing.T) {
	withDrainBlockers(t, webBlocked, nil)
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))

	stdout, stderr, err := runNodegroup(t, "update", "prod", "web", "--dry-run", "-o", "json")
	if code := exitCodeOf(err); code != 3 {
		t.Fatalf("json: exit code = %d (err %v), want 3\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if b, _ := nodegroupEntries(t, doc)["web"]["drainBlockers"].([]any); len(b) != 1 {
		t.Errorf("plan web = %v, want its drainBlockers", nodegroupEntries(t, doc)["web"])
	}

	stdout, _, err = runNodegroup(t, "update", "prod", "web", "--dry-run")
	if code := exitCodeOf(err); code != 3 || !strings.Contains(stdout, "PDB drain gate: would be REFUSED") {
		t.Fatalf("table: exit %d (err %v), stdout:\n%s", code, err, stdout)
	}

	stdout, _, err = runNodegroup(t, "update", "prod", "web", "--dry-run", "--force")
	if err != nil || !strings.Contains(stdout, "would be overridden by --force") {
		t.Fatalf("--force: err %v, stdout:\n%s", err, stdout)
	}
}

// Fleet mode gates each cluster: a blocked cluster is DrainBlocked (exit 3)
// and the others roll. The fleet dry run marks it the same way.
func TestFleetUpdate_DrainBlockedCluster(t *testing.T) {
	withDrainBlockers(t, webBlocked, nil)
	fleet := func() {
		fakeaws.New(t,
			&fakeaws.Cluster{Name: "calm", Version: "1.31", Nodegroups: []*fakeaws.Nodegroup{{Name: "web", Version: "1.31"}}},
			prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}),
		)
	}
	statuses := func(stdout string) map[string]any {
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		out := map[string]any{}
		for _, c := range doc["clusters"].([]any) {
			m := c.(map[string]any)
			out[m["cluster"].(string)] = m["status"]
		}
		return out
	}

	fleet()
	stdout, stderr, err := runNodegroup(t, "update", "--all-clusters", "-r", "us-east-1", "--yes", "--poll-interval", "5ms", "-o", "json")
	if code := exitCodeOf(err); code != 3 {
		t.Fatalf("exit code = %d (err %v), want 3\nstderr:\n%s", code, err, stderr)
	}
	if got := statuses(stdout); got["prod"] != "DrainBlocked" || got["calm"] != "Succeeded" {
		t.Errorf("statuses = %v, want prod DrainBlocked, calm Succeeded", got)
	}

	fleet()
	stdout, stderr, err = runNodegroup(t, "update", "--all-clusters", "-r", "us-east-1", "--dry-run", "-o", "json")
	if code := exitCodeOf(err); code != 3 {
		t.Fatalf("dry run: exit code = %d (err %v), want 3\nstderr:\n%s", code, err, stderr)
	}
	if got := statuses(stdout); got["prod"] != "DrainBlocked" || got["calm"] != "Planned" {
		t.Errorf("dry-run statuses = %v, want prod DrainBlocked, calm Planned", got)
	}
}
