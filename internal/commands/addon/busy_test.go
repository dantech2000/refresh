package addon

import (
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
