package nodegroup

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/render"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui/plaintest"
)

// amiLookupFailure is a denied ssm:GetParameter latest-AMI lookup.
func amiLookupFailure(name string) *diag.Failure {
	f := diag.FromError(diag.KindNodegroup, name, diag.OpGetParameter, mocks.AccessDenied())
	f.Cluster, f.Region = "prod", "us-east-1"
	return &f
}

func TestNodegroupListLines_AMILookupFailed(t *testing.T) {
	th := render.New(render.ColorNone, true)
	items := []nodegroupsvc.NodegroupSummary{
		{Name: "general", Status: "ACTIVE", AMIStatus: types.AMIUnknown, AMILookupFailure: amiLookupFailure("general")},
	}
	joined := strings.Join(nodegroupListLines(th, "prod", items), "\n")
	if !strings.Contains(joined, amiLookupFailedText) {
		t.Errorf("AMI cell should say %q:\n%s", amiLookupFailedText, joined)
	}
	if got := plainAMICell(items[0]); !strings.Contains(got, amiLookupFailedText) {
		t.Errorf("plain AMI cell = %q, want %q", got, amiLookupFailedText)
	}
}

// A failed AMI lookup is advisory: one stderr line for all the nodegroups,
// naming the IAM action and the reason, and no exit code.
func TestWarnAMILookup(t *testing.T) {
	var buf bytes.Buffer
	warnAMILookup(&buf, []nodegroupsvc.NodegroupSummary{
		{Name: "ng-a", AMILookupFailure: amiLookupFailure("ng-a")},
		{Name: "ng-b", AMILookupFailure: amiLookupFailure("ng-b")},
		{Name: "ng-c"},
	})
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "could not look up the latest recommended AMI for 2 nodegroup(s)") {
		t.Errorf("want exactly one AMI warning line for 2 nodegroups:\n%s", out)
	}
	if !strings.Contains(out, "ssm:GetParameter: AccessDenied") {
		t.Errorf("warning should name ssm:GetParameter and the reason:\n%s", out)
	}

	buf.Reset()
	warnAMILookup(&buf, []nodegroupsvc.NodegroupSummary{{Name: "ng-a"}})
	if buf.Len() != 0 {
		t.Errorf("no lookup failure wrote %q", buf.String())
	}
}

// With failures, JSON still prints what was gathered and lists the failures,
// so "count" isn't mistaken for the cluster's full nodegroup count. A
// complete list carries failures: [].
func TestWriteNodegroupList_JSONIncludesFailures(t *testing.T) {
	items := []nodegroupsvc.NodegroupSummary{{Name: "ng-ok", Status: "ACTIVE", AMIStatus: types.AMILatest}}
	bad := diag.FromError(diag.KindNodegroup, "ng-bad", diag.OpDescribeNodegroup, mocks.Throttling())
	out := captureStdout(t, func() {
		if err := writeNodegroupList("json", "prod", items, diag.List{bad}); err != nil {
			t.Errorf("writeNodegroupList: %v", err)
		}
	})
	var payload nodegroupsvc.NodegroupList
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	if payload.Count != 1 || len(payload.Nodegroups) != 1 || len(payload.Failures) != 1 || payload.Failures[0] != bad {
		t.Errorf("payload = %+v, want 1 nodegroup and the ng-bad failure", payload)
	}

	clean := captureStdout(t, func() {
		if err := writeNodegroupList("json", "prod", items, nil); err != nil {
			t.Errorf("writeNodegroupList: %v", err)
		}
	})
	if !strings.Contains(clean, `"failures": []`) {
		t.Errorf("clean JSON should carry failures: []:\n%s", clean)
	}
}

// nodegroup list: a nodegroup that could not be described is left out and
// reported in the document's failures, on one stderr line, and by exit 4. A
// failed latest-AMI lookup on another nodegroup is advisory: it stays on its
// row as amiLookupFailure and does not count.
func TestList_FailuresContract(t *testing.T) {
	world := func() *fakeaws.Cluster {
		return prodCluster(
			&fakeaws.Nodegroup{Name: "api", Version: "1.31"}, // AL2: the fake has no SSM, so the AMI lookup fails
			&fakeaws.Nodegroup{Name: "web", Version: "1.31", DescribeNodegroupError: "AccessDeniedException"},
		)
	}
	web := map[string]any{
		"kind":         "Nodegroup",
		"name":         "web",
		"cluster":      "prod",
		"region":       "us-east-1",
		"operation":    "eks:DescribeNodegroup",
		"reason":       "AccessDenied",
		"retryable":    false,
		"awsErrorCode": "AccessDeniedException",
	}
	const warning = "warning: nodegroup prod/web (us-east-1): AccessDenied: AccessDeniedException: "

	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			fakeaws.New(t, world())
			stdout, stderr, err := runNodegroup(t, "list", "prod", "-o", format)
			if got := runner.ExitCodeOf(err); got != runner.ExitIncomplete {
				t.Fatalf("exit = %d (%v), want 4\nstderr:\n%s", got, err, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, format, stdout)
			fakeaws.RequireFailures(t, doc, web)
			rows := doc.(map[string]any)["nodegroups"].([]any)
			if len(rows) != 1 {
				t.Fatalf("nodegroups = %v, want api only", rows)
			}
			row := rows[0].(map[string]any)
			lookup, ok := row["amiLookupFailure"].(map[string]any)
			if !ok || lookup["operation"] != "ssm:GetParameter" || lookup["name"] != "api" || lookup["cluster"] != "prod" {
				t.Errorf("api amiLookupFailure = %v, want the advisory ssm:GetParameter failure", row["amiLookupFailure"])
			}
			if strings.Count(stderr, warning) != 1 {
				t.Errorf("stderr does not name web once:\n%s", stderr)
			}
			if !strings.Contains(stderr, "could not look up the latest recommended AMI for 1 nodegroup(s)") {
				t.Errorf("stderr lacks the advisory AMI line:\n%s", stderr)
			}
		})
	}
	t.Run("plain", func(t *testing.T) {
		fakeaws.New(t, world())
		stdout, stderr, err := runNodegroup(t, "list", "prod", "-o", "plain")
		if got := runner.ExitCodeOf(err); got != runner.ExitIncomplete {
			t.Fatalf("exit = %d (%v), want 4", got, err)
		}
		rows := plaintest.Check(t, stdout, "NAME", "STATUS", "INSTANCE", "VERSION", "AMI", "NODES")
		if len(rows) != 1 || rows[0][0] != "api" || rows[0][4] != amiLookupFailedText {
			t.Errorf("rows = %q, want the api row with %q", rows, amiLookupFailedText)
		}
		if !strings.Contains(stderr, warning) {
			t.Errorf("stderr lacks %q:\n%s", warning, stderr)
		}
	})
	t.Run("table lists the failure", func(t *testing.T) {
		fakeaws.New(t, world())
		stdout, stderr, _ := runNodegroup(t, "list", "prod")
		if !strings.Contains(stdout, "INCOMPLETE DATA") || !strings.Contains(stdout, "nodegroup prod/web (us-east-1): AccessDenied") {
			t.Errorf("table lacks the INCOMPLETE DATA section:\n%s", stdout)
		}
		if strings.Contains(stderr, "warning: nodegroup") {
			t.Errorf("table run repeats the failure on stderr:\n%s", stderr)
		}
	})
	t.Run("AMI lookup alone exits 0", func(t *testing.T) {
		fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "api", Version: "1.31"}))
		stdout, stderr, err := runNodegroup(t, "list", "prod", "-o", "json")
		if err != nil {
			t.Fatalf("list: %v\nstderr:\n%s", err, stderr)
		}
		fakeaws.RequireFailures(t, fakeaws.RequireOneDocument(t, "json", stdout))
	})
}

// nodegroup describe reads one nodegroup or fails (exit 1), so failures is
// always []. A failed latest-AMI lookup is advisory there too.
func TestDescribe_FailuresContract(t *testing.T) {
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "api", Version: "1.31"}))
	for _, format := range []string{"json", "yaml"} {
		stdout, stderr, err := runNodegroup(t, "describe", "prod", "api", "-o", format)
		if err != nil {
			t.Fatalf("-o %s: describe: %v\nstderr:\n%s", format, err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, format, stdout)
		fakeaws.RequireFailures(t, doc)
		lookup, ok := doc.(map[string]any)["amiLookupFailure"].(map[string]any)
		if !ok || lookup["operation"] != "ssm:GetParameter" || lookup["kind"] != "Nodegroup" {
			t.Errorf("-o %s: amiLookupFailure = %v, want the advisory ssm:GetParameter failure", format, doc.(map[string]any)["amiLookupFailure"])
		}
		if _, ok := doc.(map[string]any)["amiLookupError"]; ok {
			t.Errorf("-o %s: document still has the removed amiLookupError key", format)
		}
		if !strings.Contains(stderr, "could not look up the latest recommended AMI for 1 nodegroup(s)") {
			t.Errorf("-o %s: stderr lacks the advisory AMI line:\n%s", format, stderr)
		}
	}
}
