package change

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/exitcode"
)

// An upgrade of one minor version sends the control plane, the add-ons
// that the new control plane cannot run, each nodegroup, and then the
// other add-ons, and nothing else that changes the cluster.
func TestUpgrade_WholeRun(t *testing.T) {
	e := newEnv(t, engineWorld(nil))
	res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
	if res.Status != Succeeded || res.ExitCode() != exitcode.OK {
		t.Errorf("status = %s, exit %d, err %v; want Succeeded, exit 0", res.Status, res.ExitCode(), res.Err)
	}
	if diff := cmp.Diff(wholeUpgrade(), e.sent()); diff != "" {
		t.Errorf("requests (-want +got):\n%s", diff)
	}
	t.Log(stepStatuses(res))
}
