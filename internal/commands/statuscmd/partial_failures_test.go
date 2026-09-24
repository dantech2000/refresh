package statuscmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

func runStatusFake(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return fakeaws.Run(t, fakeaws.App(Command()), append([]string{"refresh", "status"}, args...)...)
}

// A cluster whose one nodegroup cannot be described, so its row has errors
// and no SSM lookup runs.
func incompleteWorld() *fakeaws.Cluster {
	return &fakeaws.Cluster{Name: "prod", Version: "1.33", Nodegroups: []*fakeaws.Nodegroup{
		{Name: "web", Version: "1.33", DescribeNodegroupError: "AccessDeniedException"},
	}}
}

// A region that fails the sweep is named on stderr and listed under
// "failures" in the JSON document, so the fleet is never mistaken for whole.
func TestStatus_FailedRegionInFailures(t *testing.T) {
	srv := fakeaws.New(t, incompleteWorld())
	srv.FailRegions(func(region string) string {
		if region == "us-west-2" {
			return "ExpiredTokenException"
		}
		return ""
	})

	stdout, stderr, err := runStatusFake(t, "-r", "us-east-1", "-r", "us-west-2", "-o", "json")
	if err == nil {
		t.Fatalf("status succeeded with a failed region\nstderr:\n%s", stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	want := []any{map[string]any{
		"region": "us-west-2",
		"error":  "ExpiredTokenException: fakeaws: region us-west-2 answers ExpiredTokenException",
	}}
	if !reflect.DeepEqual(doc["failures"], want) {
		t.Errorf("failures = %#v, want %#v", doc["failures"], want)
	}
	if !strings.Contains(stderr, "warning: region us-west-2: ExpiredTokenException") {
		t.Errorf("stderr does not name the failed region:\n%s", stderr)
	}

	// Every region answered: no "failures" key.
	srv.FailRegions(nil)
	stdout, _, _ = runStatusFake(t, "-r", "us-east-1", "-o", "json")
	doc = fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if _, ok := doc["failures"]; ok {
		t.Errorf("failures present with no failed region: %v", doc["failures"])
	}
}

// A cluster row with errors is named on stderr with a one-line reason in the
// machine and plain formats; stderr used to carry only a count.
func TestStatus_IncompleteRowNamedOnStderr(t *testing.T) {
	fakeaws.New(t, incompleteWorld())

	for _, format := range []string{"json", "yaml", "plain"} {
		stdout, stderr, err := runStatusFake(t, "-r", "us-east-1", "-o", format)
		if err == nil {
			t.Fatalf("-o %s: status succeeded with an incomplete row\nstderr:\n%s", format, stderr)
		}
		if format != "plain" {
			fakeaws.RequireOneDocument(t, format, stdout)
		}
		want := "warning: cluster prod (us-east-1): nodegroup(s): web: "
		if !strings.Contains(stderr, want) {
			t.Errorf("-o %s: stderr lacks %q:\n%s", format, want, stderr)
		}
		for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
			if strings.HasPrefix(line, "warning: cluster") && strings.Count(line, "warning:") != 1 {
				t.Errorf("-o %s: row warning is not one line: %q", format, line)
			}
		}
	}

	// The human table already lists the row under INCOMPLETE DATA; stderr
	// does not repeat it.
	stdout, stderr, _ := runStatusFake(t, "-r", "us-east-1")
	if !strings.Contains(stdout, "INCOMPLETE DATA") {
		t.Errorf("table lacks the INCOMPLETE DATA section:\n%s", stdout)
	}
	if strings.Contains(stderr, "warning: cluster prod") {
		t.Errorf("table run repeats the row on stderr:\n%s", stderr)
	}
}
