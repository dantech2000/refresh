package nodegroup

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

// outcomeLists are the list fields of a run summary. Each one must encode as
// a list, never as null.
var outcomeLists = []string{"started", "skipped", "customUnmanaged", "failed", "rollFailures"}

func requireOutcomeLists(t *testing.T, where string, doc map[string]any) {
	t.Helper()
	for _, key := range outcomeLists {
		v, ok := doc[key]
		if !ok {
			t.Errorf("%s: no %q key in %v", where, key, doc)
			continue
		}
		if _, isList := v.([]any); !isList {
			t.Errorf("%s: %q = %#v, want a list ([] when empty)", where, key, v)
		}
	}
}

// Empty outcome lists encode as [] in the single-cluster summary, for a run
// that starts nothing and for a run the health gate stops.
func TestUpdateMachineOutput_EmptyListsAreNotNull(t *testing.T) {
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
				requireOutcomeLists(t, "summary", doc)
			})
		}
	}
}

// Each fleet cluster's outcomes carry [] for empty lists and the cluster
// name, also when the cluster stopped before any update started.
func TestFleetMachineOutput_EmptyListsAreNotNull(t *testing.T) {
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
			outcomes := entry["outcomes"].(map[string]any)
			if outcomes["cluster"] != "prod" {
				t.Errorf("outcomes.cluster = %v, want prod", outcomes["cluster"])
			}
			requireOutcomeLists(t, "fleet outcomes", outcomes)
		})
	}
}

// A roll that EKS ends Failed or Cancelled is in the summary under
// rollFailures with its nodegroup, update ID, and status. The exit code
// stays 1.
func TestUpdateMachineOutput_RollFailureInSummary(t *testing.T) {
	for _, status := range []string{"Failed", "Cancelled"} {
		t.Run(status, func(t *testing.T) {
			srv := fakeaws.New(t, &fakeaws.Cluster{Name: "mr-a", Version: "1.31", Nodegroups: []*fakeaws.Nodegroup{
				{Name: "ng-1", Version: "1.31"},
				{Name: "ng-2", Version: "1.31", UpdateStatus: status},
				{Name: "ng-3", Version: "1.31"},
			}})
			stdout, stderr, err := runNodegroup(t, "update", "mr-a", "--skip-health-check", "--yes", "--poll-interval", "5ms", "-o", "json")
			// A plain error (no exit coder, -1 here) exits 1.
			if code := exitCodeOf(err); err == nil || (code != -1 && code != 1) {
				t.Fatalf("exit code = %d (err %v), want 1\nstderr:\n%s", code, err, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
			requireOutcomeLists(t, "summary", doc)
			failures := doc["rollFailures"].([]any)
			if len(failures) != 1 {
				t.Fatalf("rollFailures = %v, want one entry for ng-2", failures)
			}
			f := failures[0].(map[string]any)
			if f["nodegroup"] != "ng-2" || f["status"] != status {
				t.Errorf("rollFailures[0] = %v, want ng-2 %s", f, status)
			}
			id, _ := f["updateId"].(string)
			if id == "" || !calledPath(srv, "/updates/"+id) {
				t.Errorf("rollFailures[0].updateId = %q, want the monitored update's ID", id)
			}
			if status == "Failed" && f["error"] != "fake update failure" {
				t.Errorf("rollFailures[0].error = %v, want the EKS error message", f["error"])
			}
		})
	}
}

// A fleet cluster's outcomes carry its roll failures too.
func TestFleetMachineOutput_RollFailureInOutcomes(t *testing.T) {
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31", UpdateStatus: "Failed"}))
	stdout, stderr, err := runNodegroup(t, "update", "--all-clusters", "-r", "us-east-1", "--skip-health-check", "--yes", "--poll-interval", "5ms", "-o", "json")
	if err == nil {
		t.Fatalf("fleet run with a failed roll passed\nstderr:\n%s", stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	outcomes := doc["clusters"].([]any)[0].(map[string]any)["outcomes"].(map[string]any)
	failures, _ := outcomes["rollFailures"].([]any)
	if len(failures) != 1 || failures[0].(map[string]any)["nodegroup"] != "web" {
		t.Errorf("fleet outcomes.rollFailures = %v, want one entry for web", outcomes["rollFailures"])
	}
}

func TestRollFailures(t *testing.T) {
	got := rollFailures([]refreshTypes.UpdateProgress{
		{NodegroupName: "ok", UpdateID: "u1", Status: ekstypes.UpdateStatusSuccessful},
		{NodegroupName: "bad", UpdateID: "u2", Status: ekstypes.UpdateStatusFailed, ErrorMessage: "drain failed"},
		{NodegroupName: "stopped", UpdateID: "u3", Status: ekstypes.UpdateStatusCancelled},
		{NodegroupName: "lost", UpdateID: "u4", Status: ekstypes.UpdateStatusInProgress, MonitorErr: errors.New("access denied")},
		{NodegroupName: "running", UpdateID: "u5", Status: ekstypes.UpdateStatusInProgress},
	})
	want := []rollFailure{
		{Nodegroup: "bad", UpdateID: "u2", Status: "Failed", Error: "drain failed"},
		{Nodegroup: "stopped", UpdateID: "u3", Status: "Cancelled"},
		{Nodegroup: "lost", UpdateID: "u4", Status: rollFailureUnmonitored, Error: "access denied"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rollFailures = %+v\nwant %+v", got, want)
	}
	if got := rollFailures(nil); got == nil || len(got) != 0 {
		t.Errorf("rollFailures(nil) = %#v, want an empty non-nil list", got)
	}
}
