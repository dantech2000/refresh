package nodegroup

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// The health verdict's failures carry the run's region, like the top-level
// failures and the stderr lines: all three come from one list and must agree
// (output.md "Failures"). The command used to build its checker without the
// region, so health.failures lacked it.
func TestUpdateHealthOnly_FailuresCarryRegion(t *testing.T) {
	fakeaws.New(t, prodCluster(
		&fakeaws.Nodegroup{Name: "web", Version: "1.31"},
		&fakeaws.Nodegroup{Name: "sys", Version: "1.31", DescribeNodegroupError: "AccessDeniedException"},
	))
	stdout, stderr, err := runNodegroup(t, "update", "prod", "--health-only", "-o", "json")
	// The fake has no Kubernetes access, so the verdict also warns; the exit
	// code follows the documented precedence and is not what this checks.
	if err == nil {
		t.Fatalf("want a non-zero exit\nstderr:\n%s", stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	fs, _ := doc["failures"].([]any)
	if len(fs) == 0 {
		t.Fatalf("no failures in the health document: %s", stdout)
	}
	for _, raw := range fs {
		f := raw.(map[string]any)
		if f["region"] != "us-east-1" {
			t.Errorf("failure %v %v: region = %v, want us-east-1", f["kind"], f["name"], f["region"])
		}
	}
	if !strings.Contains(stderr, "warning: nodegroup prod/sys (us-east-1): AccessDenied:") {
		t.Errorf("stderr lacks the regional failure line:\n%s", stderr)
	}
}
