package cluster

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// cluster describe: an add-on that could not be read is incomplete data. The
// document is printed first, then the command exits 4 (REF-165).
func TestDescribe_PartialReadExitsIncomplete(t *testing.T) {
	fakeaws.New(t, &fakeaws.Cluster{Name: "prod", Version: "1.32", Addons: []*fakeaws.Addon{
		{Name: "coredns", Version: "v1.11.4"},
		{Name: "vpc-cni", Version: "v1.19.0", DescribeAddonError: "AccessDeniedException"},
	}})
	stdout, stderr, err := runCluster(t, "describe", "prod", "-o", "json")
	if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
		t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if doc["name"] != "prod" {
		t.Errorf("name = %v, want prod", doc["name"])
	}
	if !strings.Contains(stderr, "vpc-cni") {
		t.Errorf("stderr does not name the unreadable add-on:\n%s", stderr)
	}

	// A complete read exits 0.
	fakeaws.New(t, &fakeaws.Cluster{Name: "prod", Version: "1.32", Addons: []*fakeaws.Addon{{Name: "coredns", Version: "v1.11.4"}}})
	if _, stderr, err := runCluster(t, "describe", "prod", "-o", "json"); err != nil {
		t.Fatalf("describe: %v\nstderr:\n%s", err, stderr)
	}
}

// cluster upgrade: a plan with a blocker exits 3 (blocked), with and without
// --dry-run, and changes nothing.
func TestUpgrade_BlockedPlanExitsBlocked(t *testing.T) {
	for _, args := range [][]string{
		{"upgrade", "prod", "--to", "1.33", "--dry-run", "-o", "json"},
		{"upgrade", "prod", "--to", "1.33", "--yes", "-o", "json"},
		{"upgrade", "prod", "--to", "1.33", "--dry-run"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			srv := fakeaws.New(t, &fakeaws.Cluster{
				Name: "prod", Version: "1.32",
				Insights: []*fakeaws.Insight{{ID: "ins-err", Name: "Deprecated APIs", Status: "ERROR"}},
			})
			_, stderr, err := runCluster(t, args...)
			if code := runner.ExitCodeOf(err); code != runner.ExitBlocked {
				t.Fatalf("exit code = %d (err %v), want 3\nstderr:\n%s", code, err, stderr)
			}
			if got := srv.Cluster("prod").Version; got != "1.32" {
				t.Errorf("version = %s, want 1.32 (nothing changed)", got)
			}
		})
	}
}
