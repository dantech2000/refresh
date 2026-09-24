package statuscmd

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui/plaintest"
)

func runStatusFake(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return fakeaws.Run(t, fakeaws.App(Command()), append([]string{"refresh", "status"}, args...)...)
}

// A cluster whose one nodegroup cannot be described, so its row is
// incomplete and no SSM lookup runs. Version 1.40 is past the compiled-in
// support calendar, so the support tier (exit 3) does not depend on today's
// date.
func incompleteWorld() *fakeaws.Cluster {
	return &fakeaws.Cluster{Name: "prod", Version: "1.40", Nodegroups: []*fakeaws.Nodegroup{
		{Name: "web", Version: "1.40", DescribeNodegroupError: "AccessDeniedException"},
	}}
}

// webFailure is the failure incompleteWorld reports, as decoded JSON/YAML.
var webFailure = map[string]any{
	"kind":         "Nodegroup",
	"name":         "web",
	"cluster":      "prod",
	"region":       "us-east-1",
	"operation":    "eks:DescribeNodegroup",
	"reason":       "AccessDenied",
	"retryable":    false,
	"awsErrorCode": "AccessDeniedException",
}

const webWarning = "warning: nodegroup prod/web (us-east-1): AccessDenied: AccessDeniedException: "

var statusPlainHeaders = []string{"CLUSTER", "REGION", "VERSION", "SUPPORT", "COMPUTE", "STALE AMI", "ADDONS", "HEALTH"}

// The failures contract: one document whose top-level failures names the
// nodegroup (kind, name, reason, operation, retryable, AWS error code), the
// row marked incomplete, one named stderr line, exit 4, and pure TSV for
// -o plain.
func TestStatus_FailuresContract(t *testing.T) {
	fakeaws.New(t, incompleteWorld())

	for _, format := range []string{"json", "yaml"} {
		stdout, stderr, err := runStatusFake(t, "-r", "us-east-1", "-o", format)
		if got := runner.ExitCodeOf(err); got != runner.ExitIncomplete {
			t.Fatalf("-o %s: exit = %d (%v), want 4\nstderr:\n%s", format, got, err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, format, stdout)
		fakeaws.RequireFailures(t, doc, webFailure)
		row := doc.(map[string]any)["clusters"].([]any)[0].(map[string]any)
		if row["incomplete"] != true {
			t.Errorf("-o %s: row incomplete = %v, want true", format, row["incomplete"])
		}
		if _, ok := row["errors"]; ok {
			t.Errorf("-o %s: row still has the removed errors key: %v", format, row)
		}
		if strings.Count(stderr, webWarning) != 1 {
			t.Errorf("-o %s: stderr does not name the failure once:\n%s", format, stderr)
		}
	}

	stdout, stderr, err := runStatusFake(t, "-r", "us-east-1", "-o", "plain")
	if got := runner.ExitCodeOf(err); got != runner.ExitIncomplete {
		t.Fatalf("-o plain: exit = %d (%v), want 4", got, err)
	}
	if rows := plaintest.Check(t, stdout, statusPlainHeaders...); len(rows) != 1 {
		t.Errorf("-o plain: %d rows, want 1", len(rows))
	}
	if !strings.Contains(stderr, webWarning) {
		t.Errorf("-o plain: stderr lacks %q:\n%s", webWarning, stderr)
	}

	// The human table lists the failure under INCOMPLETE DATA; stderr does
	// not repeat it.
	stdout, stderr, _ = runStatusFake(t, "-r", "us-east-1")
	if !strings.Contains(stdout, "INCOMPLETE DATA") || !strings.Contains(stdout, "nodegroup prod/web (us-east-1): AccessDenied") {
		t.Errorf("table lacks the INCOMPLETE DATA section:\n%s", stdout)
	}
	if strings.Contains(stderr, "warning: nodegroup") {
		t.Errorf("table run repeats the failure on stderr:\n%s", stderr)
	}
}

// A region that cannot be listed is a Region failure (eks:ListClusters),
// next to the row failures, so the fleet is never mistaken for whole.
func TestStatus_FailedRegionInFailures(t *testing.T) {
	srv := fakeaws.New(t, incompleteWorld())
	srv.FailRegions(func(region string) string {
		if region == "us-west-2" {
			return "ExpiredTokenException"
		}
		return ""
	})

	stdout, stderr, err := runStatusFake(t, "-r", "us-east-1", "-r", "us-west-2", "-o", "json")
	if got := runner.ExitCodeOf(err); got != runner.ExitIncomplete {
		t.Fatalf("exit = %d (%v), want 4\nstderr:\n%s", got, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout)
	fakeaws.RequireFailures(t, doc, webFailure, map[string]any{
		"kind":         "Region",
		"name":         "us-west-2",
		"region":       "us-west-2",
		"operation":    "eks:ListClusters",
		"reason":       "CredentialError",
		"retryable":    false,
		"awsErrorCode": "ExpiredTokenException",
		"error":        "ExpiredTokenException: fakeaws: region us-west-2 answers ExpiredTokenException",
	})
	if !strings.Contains(stderr, "warning: region us-west-2: CredentialError: ExpiredTokenException") {
		t.Errorf("stderr does not name the failed region:\n%s", stderr)
	}
}

// With nothing missing, the document still carries failures: [] and the
// run exits 0.
func TestStatus_NoFailuresIsEmptyList(t *testing.T) {
	fakeaws.New(t, &fakeaws.Cluster{Name: "prod", Version: "1.40", Nodegroups: []*fakeaws.Nodegroup{
		{Name: "web", Version: "1.40", AmiType: "CUSTOM"}, // no SSM lookup
	}})
	stdout, stderr, err := runStatusFake(t, "-r", "us-east-1", "-o", "json")
	if err != nil {
		t.Fatalf("status: %v\nstderr:\n%s", err, stderr)
	}
	fakeaws.RequireFailures(t, fakeaws.RequireOneDocument(t, "json", stdout))
	if !strings.Contains(stdout, `"failures": []`) {
		t.Errorf("failures is not an empty list:\n%s", stdout)
	}
}
