package health_test

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/health"
)

// The results of the table, by the words of the design note. "B" is a
// blocking check, "FC" one that fails closed, "Adv" an advisory one, and
// "Drain" the drain-blocker check, which is both and forceable.
var (
	pass      = health.Result{Name: "Nodes", Outcome: health.Pass, Blocking: true, FailClosed: true}
	skipped   = health.Result{Name: "Utilization", Outcome: health.Skipped, Message: "no metrics client"}
	warn      = health.Result{Name: "Quotas", Outcome: health.Warn, Message: "91% of the vCPU quota in use"}
	kubeWarn  = health.Result{Name: health.AccessCheck, Outcome: health.Warn, Message: "no Kubernetes access"}
	advFail   = health.Result{Name: "Utilization", Outcome: health.Fail, Message: "low headroom"}
	advUnread = health.Result{Name: "Quotas", Outcome: health.Unreadable, Message: "could not read the quota", Failures: []apidoc.Failure{{Kind: apidoc.FailureCluster, Name: "prod"}}}
	capUnread = health.Result{Name: "Capacity", Outcome: health.Unreadable, Blocking: true, Message: "could not read the CPU metrics", Failures: []apidoc.Failure{{Kind: apidoc.FailureCluster, Name: "prod"}}}
	bFail     = health.Result{Name: "Nodes", Outcome: health.Fail, Blocking: true, FailClosed: true, Message: "2/6 nodes ready"}
	fcUnread  = health.Result{Name: "Workloads", Outcome: health.Unreadable, Blocking: true, FailClosed: true, Message: "could not list pods", Failures: []apidoc.Failure{{Kind: apidoc.FailureCluster, Name: "prod"}}}
	drainFail = health.Result{Name: "DrainBlockers", Outcome: health.Fail, Blocking: true, FailClosed: true, Forceable: true, Message: "PDB default/web allows 0 disruptions"}
	drainUnr  = health.Result{Name: "DrainBlockers", Outcome: health.Unreadable, Blocking: true, FailClosed: true, Forceable: true, Message: "could not list PDBs", Failures: []apidoc.Failure{{Kind: apidoc.FailureCluster, Name: "prod"}}}
)

var (
	none           = health.Policy{}
	yes            = health.Policy{Yes: true}
	requireHealthy = health.Policy{RequireHealthy: true}
	force          = health.Policy{Force: true}
)

func reasons(results ...health.Result) []health.Reason {
	out := make([]health.Reason, 0, len(results))
	for _, r := range results {
		out = append(out, health.Reason{Check: r.Name, Outcome: r.Outcome, Message: r.Message})
	}
	return out
}

// TestGate is the table of section 4 of the design note, row for row.
func TestGate(t *testing.T) {
	type want struct {
		decision health.Decision
		exit     int
		accepted []health.Reason
		forced   []health.Reason
		failures int
	}
	tests := []struct {
		row     int
		results []health.Result
		policy  health.Policy
		want    want
	}{
		{1, []health.Result{pass, skipped}, none, want{health.Go, 0, nil, nil, 0}},
		{1, []health.Result{pass, skipped}, requireHealthy, want{health.Go, 0, nil, nil, 0}},
		{2, []health.Result{bFail}, health.Policy{Skip: true}, want{health.Go, 0, nil, nil, 0}},
		{3, []health.Result{pass, warn}, none, want{health.Ask, 2, nil, nil, 0}},
		{4, []health.Result{pass, warn}, yes, want{health.Go, 2, reasons(warn), nil, 0}},
		{5, []health.Result{pass, warn}, requireHealthy, want{health.Block, 2, nil, nil, 0}},
		{6, []health.Result{pass, warn}, health.Policy{Yes: true, RequireHealthy: true}, want{health.Block, 2, nil, nil, 0}},
		{7, []health.Result{advFail}, none, want{health.Ask, 2, nil, nil, 0}},
		{8, []health.Result{advFail}, yes, want{health.Go, 2, reasons(advFail), nil, 0}},
		{9, []health.Result{advFail}, requireHealthy, want{health.Block, 2, nil, nil, 0}},
		{10, []health.Result{advUnread}, none, want{health.Ask, 4, nil, nil, 1}},
		{11, []health.Result{advUnread}, yes, want{health.Go, 4, reasons(advUnread), nil, 1}},
		{12, []health.Result{advUnread}, requireHealthy, want{health.Block, 4, nil, nil, 1}},
		{13, []health.Result{capUnread}, none, want{health.Ask, 4, nil, nil, 1}},
		{14, []health.Result{capUnread}, yes, want{health.Go, 4, reasons(capUnread), nil, 1}},
		{15, []health.Result{capUnread}, requireHealthy, want{health.Block, 4, nil, nil, 1}},
		{16, []health.Result{pass, kubeWarn}, none, want{health.Ask, 2, nil, nil, 0}},
		{17, []health.Result{pass, kubeWarn}, yes, want{health.Go, 2, reasons(kubeWarn), nil, 0}},
		{18, []health.Result{pass, kubeWarn}, requireHealthy, want{health.Block, 2, nil, nil, 0}},
		{19, []health.Result{bFail, warn}, none, want{health.Block, 3, nil, nil, 0}},
		{19, []health.Result{bFail, warn}, yes, want{health.Block, 3, nil, nil, 0}},
		{19, []health.Result{bFail}, force, want{health.Block, 3, nil, nil, 0}},
		{20, []health.Result{bFail, advUnread}, yes, want{health.Block, 4, nil, nil, 1}},
		{21, []health.Result{fcUnread}, yes, want{health.Block, 4, nil, nil, 1}},
		{21, []health.Result{fcUnread}, force, want{health.Block, 4, nil, nil, 1}},
		{22, []health.Result{drainFail}, none, want{health.Block, 3, nil, nil, 0}},
		{22, []health.Result{drainFail}, yes, want{health.Block, 3, nil, nil, 0}},
		{23, []health.Result{drainFail}, force, want{health.Ask, 2, nil, reasons(drainFail), 0}},
		{24, []health.Result{drainFail}, health.Policy{Force: true, Yes: true}, want{health.Go, 2, reasons(drainFail), reasons(drainFail), 0}},
		{25, []health.Result{drainFail}, health.Policy{Force: true, RequireHealthy: true}, want{health.Block, 2, nil, reasons(drainFail), 0}},
		{26, []health.Result{drainUnr}, none, want{health.Block, 4, nil, nil, 1}},
		{26, []health.Result{drainUnr}, yes, want{health.Block, 4, nil, nil, 1}},
		{27, []health.Result{drainUnr}, force, want{health.Ask, 4, nil, reasons(drainUnr), 1}},
		{28, []health.Result{drainUnr}, health.Policy{Force: true, Yes: true}, want{health.Go, 4, reasons(drainUnr), reasons(drainUnr), 1}},
		{29, []health.Result{drainUnr}, health.Policy{Force: true, RequireHealthy: true}, want{health.Block, 4, nil, reasons(drainUnr), 1}},
		{30, []health.Result{drainFail, bFail}, force, want{health.Block, 3, nil, reasons(drainFail), 0}},
		{31, []health.Result{warn, capUnread}, yes, want{health.Go, 4, reasons(warn, capUnread), nil, 1}},
		{32, []health.Result{pass, warn}, force, want{health.Ask, 2, nil, nil, 0}},
		{32, []health.Result{capUnread}, force, want{health.Ask, 4, nil, nil, 1}},
	}
	for _, tt := range tests {
		v := health.Gate(tt.results, tt.policy)
		got := want{v.Decision, v.Exit, v.Accepted, v.Forced, len(v.Failures)}
		if diff := cmp.Diff(tt.want, got, cmp.AllowUnexported(want{})); diff != "" {
			t.Errorf("row %d, policy %+v (-want +got):\n%s", tt.row, tt.policy, diff)
		}
	}
}

func TestGate_ReasonsWorstFirst(t *testing.T) {
	v := health.Gate([]health.Result{warn, advUnread, bFail, pass, skipped}, health.Policy{})
	want := reasons(bFail, warn, advUnread)
	if diff := cmp.Diff(want, v.Reasons); diff != "" {
		t.Errorf("reasons (-want +got):\n%s", diff)
	}
	if got, want := v.Summary(), "blocked by 1 health check: Nodes: 2/6 nodes ready"; got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
	ask := health.Gate([]health.Result{warn, kubeWarn}, health.Policy{})
	if got, want := ask.Question(), "proceed despite 2 warnings (Quotas: 91% of the vCPU quota in use; Kubernetes: no Kubernetes access)?"; got != want {
		t.Errorf("Question() = %q, want %q", got, want)
	}
	stop := health.Gate([]health.Result{warn}, requireHealthy)
	if got, want := stop.Summary(), "health checks reported 1 warning and --require-healthy is set: Quotas: 91% of the vCPU quota in use"; got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

func TestWorse(t *testing.T) {
	result := func(name string, o health.Outcome, findings ...string) health.Result {
		return health.Result{Name: name, Outcome: o, Message: string(o), Findings: findings}
	}
	tests := []struct {
		name string
		prev []health.Result
		cur  []health.Result
		want []health.Reason
	}{
		{"same", []health.Result{result("Nodes", health.Warn, "a")}, []health.Result{result("Nodes", health.Warn, "a")}, nil},
		{"worse outcome", []health.Result{result("Nodes", health.Warn)}, []health.Result{result("Nodes", health.Fail)}, reasons(result("Nodes", health.Fail))},
		{"better outcome", []health.Result{result("Nodes", health.Fail)}, []health.Result{result("Nodes", health.Warn)}, nil},
		{"fail to unreadable", []health.Result{result("DrainBlockers", health.Fail, "default/web")}, []health.Result{result("DrainBlockers", health.Unreadable)}, reasons(result("DrainBlockers", health.Unreadable))},
		{"warn to unreadable", []health.Result{result("Quotas", health.Warn)}, []health.Result{result("Quotas", health.Unreadable)}, reasons(result("Quotas", health.Unreadable))},
		{"unreadable to fail", []health.Result{result("DrainBlockers", health.Unreadable)}, []health.Result{result("DrainBlockers", health.Fail, "default/web")}, nil},
		{"new finding", []health.Result{result("Nodes", health.Warn, "a")}, []health.Result{result("Nodes", health.Warn, "a", "b")}, reasons(result("Nodes", health.Warn))},
		{"fewer findings", []health.Result{result("Nodes", health.Warn, "a", "b")}, []health.Result{result("Nodes", health.Warn, "a")}, nil},
		{"a number within the same outcome", []health.Result{result("Capacity", health.Warn)}, []health.Result{result("Capacity", health.Warn)}, nil},
		{"skipped now", []health.Result{result("Workloads", health.Pass)}, []health.Result{{Name: "Workloads", Outcome: health.Skipped, Message: "no Kubernetes access"}},
			[]health.Reason{{Check: "Workloads", Outcome: health.Skipped, Message: "did not run this time: no Kubernetes access"}}},
		{"skipped both times", []health.Result{result("Workloads", health.Skipped)}, []health.Result{result("Workloads", health.Skipped)}, nil},
		{"new check", nil, []health.Result{result("Nodes", health.Fail)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, health.Worse(tt.prev, tt.cur)); diff != "" {
				t.Errorf("Worse (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDoc_Summary(t *testing.T) {
	tests := []struct {
		name    string
		results []health.Result
		want    apidoc.HealthOutcome
	}{
		{"all pass", []health.Result{pass}, apidoc.HealthPass},
		{"only skipped", []health.Result{skipped}, apidoc.HealthSkipped},
		{"nothing", nil, apidoc.HealthSkipped},
		{"skipped and pass", []health.Result{skipped, pass}, apidoc.HealthPass},
		{"warn", []health.Result{pass, warn, skipped}, apidoc.HealthWarn},
		{"unreadable over warn", []health.Result{warn, advUnread}, apidoc.HealthUnreadable},
		{"fail over unreadable", []health.Result{advUnread, bFail}, apidoc.HealthFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := health.Doc(tt.results)
			if doc.Summary != tt.want {
				t.Errorf("Summary = %s, want %s", doc.Summary, tt.want)
			}
			for _, c := range doc.Checks {
				if c.Details == nil || c.Findings == nil {
					t.Errorf("%s: a list is nil; the document would print null", c.Name)
				}
			}
		})
	}
}

func TestGateDoc(t *testing.T) {
	v := health.Gate([]health.Result{drainFail, warn, pass}, health.Policy{Force: true, Yes: true})
	doc := health.GateDoc(v, []health.Result{drainFail, warn, pass})
	if doc.Decision != apidoc.GateGo || len(doc.Checks) != 3 {
		t.Errorf("doc = %+v", doc)
	}
	want := []string{"DrainBlockers: PDB default/web allows 0 disruptions"}
	if diff := cmp.Diff(want, doc.Forced); diff != "" {
		t.Errorf("forced (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"DrainBlockers: PDB default/web allows 0 disruptions", "Quotas: 91% of the vCPU quota in use"}, doc.Accepted); diff != "" {
		t.Errorf("accepted (-want +got):\n%s", diff)
	}
	for _, d := range []health.Decision{health.Go, health.Ask, health.Block} {
		if !slices.Contains(apidoc.GateDecision("").Values(), string(d)) {
			t.Errorf("decision %s is not a value of the document enum", d)
		}
	}
	empty := health.GateDoc(health.Gate(nil, health.Policy{}), nil)
	if empty.Reasons == nil || empty.Accepted == nil || empty.Forced == nil || empty.Checks == nil {
		t.Error("a list is nil; the document would print null")
	}
}
