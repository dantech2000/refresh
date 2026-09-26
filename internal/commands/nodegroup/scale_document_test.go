package nodegroup

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
)

func scaleNodegroupWorld() *fakeaws.Cluster {
	return prodCluster(&fakeaws.Nodegroup{Name: "ng-a", Version: "1.31", Desired: 3, Min: 1, Max: 5})
}

// nodegroup scale -o json|yaml prints one document: the sizes before and
// after, whether it waited, the nodegroup's status after the wait, and the
// outcome.
func TestScaleDocument(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        []string
		outcome     string
		waited      bool
		dryRun      bool
		after       map[string]any
		wantStatus  string
		wantScaling bool
	}{
		{"dry run", []string{"--desired", "2", "--dry-run"}, "Planned", false, true, map[string]any{"desired": 2, "min": 1, "max": 5}, "", false},
		{"requested", []string{"--desired", "4", "--yes"}, "Requested", false, false, map[string]any{"desired": 4, "min": 1, "max": 5}, "", true},
		{"wait", []string{"--desired", "4", "--max", "6", "--wait", "--yes"}, "Completed", true, false, map[string]any{"desired": 4, "min": 1, "max": 6}, "ACTIVE", true},
	} {
		for _, format := range []string{"json", "yaml"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				srv := fakeaws.New(t, scaleNodegroupWorld())
				args := append([]string{"scale", "prod", "ng-a", "-o", format}, tc.args...)
				stdout, stderr, err := runNodegroup(t, args...)
				if err != nil {
					t.Fatalf("scale: %v\nstderr:\n%s", err, stderr)
				}
				checkScaleDocument(t, fakeaws.RequireOneDocument(t, format, stdout).(map[string]any), tc.outcome, tc.waited, tc.dryRun, tc.after, tc.wantStatus)
				if calledPath(srv, "/update-config") != tc.wantScaling {
					t.Errorf("UpdateNodegroupConfig called = %v, want %v", !tc.wantScaling, tc.wantScaling)
				}
			})
		}
	}
}

// checkScaleDocument checks a NodegroupScale document from the scaleNodegroupWorld nodegroup.
func checkScaleDocument(t *testing.T, doc map[string]any, outcome string, waited, dryRun bool, wantAfter map[string]any, wantStatus string) {
	t.Helper()
	if doc["kind"] != "NodegroupScale" || doc["cluster"] != "prod" || doc["nodegroup"] != "ng-a" || doc["region"] != "us-east-1" {
		t.Errorf("header = %v", doc)
	}
	if doc["outcome"] != outcome || doc["waited"] != waited || doc["dryRun"] != dryRun {
		t.Errorf("outcome/waited/dryRun = %v/%v/%v, want %s/%v/%v", doc["outcome"], doc["waited"], doc["dryRun"], outcome, waited, dryRun)
	}
	before := doc["before"].(map[string]any)
	// JSON numbers decode as float64, YAML ones as int: compare text.
	if fmt.Sprint(before["desired"], before["min"], before["max"]) != "3 1 5" {
		t.Errorf("before = %v", before)
	}
	after := doc["after"].(map[string]any)
	for k, v := range wantAfter {
		if fmt.Sprint(after[k]) != fmt.Sprint(v) {
			t.Errorf("after.%s = %v, want %v", k, after[k], v)
		}
	}
	if got, _ := doc["nodegroupStatus"].(string); got != wantStatus {
		t.Errorf("nodegroupStatus = %q, want %q", got, wantStatus)
	}
	if _, ok := doc["pdbGate"]; ok {
		t.Error("pdbGate without --check-pdbs")
	}
	if fs, ok := doc["failures"].([]any); !ok || len(fs) != 0 {
		t.Errorf("failures = %v, want []", doc["failures"])
	}
}

// Machine runs never prompt: without --yes they fail before any AWS call.
func TestScaleDocument_NeedsYes(t *testing.T) {
	withScalePrompt(t, true, "y")
	srv := fakeaws.New(t, scaleNodegroupWorld())
	_, _, err := runNodegroup(t, "scale", "prod", "ng-a", "--desired", "2", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v, want one naming --yes", err)
	}
	if len(srv.Calls()) != 0 {
		t.Errorf("AWS calls before the --yes check: %v", srv.Calls())
	}
}

// With --check-pdbs --force and PDBs that can't be read, the document says
// the gate did not check, and lists the failure (exit 4).
func TestScaleDocument_PDBGateUnchecked(t *testing.T) {
	t.Setenv("KUBECONFIG", t.TempDir()+"/none")
	fakeaws.New(t, scaleNodegroupWorld())
	stdout, stderr, err := runNodegroup(t, "scale", "prod", "ng-a", "--desired", "1", "--check-pdbs", "--force", "--yes", "-o", "json")
	if code := exitCodeOf(err); code != 4 {
		t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	gate, _ := doc["pdbGate"].(map[string]any)
	if gate["result"] != "Unchecked" || doc["outcome"] != "Requested" {
		t.Errorf("pdbGate = %v, outcome = %v", gate, doc["outcome"])
	}
	if fs, _ := doc["failures"].([]any); len(fs) != 1 {
		t.Errorf("failures = %v, want one", doc["failures"])
	}
}

func TestPDBGateOf(t *testing.T) {
	blocker := health.PDBInfo{Namespace: "default", Name: "web"}
	refused := &nodegroupsvc.ScaleDownPDBCheck{CurrentDesired: 3, RequestedDesired: 1, ScaleDown: true, Blockers: []health.PDBInfo{blocker}, Scoped: true}
	for _, tc := range []struct {
		name  string
		check *nodegroupsvc.ScaleDownPDBCheck
		err   error
		force bool
		want  pdbGateResult
		n     int
	}{
		{"not a scale-down", &nodegroupsvc.ScaleDownPDBCheck{CurrentDesired: 3, RequestedDesired: 4}, nil, false, pdbGateNotScaleDown, 0},
		{"passed", &nodegroupsvc.ScaleDownPDBCheck{CurrentDesired: 3, RequestedDesired: 2, ScaleDown: true, Scoped: true}, nil, false, pdbGatePassed, 0},
		{"refused", refused, nil, false, pdbGateRefused, 1},
		{"overridden", refused, nil, true, pdbGateOverridden, 1},
		{"unchecked", nil, errors.New("no kube"), true, pdbGateUnchecked, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := pdbGateOf(tc.check, tc.err, tc.force)
			if g == nil || g.Result != tc.want || len(g.Blockers) != tc.n || g.Blockers == nil {
				t.Fatalf("gate = %+v, want %s with %d blocker(s)", g, tc.want, tc.n)
			}
		})
	}
	if pdbGateOf(nil, nil, false) != nil {
		t.Error("a gate that did not run has a verdict")
	}
}
