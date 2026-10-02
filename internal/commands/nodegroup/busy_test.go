package nodegroup

import (
	"fmt"
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
	// Another nodegroup's roll: the refusal says how to scale outside
	// refresh, since a roll waiting for capacity frees none itself.
	if !strings.Contains(err.Error(), "aws eks update-nodegroup-config --cluster-name prod --nodegroup-name ng-a --scaling-config desiredSize=2") {
		t.Errorf("error = %v, want the scale command outside refresh", err)
	}
	if calledPath(srv, "/update-config") || *asked != 0 {
		t.Errorf("a busy cluster must not prompt (asked %d) or scale", *asked)
	}
	if _, stderr, err := runNodegroup(t, "scale", "prod", "ng-a", "--desired", "2", "--dry-run"); err != nil {
		t.Fatalf("dry run on a busy cluster: %v\nstderr:\n%s", err, stderr)
	}
}

// #433: with -o json, a refusal prints the command's document with nothing
// started: changesInProgress for a busy cluster, the failed read for one
// refresh could not check. Exit 3 either way.
func TestBusyRefusalsPrintADocument(t *testing.T) {
	t.Run("update busy", func(t *testing.T) {
		fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}, &fakeaws.Nodegroup{Name: "other", Version: "1.31", Status: "UPDATING"}))
		stdout, stderr, err := runNodegroup(t, "update", "prod", "web", "--skip-health-check", "--yes", "-o", "json")
		if code := exitCodeOf(err); code != 3 {
			t.Fatalf("exit = %d (%v)\nstderr:\n%s", code, err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if doc["kind"] != "NodegroupUpdate" || fmt.Sprint(doc["changesInProgress"]) != "[nodegroup other UPDATING]" || fmt.Sprint(doc["nodegroups"]) != "[]" {
			t.Errorf("document = %v", doc)
		}
		fakeaws.RequireFailures(t, doc)
	})
	t.Run("update unreadable", func(t *testing.T) {
		fakeaws.New(t, &fakeaws.Cluster{Name: "prod", Version: "1.31", ListNodegroupsError: "AccessDeniedException"})
		stdout, _, err := runNodegroup(t, "update", "prod", "--yes", "-o", "json")
		if code := exitCodeOf(err); code != 3 {
			t.Fatalf("exit = %d (%v)", code, err)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if _, ok := doc["changesInProgress"]; ok {
			t.Errorf("an unreadable cluster has no changesInProgress: %v", doc)
		}
		failureWant{kind: "Cluster", name: "prod", reason: "AccessDenied", operation: "eks:ListNodegroups"}.check(t, "failures[0]", onlyFailure(t, doc))
	})
	t.Run("scale busy", func(t *testing.T) {
		fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "ng-a", Version: "1.31", Desired: 3, Min: 1, Max: 5}, &fakeaws.Nodegroup{Name: "ng-b", Version: "1.31", Status: "UPDATING"}))
		stdout, stderr, err := runNodegroup(t, "scale", "prod", "ng-a", "--desired", "2", "--yes", "-o", "json")
		if code := exitCodeOf(err); code != 3 {
			t.Fatalf("exit = %d (%v)\nstderr:\n%s", code, err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		before, _ := doc["before"].(map[string]any)
		if doc["outcome"] != "Busy" || fmt.Sprint(doc["changesInProgress"]) != "[nodegroup ng-b UPDATING]" || fmt.Sprint(before["desired"]) != "3" {
			t.Errorf("document = %v", doc)
		}
	})
	// From review: the busy check comes first, as before #433. A nodegroup
	// that cannot be read does not turn a busy refusal into exit 1; its
	// sizes are left out.
	t.Run("scale busy, nodegroup unreadable", func(t *testing.T) {
		fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "ng-a", Version: "1.31", DescribeNodegroupError: "AccessDeniedException"}, &fakeaws.Nodegroup{Name: "ng-b", Version: "1.31", Status: "UPDATING"}))
		stdout, stderr, err := runNodegroup(t, "scale", "prod", "ng-a", "--desired", "2", "--yes", "-o", "json")
		if code := exitCodeOf(err); code != 3 {
			t.Fatalf("exit = %d (%v)\nstderr:\n%s", code, err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if _, ok := doc["before"]; ok {
			t.Errorf("sizes of an unreadable nodegroup: %v", doc)
		}
		// Bounds only: the bounds check reads the nodegroup too, but after
		// the busy check, so the run still refuses with a document (exit 3).
		for _, bound := range [][]string{{"--min", "2"}, {"--max", "6"}} {
			stdout, stderr, err := runNodegroup(t, append([]string{"scale", "prod", "ng-a", "--yes", "-o", "json"}, bound...)...)
			if code := exitCodeOf(err); code != 3 {
				t.Fatalf("%v: exit = %d (%v)\nstderr:\n%s", bound, code, err, stderr)
			}
			// The busy scan cannot read ng-a either, so it cannot tell:
			// Blocked, with the failed read.
			if doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any); doc["outcome"] != "Blocked" || !strings.Contains(fmt.Sprint(doc["failures"]), "eks:DescribeNodegroup") {
				t.Errorf("%v: document = %v", bound, doc)
			}
		}
	})
}

// No hint when scaling outside refresh is no way out: the target is
// changing too, something other than a nodegroup roll is, or EKS would
// reject the sizes.
func TestScale_BusyHintOnlyWhenItHelps(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b string // the nodegroups' statuses
		args []string
	}{
		{"target and another rolling", "UPDATING", "UPDATING", []string{"--desired", "2"}},
		{"another being deleted", "ACTIVE", "DELETING", []string{"--desired", "2"}},
		{"desired below min", "ACTIVE", "UPDATING", []string{"--desired", "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withScalePrompt(t, true, "y")
			fakeaws.New(t, prodCluster(
				&fakeaws.Nodegroup{Name: "ng-a", Version: "1.31", Desired: 3, Min: 1, Max: 5, Status: tc.a},
				&fakeaws.Nodegroup{Name: "ng-b", Version: "1.31", Status: tc.b},
			))
			_, _, err := runNodegroup(t, append([]string{"scale", "prod", "ng-a"}, tc.args...)...)
			if code := exitCodeOf(err); code != 3 || strings.Contains(err.Error(), "update-nodegroup-config") {
				t.Fatalf("exit %d, err = %v", code, err)
			}
		})
	}
}
