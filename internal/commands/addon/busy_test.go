package addon

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// addon update refuses to start on a cluster EKS is changing (exit 3), for
// one add-on and for --all, before the prompt. An update of the target
// add-on that is already running is not a refusal, and a dry run previews.
func TestUpdate_BusyCluster(t *testing.T) {
	busyNodegroup := func() *fakeaws.Cluster {
		c := addonCluster(&fakeaws.Addon{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}})
		c.Nodegroups = []*fakeaws.Nodegroup{{Name: "ng-a", Version: "1.31", Status: "UPDATING"}}
		return c
	}
	for _, args := range [][]string{
		{"update", "prod", "vpc-cni", "--yes"},
		{"update", "prod", "--all", "--yes"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			srv := fakeaws.New(t, busyNodegroup())
			_, stderr, err := runAddon(t, args...)
			if code := exitCodeOf(err); code != 3 {
				t.Fatalf("exit code = %d (err %v), want 3\nstderr:\n%s", code, err, stderr)
			}
			if want := "prod is busy (nodegroup ng-a UPDATING); nothing was started"; !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want %q", err, want)
			}
			if n := updateCalls(srv); n != 0 {
				t.Errorf("%d UpdateAddon call(s) on a busy cluster", n)
			}
		})
	}

	t.Run("dry run", func(t *testing.T) {
		fakeaws.New(t, busyNodegroup())
		if _, stderr, err := runAddon(t, "update", "prod", "vpc-cni", "--dry-run"); err != nil {
			t.Fatalf("dry run: %v\nstderr:\n%s", err, stderr)
		}
	})

	t.Run("target already updating", func(t *testing.T) {
		fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "vpc-cni", Version: "v1.19.0", Status: "UPDATING", Available: []string{"v1.19.0", "v1.18.0"}}))
		_, stderr, err := runAddon(t, "update", "prod", "vpc-cni", "--yes")
		if err != nil && strings.Contains(err.Error(), "is busy") {
			t.Fatalf("the target's own update refused the run: %v\nstderr:\n%s", err, stderr)
		}
	})
}

// #433: with -o json, a refusal prints the command's document with nothing
// started. Exit 3 either way.
func TestBusyRefusalsPrintADocument(t *testing.T) {
	busyNodegroup := func() *fakeaws.Cluster {
		c := addonCluster(&fakeaws.Addon{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}})
		c.Nodegroups = []*fakeaws.Nodegroup{{Name: "ng-a", Version: "1.31", Status: "UPDATING"}}
		return c
	}
	t.Run("one add-on", func(t *testing.T) {
		fakeaws.New(t, busyNodegroup())
		stdout, stderr, err := runAddon(t, "update", "prod", "vpc-cni", "--yes", "-o", "json")
		if code := exitCodeOf(err); code != 3 {
			t.Fatalf("exit = %d (%v)\nstderr:\n%s", code, err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if doc["kind"] != "AddonUpdate" || doc["status"] != "Busy" || doc["addonName"] != "vpc-cni" || fmt.Sprint(doc["changesInProgress"]) != "[nodegroup ng-a UPDATING]" {
			t.Errorf("document = %v", doc)
		}
		fakeaws.RequireFailures(t, doc)
	})
	t.Run("all", func(t *testing.T) {
		fakeaws.New(t, busyNodegroup())
		stdout, _, err := runAddon(t, "update", "prod", "--all", "--yes", "-o", "json")
		if code := exitCodeOf(err); code != 3 {
			t.Fatalf("exit = %d (%v)", code, err)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if doc["kind"] != "AddonUpdateAll" || fmt.Sprint(doc["results"]) != "[]" || fmt.Sprint(doc["changesInProgress"]) != "[nodegroup ng-a UPDATING]" {
			t.Errorf("document = %v", doc)
		}
	})
	t.Run("all unreadable", func(t *testing.T) {
		c := addonCluster(&fakeaws.Addon{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}, DescribeAddonError: "AccessDeniedException"})
		fakeaws.New(t, c)
		stdout, _, err := runAddon(t, "update", "prod", "--all", "--yes", "-o", "json")
		if code := exitCodeOf(err); code != 3 {
			t.Fatalf("exit = %d (%v)", code, err)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		fs, _ := doc["failures"].([]any)
		if len(fs) != 1 || !strings.Contains(fmt.Sprint(fs[0]), "AccessDenied") {
			t.Errorf("failures = %v, want the failed read", doc["failures"])
		}
	})
}
