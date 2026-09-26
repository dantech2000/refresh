package nodegroup

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// A change EKS is already making elsewhere on the cluster stops `nodegroup
// update` before the prompts and before any change: exit 3, naming it.
func TestUpdate_BusyClusterExitsThree(t *testing.T) {
	withTerminal(t, true)
	asked := withScalePrompt(t, true, "y")
	srv := fakeaws.New(t, prodCluster(
		&fakeaws.Nodegroup{Name: "web", Version: "1.31"},
		&fakeaws.Nodegroup{Name: "other", Version: "1.31", Status: "UPDATING"},
	))
	_, stderr, err := runNodegroup(t, "update", "prod", "web", "--skip-health-check")
	if code := exitCodeOf(err); code != 3 {
		t.Fatalf("exit code = %d (err %v), want 3\nstderr:\n%s", code, err, stderr)
	}
	if want := "prod is busy (nodegroup other UPDATING); nothing was started"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want %q", err, want)
	}
	if calledPath(srv, "/update-version") || *asked != 0 {
		t.Errorf("a busy cluster must not prompt (asked %d) or start an update", *asked)
	}
}

// A target nodegroup that is already UPDATING is not "busy": the run skips
// it, as before. A dry run previews a busy cluster.
func TestUpdate_BusyTargetOrDryRunGoesOn(t *testing.T) {
	fakeaws.New(t, prodCluster(
		&fakeaws.Nodegroup{Name: "web", Version: "1.31", Status: "UPDATING"},
		&fakeaws.Nodegroup{Name: "other", Version: "1.31", Status: "UPDATING"},
	))
	if _, stderr, err := runNodegroup(t, "update", "prod", "--skip-health-check", "--yes", "-o", "json"); err != nil {
		t.Fatalf("update of UPDATING targets: %v\nstderr:\n%s", err, stderr)
	}
	if _, stderr, err := runNodegroup(t, "update", "prod", "web", "--dry-run"); err != nil {
		t.Fatalf("dry run on a busy cluster: %v\nstderr:\n%s", err, stderr)
	}
}

// Fleet mode skips a busy cluster with status Busy (exit 3) and rolls the
// others.
func TestFleetUpdate_SkipsBusyCluster(t *testing.T) {
	srv := fakeaws.New(t,
		&fakeaws.Cluster{Name: "calm", Version: "1.31", Nodegroups: []*fakeaws.Nodegroup{{Name: "web", Version: "1.31"}}},
		&fakeaws.Cluster{Name: "prod", Version: "1.31", Nodegroups: []*fakeaws.Nodegroup{{Name: "web", Version: "1.31"}},
			Addons: []*fakeaws.Addon{{Name: "vpc-cni", Version: "v1.18.0", Status: "UPDATING"}}},
	)
	stdout, stderr, err := runNodegroup(t, "update", "--all-clusters", "-r", "us-east-1", "-n", "web", "--skip-health-check", "--yes", "--poll-interval", "5ms", "-o", "json")
	if code := exitCodeOf(err); code != 3 {
		t.Fatalf("exit code = %d (err %v), want 3\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	status := map[string]map[string]any{}
	for _, c := range doc["clusters"].([]any) {
		m := c.(map[string]any)
		status[m["cluster"].(string)] = m
	}
	if status["prod"]["status"] != "Busy" || status["calm"]["status"] != "Succeeded" {
		t.Fatalf("statuses = prod %v, calm %v; want Busy, Succeeded", status["prod"]["status"], status["calm"]["status"])
	}
	if got, _ := status["prod"]["changesInProgress"].([]any); len(got) != 1 || got[0] != "add-on vpc-cni UPDATING" {
		t.Errorf("changesInProgress = %v", status["prod"]["changesInProgress"])
	}
	if calledPath(srv, "/clusters/prod/node-groups/web/update-version") {
		t.Error("a busy cluster in the fleet must not be rolled")
	}
}

// nodegroup scale refuses on a busy cluster before the prompt, and a dry
// run still previews it.
func TestScale_BusyClusterExitsThree(t *testing.T) {
	asked := withScalePrompt(t, true, "y")
	srv := fakeaws.New(t, prodCluster(
		&fakeaws.Nodegroup{Name: "ng-a", Version: "1.31", Desired: 3, Min: 1, Max: 5},
		&fakeaws.Nodegroup{Name: "ng-b", Version: "1.31", Status: "UPDATING"},
	))
	_, stderr, err := runNodegroup(t, "scale", "prod", "ng-a", "--desired", "2")
	if code := exitCodeOf(err); code != 3 {
		t.Fatalf("exit code = %d (err %v), want 3\nstderr:\n%s", code, err, stderr)
	}
	if !strings.Contains(err.Error(), "prod is busy (nodegroup ng-b UPDATING)") {
		t.Errorf("error = %v", err)
	}
	if calledPath(srv, "/update-config") || *asked != 0 {
		t.Errorf("a busy cluster must not prompt (asked %d) or scale", *asked)
	}
	if _, stderr, err := runNodegroup(t, "scale", "prod", "ng-a", "--desired", "2", "--dry-run"); err != nil {
		t.Fatalf("dry run on a busy cluster: %v\nstderr:\n%s", err, stderr)
	}
}
