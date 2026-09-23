package nodegroup

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/render"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/types"
)

func ssmDenied() error {
	return errors.Join(errors.New("reading SSM parameter /aws/service/eks/optimized-ami/1.32/amazon-linux-2023/x86_64/standard/recommended/image_id"),
		&smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform: ssm:GetParameter"})
}

func TestNodegroupListLines_AMILookupFailed(t *testing.T) {
	th := render.New(render.ColorNone, true)
	items := []nodegroupsvc.NodegroupSummary{
		{Name: "general", Status: "ACTIVE", AMIStatus: types.AMIUnknown, AMILookupError: "AccessDeniedException"},
	}
	joined := strings.Join(nodegroupListLines(th, "prod", items), "\n")
	if !strings.Contains(joined, amiLookupFailedText) {
		t.Errorf("AMI cell should say %q:\n%s", amiLookupFailedText, joined)
	}
	if got := plainAMICell(items[0]); !strings.Contains(got, amiLookupFailedText) {
		t.Errorf("plain AMI cell = %q, want %q", got, amiLookupFailedText)
	}
}

// Nodegroups that could not be described are named on stderr and make the
// command exit 4; a failed AMI lookup alone warns once and does not.
func TestReportListProblems(t *testing.T) {
	t.Run("describe failures", func(t *testing.T) {
		var buf bytes.Buffer
		err := reportListProblems(&buf, "prod", nodegroupsvc.ListResult{
			Failures: []string{"ng-a: not evaluated: context deadline exceeded", "ng-b: ThrottlingException"},
		})
		if err == nil || !strings.Contains(err.Error(), "2 nodegroup(s)") {
			t.Fatalf("err = %v, want a non-nil error counting 2 nodegroups", err)
		}
		if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
			t.Errorf("exit code = %d, want 4 (incomplete data)", code)
		}
		for _, want := range []string{"ng-a", "ng-b"} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("warnings missing %q:\n%s", want, buf.String())
			}
		}
	})

	t.Run("AMI lookup failure only", func(t *testing.T) {
		var buf bytes.Buffer
		err := reportListProblems(&buf, "prod", nodegroupsvc.ListResult{
			AMILookupFailures: []string{"ng-a: latest AMI lookup failed", "ng-b: latest AMI lookup failed"},
			AMILookupErr:      ssmDenied(),
		})
		if err != nil {
			t.Fatalf("err = %v, want nil (rows already say lookup failed)", err)
		}
		out := buf.String()
		if strings.Count(out, "could not look up the latest recommended AMI") != 1 {
			t.Errorf("want exactly one AMI warning:\n%s", out)
		}
		if !strings.Contains(out, "ssm:GetParameter") {
			t.Errorf("warning should name the missing ssm:GetParameter permission:\n%s", out)
		}
	})

	t.Run("clean", func(t *testing.T) {
		var buf bytes.Buffer
		if err := reportListProblems(&buf, "prod", nodegroupsvc.ListResult{}); err != nil || buf.Len() != 0 {
			t.Errorf("err = %v, output = %q; want nil and nothing", err, buf.String())
		}
	})
}

// With failures, JSON still prints what was gathered and lists the failures,
// so "count" isn't mistaken for the cluster's full nodegroup count.
func TestWriteNodegroupList_JSONIncludesFailures(t *testing.T) {
	items := []nodegroupsvc.NodegroupSummary{{Name: "ng-ok", Status: "ACTIVE", AMIStatus: types.AMILatest}}
	out := captureStdout(t, func() {
		if err := writeNodegroupList("json", "prod", items, []string{"ng-bad: ThrottlingException"}); err != nil {
			t.Errorf("writeNodegroupList: %v", err)
		}
	})
	var payload struct {
		Count      int               `json:"count"`
		Nodegroups []json.RawMessage `json:"nodegroups"`
		Failures   []string          `json:"failures"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	if payload.Count != 1 || len(payload.Nodegroups) != 1 || len(payload.Failures) != 1 {
		t.Errorf("payload = %+v, want 1 nodegroup and 1 failure", payload)
	}

	clean := captureStdout(t, func() {
		if err := writeNodegroupList("json", "prod", items, nil); err != nil {
			t.Errorf("writeNodegroupList: %v", err)
		}
	})
	if strings.Contains(clean, "failures") {
		t.Errorf("clean JSON should not carry a failures key:\n%s", clean)
	}
}
