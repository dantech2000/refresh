package nodegroup

import (
	"errors"
	"strings"
	"testing"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/monitoring"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
)

// nodegroupEntries indexes a document's nodegroups list by name.
func nodegroupEntries(t *testing.T, doc map[string]any) map[string]map[string]any {
	t.Helper()
	list, ok := doc["nodegroups"].([]any)
	if !ok {
		t.Fatalf("nodegroups = %#v, want a list", doc["nodegroups"])
	}
	out := map[string]map[string]any{}
	for _, it := range list {
		m := it.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

// requireNoFailures fails t unless doc has "failures": [].
func requireNoFailures(t *testing.T, doc map[string]any) {
	t.Helper()
	fs, ok := doc["failures"].([]any)
	if !ok || len(fs) != 0 {
		t.Errorf("failures = %#v, want []", doc["failures"])
	}
}

// onlyFailure returns the one entry of doc's failures.
func onlyFailure(t *testing.T, doc map[string]any) any {
	t.Helper()
	fs, ok := doc["failures"].([]any)
	if !ok || len(fs) != 1 {
		t.Fatalf("failures = %#v, want one entry", doc["failures"])
	}
	return fs[0]
}

// failureWant is the expected keys of an encoded diag.Failure. Empty fields
// are not checked; retryable is checked when checkRetryable is set.
type failureWant struct {
	kind, name, cluster, reason, operation string
	updateID                               bool // want a non-empty updateId
	retryable, checkRetryable              bool
}

func (w failureWant) check(t *testing.T, where string, v any) {
	t.Helper()
	f, ok := v.(map[string]any)
	if !ok {
		t.Errorf("%s = %#v, want a failure object", where, v)
		return
	}
	for key, want := range map[string]string{"kind": w.kind, "name": w.name, "cluster": w.cluster, "reason": w.reason, "operation": w.operation} {
		if want != "" && f[key] != want {
			t.Errorf("%s.%s = %v, want %s (%v)", where, key, f[key], want, f)
		}
	}
	if _, ok := f["retryable"].(bool); !ok {
		t.Errorf("%s has no retryable bool: %v", where, f)
	}
	if w.checkRetryable && f["retryable"] != w.retryable {
		t.Errorf("%s.retryable = %v, want %v", where, f["retryable"], w.retryable)
	}
	if e, _ := f["error"].(string); e == "" || strings.Contains(e, "\n") {
		t.Errorf("%s.error = %q, want one non-empty line", where, e)
	}
	if id, _ := f["updateId"].(string); w.updateID && id == "" {
		t.Errorf("%s has no updateId: %v", where, f)
	}
}

// requireStderrLine fails t unless stderr has line exactly once.
func requireStderrLine(t *testing.T, stderr, line string) {
	t.Helper()
	if n := strings.Count(stderr, line+"\n"); n != 1 {
		t.Errorf("stderr has %q %d time(s), want once; got:\n%s", line, n, stderr)
	}
}

// requireTableFailure fails t unless the table view lists text (the failure
// line without "warning: ") once in its INCOMPLETE DATA section on stdout,
// and stderr does not repeat it.
func requireTableFailure(t *testing.T, stdout, stderr, text string) {
	t.Helper()
	out := ui.StripANSI(stdout)
	if !strings.Contains(out, "INCOMPLETE DATA") || strings.Count(out, text) != 1 {
		t.Errorf("stdout does not list %q once under INCOMPLETE DATA:\n%s", text, out)
	}
	if strings.Contains(stderr, "warning: "+text) {
		t.Errorf("a table run repeats the failure on stderr:\n%s", stderr)
	}
}

// The run document always has its lists, as [] when empty, for a run that
// starts nothing and for a run the health gate stops.
func TestUpdateDocument_EmptyListsAreNotNull(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"nothing to start", []string{"--skip-health-check", "--yes"}},
		{"health gate stops the run", nil},
	}
	for _, format := range []string{"json", "yaml"} {
		for _, tc := range cases {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "busy", Version: "1.31", Status: "UPDATING"}))
				args := append([]string{"update", "prod", "-o", format}, tc.args...)
				stdout, stderr, _ := runNodegroup(t, args...)
				if format == "json" && strings.Contains(stdout, "null") {
					t.Errorf("stdout has null:\n%s\nstderr:\n%s", stdout, stderr)
				}
				doc := fakeaws.RequireOneDocument(t, format, stdout).(map[string]any)
				if _, ok := doc["nodegroups"].([]any); !ok {
					t.Errorf("nodegroups = %#v, want a list", doc["nodegroups"])
				}
				requireNoFailures(t, doc)
			})
		}
	}
}

// Each fleet cluster carries its status and a nodegroups list, and the
// document its failures, also when the cluster stopped before any update.
func TestFleetDocument_EmptyListsAreNotNull(t *testing.T) {
	for _, extra := range [][]string{{"--yes"}, {"--health-only"}} {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "busy", Version: "1.31", Status: "UPDATING"}))
			args := append([]string{"update", "--all-clusters", "-r", "us-east-1", "-o", "json"}, extra...)
			stdout, stderr, _ := runNodegroup(t, args...)
			if strings.Contains(stdout, "null") {
				t.Errorf("stdout has null:\n%s\nstderr:\n%s", stdout, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
			entry := doc["clusters"].([]any)[0].(map[string]any)
			if entry["cluster"] != "prod" || entry["status"] == nil {
				t.Errorf("entry = %v, want prod with a status", entry)
			}
			if _, ok := entry["nodegroups"].([]any); !ok {
				t.Errorf("nodegroups = %#v, want a list", entry["nodegroups"])
			}
			requireNoFailures(t, doc)
		})
	}
}

// A roll that EKS ends Failed or Cancelled has that status, its update ID,
// and a failure (kind Update) in the document and on stderr. The exit code
// stays 1.
func TestUpdateDocument_RollFailure(t *testing.T) {
	for _, tc := range []struct{ eksStatus, status, reason string }{
		{"Failed", "Failed", "UpdateFailed"},
		{"Cancelled", "Cancelled", "UpdateCancelled"},
	} {
		t.Run(tc.eksStatus, func(t *testing.T) {
			srv := fakeaws.New(t, &fakeaws.Cluster{Name: "mr-a", Version: "1.31", Nodegroups: []*fakeaws.Nodegroup{
				{Name: "ng-1", Version: "1.31"},
				{Name: "ng-2", Version: "1.31", UpdateStatus: tc.eksStatus},
			}})
			stdout, stderr, err := runNodegroup(t, "update", "mr-a", "--skip-health-check", "--yes", "--poll-interval", "5ms", "-o", "json")
			if code := exitCodeOf(err); code != 1 {
				t.Fatalf("exit code = %d (err %v), want 1\nstderr:\n%s", code, err, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
			ngs := nodegroupEntries(t, doc)
			if ngs["ng-1"]["status"] != "Succeeded" {
				t.Errorf("ng-1 = %v, want Succeeded", ngs["ng-1"])
			}
			ng2 := ngs["ng-2"]
			id, _ := ng2["updateId"].(string)
			if ng2["status"] != tc.status || id == "" || !calledPath(srv, "/updates/"+id) {
				t.Errorf("ng-2 = %v, want %s with the monitored update's ID", ng2, tc.status)
			}
			want := failureWant{kind: "Update", name: "ng-2", cluster: "mr-a", reason: tc.reason, updateID: true, checkRetryable: true}
			want.check(t, "ng-2.failure", ng2["failure"])
			want.check(t, "failures[0]", onlyFailure(t, doc))
			if tc.eksStatus == "Failed" {
				requireStderrLine(t, stderr, "warning: update mr-a/ng-2 (us-east-1): UpdateFailed: fake update failure")
			}
			if strings.Contains(err.Error(), "fake update failure") {
				t.Errorf("the exit error repeats the failure text on stderr: %v", err)
			}
		})
	}
}

// A fleet cluster whose roll failed is Failed, and the fleet exits 4 with
// the failure in the top-level list.
func TestFleetDocument_RollFailure(t *testing.T) {
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31", UpdateStatus: "Failed"}))
	stdout, stderr, err := runNodegroup(t, "update", "--all-clusters", "-r", "us-east-1", "--skip-health-check", "--yes", "--poll-interval", "5ms", "-o", "json")
	if code := exitCodeOf(err); code != 4 {
		t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	entry := doc["clusters"].([]any)[0].(map[string]any)
	if entry["status"] != "Failed" || nodegroupEntries(t, entry)["web"]["status"] != "Failed" {
		t.Errorf("fleet entry = %v, want the cluster and web Failed", entry)
	}
	if _, ok := entry["failure"]; ok {
		t.Errorf("a nodegroup failure must not be copied to the cluster's failure: %v", entry)
	}
	failureWant{kind: "Update", name: "web", cluster: "prod", reason: "UpdateFailed", updateID: true}.check(t, "failures[0]", onlyFailure(t, doc))
	requireStderrLine(t, stderr, "warning: update prod/web (us-east-1): UpdateFailed: fake update failure")
}

// Misclassification fix: a nodegroup the dry run can't describe was shown as
// skip-updating with exit 0. It is a failure now: action unknown, exit 4.
func TestUpdateDryRun_DescribeFailureIsAFailure(t *testing.T) {
	for _, format := range []string{"json", "table"} {
		t.Run(format, func(t *testing.T) {
			fakeaws.New(t, prodCluster(
				&fakeaws.Nodegroup{Name: "web", Version: "1.31"},
				&fakeaws.Nodegroup{Name: "api", Version: "1.31", DescribeNodegroupError: "AccessDeniedException"},
			))
			stdout, stderr, err := runNodegroup(t, "update", "prod", "--dry-run", "-o", format)
			if code := exitCodeOf(err); code != 4 {
				t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
			}
			const text = "nodegroup prod/api (us-east-1): AccessDenied: AccessDeniedException: fake DescribeNodegroup failure for api"
			if format != "json" {
				requireTableFailure(t, stdout, stderr, text)
				return
			}
			requireStderrLine(t, stderr, "warning: "+text)
			doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
			ngs := nodegroupEntries(t, doc)
			if ngs["api"]["action"] != "Unknown" || ngs["web"]["action"] != "Update" {
				t.Errorf("actions: api=%v web=%v, want unknown/update", ngs["api"]["action"], ngs["web"]["action"])
			}
			want := failureWant{kind: "Nodegroup", name: "api", cluster: "prod", reason: "AccessDenied", operation: "eks:DescribeNodegroup", retryable: false, checkRetryable: true}
			want.check(t, "api.failure", ngs["api"]["failure"])
			want.check(t, "failures[0]", onlyFailure(t, doc))
		})
	}
}

// Misclassification fix: a fleet dry run whose cluster preview failed
// exited 0 with the error as a string. The cluster is Failed with a
// failure now, and the run exits 4.
func TestFleetDryRun_ClusterFailureExitsFour(t *testing.T) {
	for _, format := range []string{"json", "table"} {
		t.Run(format, func(t *testing.T) {
			fakeaws.New(t,
				&fakeaws.Cluster{Name: "a", Version: "1.31", Nodegroups: []*fakeaws.Nodegroup{{Name: "web", Version: "1.31"}}},
				&fakeaws.Cluster{Name: "b", Version: "1.31", ListNodegroupsError: "AccessDeniedException"},
			)
			stdout, stderr, err := runNodegroup(t, "update", "--all-clusters", "-r", "us-east-1", "--dry-run", "-o", format)
			if code := exitCodeOf(err); code != 4 {
				t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
			}
			const text = "cluster b (us-east-1): AccessDenied: AccessDeniedException: fake ListNodegroups failure for b"
			if format != "json" {
				requireTableFailure(t, stdout, stderr, text)
				return
			}
			requireStderrLine(t, stderr, "warning: "+text)
			doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
			byName := map[string]map[string]any{}
			for _, c := range doc["clusters"].([]any) {
				m := c.(map[string]any)
				byName[m["cluster"].(string)] = m
			}
			if byName["a"]["status"] != "Planned" || byName["b"]["status"] != "Failed" || byName["b"]["plan"] != nil {
				t.Errorf("clusters = %v, want a Planned and b Failed with no plan", byName)
			}
			want := failureWant{kind: "Cluster", name: "b", reason: "AccessDenied", operation: "eks:ListNodegroups", checkRetryable: true}
			want.check(t, "b.failure", byName["b"]["failure"])
			want.check(t, "failures[0]", onlyFailure(t, doc))
		})
	}
}

// Misclassification fix: a nodegroup post-roll verification can't describe
// was a verification issue (exit 5). Its state is unknown, so it is a
// failure now (exit 4), and the roll itself still Succeeded.
func TestUpdate_PostRollDescribeFailureExitsFour(t *testing.T) {
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31", DescribeErrorAfterUpdate: "AccessDeniedException"}))
	stdout, stderr, err := runNodegroup(t, "update", "prod", "--skip-health-check", "--yes", "--poll-interval", "5ms", "-o", "json")
	if code := exitCodeOf(err); code != 4 {
		t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	web := nodegroupEntries(t, doc)["web"]
	if web["status"] != "Succeeded" || web["failure"] != nil {
		t.Errorf("web = %v, want Succeeded with no failure of its own", web)
	}
	if v, _ := doc["verification"].(map[string]any); v == nil || v["issues"] != nil {
		t.Errorf("verification = %v, want no issues", doc["verification"])
	}
	failureWant{kind: "Nodegroup", name: "web", cluster: "prod", reason: "AccessDenied", operation: "eks:DescribeNodegroup"}.check(t, "failures[0]", onlyFailure(t, doc))
	if !strings.Contains(stderr, "warning: nodegroup prod/web (us-east-1): AccessDenied: ") {
		t.Errorf("stderr does not name the failure; got:\n%s", stderr)
	}
}

// A cluster whose nodegroups cannot be read is refused before the health
// gate (exit 3): the busy check cannot tell whether EKS is changing one.
func TestUpdate_UnreadableNodegroupsRefuse(t *testing.T) {
	srv := fakeaws.New(t, &fakeaws.Cluster{Name: "prod", Version: "1.31", ListNodegroupsError: "AccessDeniedException"})
	_, stderr, err := runNodegroup(t, "update", "prod", "--yes", "-o", "json")
	if code := exitCodeOf(err); code != 3 {
		t.Fatalf("exit code = %d (err %v), want 3\nstderr:\n%s", code, err, stderr)
	}
	if !strings.Contains(err.Error(), "could not check what EKS is changing on prod") || !strings.Contains(err.Error(), "ListNodegroups") {
		t.Errorf("err = %v, want the busy check naming ListNodegroups", err)
	}
	if calledPath(srv, "/update-version") {
		t.Error("an update started")
	}
}

func TestApplyMonitorResult(t *testing.T) {
	run := newUpdateRun("prod", "us-east-1")
	for _, n := range []string{"ok", "bad", "stopped", "lost", "running"} {
		run.nodegroups = append(run.nodegroups, nodegroupResult{Name: n, Status: ngStarted, UpdateID: "u-" + n})
	}
	run.skip("skipped", skipAlreadyLatest)
	run.applyMonitorResult([]refreshTypes.UpdateProgress{
		{NodegroupName: "ok", UpdateID: "u-ok", Status: ekstypes.UpdateStatusSuccessful},
		{NodegroupName: "bad", UpdateID: "u-bad", Status: ekstypes.UpdateStatusFailed, ErrorMessage: "drain failed"},
		{NodegroupName: "stopped", UpdateID: "u-stopped", Status: ekstypes.UpdateStatusCancelled},
		{NodegroupName: "lost", UpdateID: "u-lost", Status: ekstypes.UpdateStatusInProgress, MonitorErr: errors.New("access denied")},
		{NodegroupName: "running", UpdateID: "u-running", Status: ekstypes.UpdateStatusInProgress},
	}, monitoring.ErrMonitorTimeout)

	want := map[string]struct {
		status nodegroupStatus
		reason diag.Reason
	}{
		"ok":      {ngSucceeded, ""},
		"bad":     {ngFailed, diag.ReasonUpdateFailed},
		"stopped": {ngCancelled, diag.ReasonUpdateCancelled},
		"lost":    {ngInProgress, diag.ReasonNotMonitored},
		"running": {ngInProgress, diag.ReasonTimeout},
		"skipped": {ngSkipped, ""},
	}
	for _, ng := range run.nodegroups {
		w := want[ng.Name]
		if ng.Status != w.status {
			t.Errorf("%s: status = %s, want %s", ng.Name, ng.Status, w.status)
		}
		switch {
		case w.reason == "" && ng.Failure != nil:
			t.Errorf("%s: failure = %+v, want none", ng.Name, ng.Failure)
		case w.reason != "" && (ng.Failure == nil || ng.Failure.Reason != w.reason || ng.Failure.UpdateID != ng.UpdateID ||
			ng.Failure.Kind != diag.KindUpdate || ng.Failure.Cluster != "prod" || ng.Failure.Region != "us-east-1"):
			t.Errorf("%s: failure = %+v, want %s for its update", ng.Name, ng.Failure, w.reason)
		}
	}
	if got := run.rollFailures(); got != 3 {
		t.Errorf("rollFailures = %d, want 3 (Failed, Cancelled, not monitored)", got)
	}
	if f := run.nodegroups[1].Failure; f.Error != "drain failed" {
		t.Errorf("bad: error = %q, want the EKS error message", f.Error)
	}
}
