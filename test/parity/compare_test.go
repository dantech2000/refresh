//go:build unix

package parity

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

const allowlistPath = "testdata/allowlist.json"

// step adds one thing to a trace under construction.
type step func(t *Trace)

// seq returns the next sequence number of the trace.
func seq(t *Trace) int {
	n := 0
	for _, r := range t.Requests {
		n = max(n, r.Arrived, r.Completed)
	}
	for _, c := range t.Confirmations {
		n = max(n, c)
	}
	return n + 1
}

func fact(subject, attribute, value string) Fact {
	return Fact{Subject: subject, Attribute: attribute, Value: value}
}

// read is a read that arrives and is answered before anything else happens.
func read(op fleettest.Operation, target string, facts ...Fact) step {
	return func(t *Trace) {
		n := seq(t)
		t.Requests = append(t.Requests, Record{Operation: op, Region: Region, Target: target, Arrived: n, Completed: n + 1, Facts: facts})
	}
}

// change is a mutating request that EKS accepts.
func change(op fleettest.Operation, target, token, outcome string) step {
	return func(t *Trace) {
		n := seq(t)
		t.Requests = append(t.Requests, Record{Operation: op, Region: Region, Target: target, Arrived: n, Completed: n + 1, Mutation: &Mutation{
			Operation: op, Region: Region, Target: target, Outcome: outcome,
			Payload: map[string]any{"version": "1.33", "clientRequestToken": token},
		}})
	}
}

// confirm is the user's answer to a prompt.
func confirm(t *Trace) { t.Confirmations = append(t.Confirmations, seq(t)) }

func build(steps ...step) Trace {
	var t Trace
	for _, s := range steps {
		s(&t)
	}
	t.derive()
	return t
}

// The reads of the sample.
var (
	clusterOld   = read("eks:DescribeCluster", "prod", fact("cluster prod", "status", "ACTIVE"), fact("cluster prod", "version", "1.32"))
	clusterNew   = read("eks:DescribeCluster", "prod", fact("cluster prod", "status", "ACTIVE"), fact("cluster prod", "version", "1.33"))
	budgets      = read("kube:ListPodDisruptionBudgets", "prod", fact("pdbs prod", "read", "ok"), fact("pdb prod/default/web", "allowed", "1"))
	nodegroup    = read("eks:DescribeNodegroup", "prod/ng-a", fact("nodegroup prod/ng-a", "status", "ACTIVE"), fact("nodegroup prod/ng-a", "version", "1.32"))
	nodegroupNew = read("eks:DescribeNodegroup", "prod/ng-a", fact("nodegroup prod/ng-a", "status", "ACTIVE"), fact("nodegroup prod/ng-a", "version", "1.33"))
	neighbour    = read("eks:DescribeNodegroup", "prod/ng-b", fact("nodegroup prod/ng-b", "status", "ACTIVE"))
	pendingPods  = read("kube:ListPods", "prod", fact("pods prod", "read", "ok"), fact("pods prod", "pending", "none"))
	nodesReady   = read("kube:ListNodes", "prod", fact("nodes prod", "read", "ok"), fact("node prod/a-1", "condition", "Ready"))
)

func updateIs(target, n, status string) step {
	return read("eks:DescribeUpdate", target, fact("update "+target+" "+n, "status", status))
}

// sampleSteps is a small upgrade: the control plane, then one nodegroup,
// then the verification. The comments number the steps for the tests.
func sampleSteps() []step {
	return []step{
		clusterOld, // 0
		nodegroup,  // 1
		change("eks:UpdateClusterVersion", "prod", "token-1", "accepted update "+updateID(1)), // 2
		updateIs("prod", "1", "InProgress"),                                                   // 3
		updateIs("prod", "1", "Successful"),                                                   // 4
		clusterNew,                                                                            // 5
		nodegroup,                                                                             // 6
		neighbour,                                                                             // 7
		budgets,                                                                               // 8
		change("eks:UpdateNodegroupVersion", "prod/ng-a", "token-2", "accepted update "+updateID(2)), // 9
		updateIs("prod/ng-a", "2", "InProgress"),                                                     // 10
		updateIs("prod/ng-a", "2", "Successful"),                                                     // 11
		nodegroupNew,                                                                                 // 12
		pendingPods,                                                                                  // 13
		nodesReady,                                                                                   // 14
	}
}

func sampleTrace() Trace {
	t := build(sampleSteps()...)
	t.Stdout = "old words"
	t.World = map[string]ClusterState{Region + "/prod": {Cluster: "1.33 ACTIVE", Nodegroups: map[string]string{"ng-a": "1.33 r2 ACTIVE 2/1/3"}}}
	return t
}

// without returns the steps without those at the indexes.
func without(steps []step, drop ...int) []step {
	var out []step
	for i, s := range steps {
		if !slices.Contains(drop, i) {
			out = append(out, s)
		}
	}
	return out
}

// moved returns the steps with the one at from moved to just before the one
// at to.
func moved(steps []step, from, to int) []step {
	out := slices.Clone(steps)
	s := out[from]
	out = slices.Delete(out, from, from+1)
	if from < to {
		to--
	}
	return slices.Insert(out, to, s)
}

// TestDerive shows what a trace keeps of its requests.
func TestDerive(t *testing.T) {
	t.Parallel()
	got := sampleTrace()
	lines := func(list []Evidence) []string {
		var out []string
		for _, e := range list {
			out = append(out, e.String())
		}
		return out
	}
	want := [][]string{
		{
			"target: cluster prod: status=ACTIVE", "target: cluster prod: version=1.32",
			"gate: nodegroup prod/ng-a: status=ACTIVE", "gate: nodegroup prod/ng-a: version=1.32",
		},
		{
			"prerequisite: cluster prod: status=ACTIVE", "prerequisite: cluster prod: version=1.33",
			"target: nodegroup prod/ng-a: status=ACTIVE", "target: nodegroup prod/ng-a: version=1.32",
			"gate: nodegroup prod/ng-b: status=ACTIVE", "gate: pdb prod/default/web: allowed=1",
			"prerequisite: update prod 1: status=Successful",
		},
		{"settlement: nodegroup prod/ng-a: status=ACTIVE", "settlement: nodegroup prod/ng-a: version=1.33", "settlement: update prod/ng-a 2: status=Successful"},
		{"verification: node prod/a-1: condition=Ready", "verification: pods prod: pending=none"},
	}
	have := [][]string{lines(got.Changes[0].Evidence), lines(got.Changes[1].Evidence), lines(got.Settlement), lines(got.Verification)}
	if diff := cmp.Diff(want, have); diff != "" {
		t.Errorf("evidence of the two changes, settlement, and verification (-want +got):\n%s", diff)
	}
	if got.Decision != nil {
		t.Errorf("a run with changes has decision evidence: %v", got.Decision)
	}

	// The evidence reads the same after a golden file.
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var back Trace
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(got, back, outputOnly); diff != "" {
		t.Errorf("the trace after JSON (-before +after):\n%s", diff)
	}

	// A prompt splits the window of the change after it. A read that failed
	// and then worked leaves no error, and one that worked and then failed
	// does.
	denied := read("kube:ListPods", "prod", fact("pods prod", "read", "error=Forbidden"))
	asked := build(nodegroup, denied, pendingPods, confirm, budgets, pendingPods, denied, change("eks:UpdateNodegroupVersion", "prod/ng-a", "token-1", "accepted")) //nolint:gocritic // the same read twice, on purpose
	wantAsked := []string{
		"before the prompt, target: nodegroup prod/ng-a: status=ACTIVE", "before the prompt, target: nodegroup prod/ng-a: version=1.32",
		"before the prompt, gate: pods prod: pending=none",
		"gate: pdb prod/default/web: allowed=1", "gate: pods prod: pending=none", "gate: pods prod: read=error=Forbidden",
	}
	if diff := cmp.Diff(wantAsked, lines(asked.Changes[0].Evidence)); diff != "" {
		t.Errorf("evidence of a change after a prompt (-want +got):\n%s", diff)
	}

	// A run that changes nothing has decision evidence, and of every cluster.
	refused := build(nodegroup, read("eks:DescribeCluster", "staging", fact("cluster staging", "status", "UPDATING")))
	if diff := cmp.Diff([]string{"decision: cluster staging: status=UPDATING", "decision: nodegroup prod/ng-a: status=ACTIVE", "decision: nodegroup prod/ng-a: version=1.32"}, lines(refused.Decision)); diff != "" {
		t.Errorf("decision evidence (-want +got):\n%s", diff)
	}
}

// TestCompareFindsDifferences alters one thing in a copy of a trace, as a
// wrong v2 binary would, and checks that Compare reports that field and no
// other, and says in words which obligation is not met.
func TestCompareFindsDifferences(t *testing.T) {
	t.Parallel()
	steps := sampleSteps
	edit := func(f func(v2 *Trace)) func() Trace {
		return func() Trace {
			v2 := sampleTrace()
			f(&v2)
			return v2
		}
	}
	rebuilt := func(s []step) func() Trace {
		return func() Trace {
			v2 := build(s...)
			v2.World = sampleTrace().World
			return v2
		}
	}
	// held makes the read at index i arrive where it is and get its answer
	// only after everything else: the binary did not wait for it.
	held := func(i int) func() Trace {
		return func() Trace {
			v2 := sampleTrace()
			reads := 0
			for j := range v2.Requests {
				if reads == i {
					v2.Requests[j].Completed = seq(&v2)
					break
				}
				reads++
			}
			v2.derive()
			return v2
		}
	}
	tests := []struct {
		name string
		v2   func() Trace
		want []Field
		says string
	}{
		{name: "nothing", v2: sampleTrace},
		{name: "other words on stdout and stderr", v2: edit(func(v2 *Trace) { v2.Stdout, v2.Stderr = "new words", "new words" })},

		// Harmless: the new binary reads more, or otherwise, and knows the same.
		{name: "a read more of something else", v2: rebuilt(slices.Insert(steps(), 8, read("eks:DescribeAddon", "prod/coredns", fact("addon prod/coredns", "status", "ACTIVE"))))},
		{name: "the same read twice", v2: rebuilt(slices.Insert(steps(), 8, nodegroup, nodegroup))}, //nolint:gocritic // the same read twice, on purpose
		{name: "two reads in one answer", v2: rebuilt(slices.Insert(without(steps(), 6, 7), 6, read("eks:ListNodegroups", "prod",
			fact("nodegroup prod/ng-a", "status", "ACTIVE"), fact("nodegroup prod/ng-a", "version", "1.32"), fact("nodegroup prod/ng-b", "status", "ACTIVE"))))},
		{name: "a gate read earlier in the same window", v2: rebuilt(moved(steps(), 8, 5))},
		{name: "the verification in another order", v2: rebuilt(moved(steps(), 14, 12))},
		{name: "an error that a later read replaced", v2: rebuilt(slices.Insert(steps(), 6, read("eks:DescribeNodegroup", "prod/ng-a", fact("nodegroup prod/ng-a", "read", "error=ThrottlingException"))))},

		// One per kind of obligation.
		{name: "the nodegroup was not read before its roll", v2: rebuilt(without(steps(), 6)), want: []Field{FieldEvidence},
			says: "target obligation not met: before eks:UpdateNodegroupVersion prod/ng-a (change 2), the old binary had seen nodegroup prod/ng-a status=ACTIVE, and the new binary did not read it there"},
		{name: "the roll was sent while the read of the nodegroup was unanswered", v2: held(6), want: []Field{FieldEvidence},
			says: "target obligation not met"},
		{name: "the roll started before the control plane was seen to finish", v2: rebuilt(without(steps(), 4)), want: []Field{FieldEvidence},
			says: "prerequisite obligation not met: before eks:UpdateNodegroupVersion prod/ng-a (change 2), the old binary had seen update prod 1 status=Successful, and the new binary last saw InProgress"},
		{name: "the budgets were read before the control plane changed, and not again", v2: rebuilt(moved(steps(), 8, 2)), want: []Field{FieldEvidence},
			says: "gate obligation not met: before eks:UpdateNodegroupVersion prod/ng-a (change 2), the old binary had seen pdb prod/default/web allowed=1"},
		{name: "the gate saw another value", v2: rebuilt(slices.Insert(without(steps(), 8), 8, read("kube:ListPodDisruptionBudgets", "prod", fact("pdb prod/default/web", "allowed", "0")))),
			want: []Field{FieldEvidence}, says: "and the new binary last saw 0"},
		{name: "the gate could not read", v2: rebuilt(slices.Insert(steps(), 9, read("eks:DescribeNodegroup", "prod/ng-b", fact("nodegroup prod/ng-b", "read", "error=AccessDeniedException")))),
			want: nil}, // the old binary has no such obligation; the checks of a scenario catch a binary that goes on
		{name: "the last update was not seen to finish", v2: rebuilt(without(steps(), 11, 13, 14)), want: []Field{FieldSettlement, FieldVerification},
			says: "settlement obligation not met: after its last change, the old binary had seen update prod/ng-a 2 status=Successful, and the new binary last saw InProgress"},
		{name: "no verification after the last change", v2: rebuilt(without(steps(), 13, 14)), want: []Field{FieldVerification},
			says: "verification obligation not met: after its last update ended, the old binary had seen node prod/a-1 condition=Ready, and the new binary did not read it there"},
		{name: "the pods were checked before the roll had ended", v2: rebuilt(moved(steps(), 13, 11)), want: []Field{FieldVerification},
			says: "the old binary had seen pods prod pending=none"},

		// Compared for equality.
		{name: "another target version", v2: edit(func(v2 *Trace) { v2.Changes[1].Mutation.Payload["version"] = "1.34" }), want: []Field{FieldMutations}},
		{name: "a field more in the payload", v2: edit(func(v2 *Trace) { v2.Changes[1].Mutation.Payload["force"] = true }), want: []Field{FieldMutations}},
		{name: "one token for two requests", v2: edit(func(v2 *Trace) { v2.Changes[1].Mutation.Payload["clientRequestToken"] = "token-1" }), want: []Field{FieldMutations}},
		{name: "a mutation that was refused", v2: edit(func(v2 *Trace) { v2.Changes[1].Mutation.Outcome = "error=ResourceInUseException" }), want: []Field{FieldMutations}},
		{name: "the target of the second change was read before the first change only", v2: rebuilt(moved(steps(), 6, 2)), want: []Field{FieldEvidence},
			says: "the old binary had seen nodegroup prod/ng-a status=ACTIVE, and the new binary did not read it there"},
		{name: "a mutation more", v2: rebuilt(append(steps(), change("eks:UpdateAddon", "prod/coredns", "token-3", "accepted"))), want: []Field{FieldMutations, FieldSettlement, FieldVerification}},
		{name: "the nodegroup rolled before the control plane", v2: rebuilt(moved(steps(), 9, 2)), want: []Field{FieldMutations}},
		{name: "another final state", v2: edit(func(v2 *Trace) {
			v2.World[Region+"/prod"].Nodegroups["ng-a"] = "1.33 r2 DEGRADED 2/1/3 issues=NodeCreationFailure"
		}), want: []Field{FieldWorld}},
		{name: "another cluster touched", v2: edit(func(v2 *Trace) { v2.World[Region+"/staging"] = ClusterState{Cluster: "1.32 ACTIVE"} }), want: []Field{FieldWorld}},
		{name: "a refusal", v2: func() Trace {
			v2 := build(clusterOld, nodegroup)
			v2.RefusedBeforeMutation, v2.ExitCode = true, 3
			v2.World = map[string]ClusterState{Region + "/prod": {Cluster: "1.32 ACTIVE"}}
			return v2
		}, want: []Field{FieldMutations, FieldWorld, FieldRefused, FieldExitCode}},
		{name: "another exit code", v2: edit(func(v2 *Trace) { v2.ExitCode = 5 }), want: []Field{FieldExitCode}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []Field
			var text []string
			for _, d := range Compare("sample", sampleTrace(), tt.v2(), nil) {
				if d.Allowed {
					t.Errorf("%s is allowed with an empty allowlist", d.Field)
				}
				got = append(got, d.Field)
				text = append(text, d.String())
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("fields that differ (-want +got):\n%s\n%s", diff, strings.Join(text, "\n"))
			}
			if !strings.Contains(strings.Join(text, "\n"), tt.says) {
				t.Errorf("the differences do not say %q:\n%s", tt.says, strings.Join(text, "\n"))
			}
		})
	}
}

// TestCompareDecision holds a run that changes nothing to what the old
// binary saw when it decided that.
func TestCompareDecision(t *testing.T) {
	t.Parallel()
	done := updateIs("prod", "1", "Successful")
	old := build(clusterOld, updateIs("prod", "1", "InProgress"), done)
	tests := []struct {
		name string
		v2   Trace
		says string
	}{
		{"the same, in another order", build(done, clusterOld), ""},
		{"it did not wait for the update it attached to", build(clusterOld, updateIs("prod", "1", "InProgress")),
			"decision obligation not met: in the run, the old binary had seen update prod 1 status=Successful, and the new binary last saw InProgress"},
		{"it did not look at the update", build(clusterOld), "the new binary did not read it there"},
	}
	for _, tt := range tests {
		diffs := Compare("sample", old, tt.v2, nil)
		switch {
		case tt.says == "" && len(diffs) != 0:
			t.Errorf("%s: differences %v, want none", tt.name, diffs)
		case tt.says != "" && (len(diffs) != 1 || diffs[0].Field != FieldDecision || !strings.Contains(diffs[0].Detail, tt.says)):
			t.Errorf("%s: differences %v, want one in the decision that says %q", tt.name, diffs, tt.says)
		}
	}
}

// TestCompareBatch is a binary that reads two nodegroups, and then rolls
// one after the other. It owes no second read of the second nodegroup, and
// a new binary that makes one meets the obligation too. One that never read
// the second nodegroup, or that read it only before the user said yes, does
// not.
func TestCompareBatch(t *testing.T) {
	t.Parallel()
	other := read("eks:DescribeNodegroup", "prod/ng-b", fact("nodegroup prod/ng-b", "status", "ACTIVE"))
	degraded := read("eks:DescribeNodegroup", "prod/ng-b", fact("nodegroup prod/ng-b", "status", "DEGRADED"))
	a := change("eks:UpdateNodegroupVersion", "prod/ng-a", "token-1", "accepted update "+updateID(1))
	b := change("eks:UpdateNodegroupVersion", "prod/ng-b", "token-2", "accepted update "+updateID(2))
	done := updateIs("prod/ng-a", "1", "Successful")
	old := build(nodegroup, other, a, done, b)
	if got := old.Changes[1].Evidence[len(old.Changes[1].Evidence)-1].String(); got != "carried, target: nodegroup prod/ng-b: status=ACTIVE" {
		t.Errorf("the last evidence of the second roll is %q, want the carried status of ng-b", got)
	}
	tests := []struct {
		name string
		v2   Trace
		says string
	}{
		{"the same", build(other, nodegroup, a, done, b), ""},
		{"it reads the second target again", build(nodegroup, other, a, done, other, b), ""}, //nolint:gocritic // the same read twice, on purpose
		{"it reads the second target only between the changes", build(nodegroup, a, done, other, b), "gate obligation not met: before eks:UpdateNodegroupVersion prod/ng-a (change 1)"},
		{"it never read the second target", build(nodegroup, a, done, b), "from before its previous change, the old binary had seen nodegroup prod/ng-b status=ACTIVE, and the new binary did not read it there"},
		{"it read the second target again and it had degraded", build(nodegroup, other, a, done, degraded, b), "and the new binary last saw DEGRADED"},
		{"it read the second target only before the prompt", build(nodegroup, other, confirm, nodegroup, a, done, b), "did not read it there"}, //nolint:gocritic // the same read twice, on purpose
	}
	for _, tt := range tests {
		var text []string
		for _, d := range Compare("sample", old, tt.v2, nil) {
			text = append(text, d.String())
		}
		switch all := strings.Join(text, "\n"); {
		case tt.says == "" && all != "":
			t.Errorf("%s: want no difference:\n%s", tt.name, all)
		case tt.says != "" && (len(text) != 1 || !strings.Contains(all, "evidence differs") || !strings.Contains(all, tt.says)):
			t.Errorf("%s: want one difference in the evidence that says %q:\n%s", tt.name, tt.says, all)
		}
	}

	// A fresh read is owed fresh: the old binary read the second target
	// again, so a new binary that holds only the read from the start does
	// not meet it.
	careful := build(nodegroup, other, a, done, other, b) //nolint:gocritic // the same read twice, on purpose
	if diffs := Compare("sample", careful, old, nil); len(diffs) != 1 || !strings.Contains(diffs[0].Detail, "the new binary did not read it there") {
		t.Errorf("a batch binary held to one that reads again: %v, want one unmet obligation", diffs)
	}
}

// TestCompareBaselineRule holds the baseline rule to its words. The old
// binary reads the baseline before its drain gate, so a run that the gate
// refuses has read it. A new binary that is refused too owes no baseline.
// It owes every gate read, and it owes the same list where that list is
// not a baseline: in a run that was not refused, and after an update that
// the run followed.
func TestCompareBaselineRule(t *testing.T) {
	t.Parallel()
	tight := read("kube:ListPodDisruptionBudgets", "prod", fact("pdbs prod", "read", "ok"), fact("pdb prod/default/web", "allowed", "0"))
	refused := func(steps ...step) Trace {
		tr := build(steps...)
		tr.RefusedBeforeMutation, tr.ExitCode = true, 3
		return tr
	}
	old := refused(nodegroup, pendingPods, nodesReady, tight)
	const baseline = "the old binary had seen pods prod pending=none"
	tests := []struct {
		name    string
		old, v2 Trace
		// says is a text of the one difference, in the decision. Empty
		// means no difference.
		says string
	}{
		{name: "refused with no baseline", old: old, v2: refused(nodegroup, nodesReady, tight)},
		{name: "refused with the baseline", old: old, v2: refused(nodegroup, pendingPods, nodesReady, tight)},
		{name: "refused, and the budgets were not read", old: old, v2: refused(nodegroup, nodesReady),
			says: "the old binary had seen pdb prod/default/web allowed=0, and the new binary did not read it there"},
		{name: "refused, and the nodes were not read", old: old, v2: refused(nodegroup, tight),
			says: "the old binary had seen node prod/a-1 condition=Ready, and the new binary did not read it there"},
		{name: "a run that was not refused owes the list", old: build(nodegroup, pendingPods), v2: build(nodegroup), says: baseline},
		{name: "after an update that the run followed, the list is the verification",
			old: refused(nodegroup, updateIs("prod/ng-a", "1", "Failed"), pendingPods),
			v2:  refused(nodegroup, updateIs("prod/ng-a", "1", "Failed")), says: baseline},
	}
	for _, tt := range tests {
		var text []string
		for _, d := range Compare("sample", tt.old, tt.v2, nil) {
			text = append(text, d.String())
		}
		switch all := strings.Join(text, "\n"); {
		case tt.says == "" && all != "":
			t.Errorf("%s: want no difference:\n%s", tt.name, all)
		case tt.says != "" && (len(text) != 1 || !strings.Contains(all, "decision differs") || !strings.Contains(all, tt.says)):
			t.Errorf("%s: want one difference in the decision that says %q:\n%s", tt.name, tt.says, all)
		}
	}
	// With a mutation in both runs the baseline is evidence of the change,
	// and the rule does not reach it.
	roll := change("eks:UpdateNodegroupVersion", "prod/ng-a", "token-1", "accepted")
	diffs := Compare("sample", build(nodegroup, pendingPods, roll), build(nodegroup, roll), nil)
	if len(diffs) != 1 || diffs[0].Field != FieldEvidence || !strings.Contains(diffs[0].Detail, baseline) {
		t.Errorf("a roll with no baseline before it: %v, want one unmet gate obligation for the Pending pods", diffs)
	}
}

func TestCompareExitCodeTable(t *testing.T) {
	t.Parallel()
	legacyTrace, v2 := sampleTrace(), sampleTrace()
	legacyTrace.ExitCode, v2.ExitCode = 7, 7
	diffs := Compare("sample", legacyTrace, v2, nil)
	if len(diffs) != 1 || diffs[0].Field != FieldExitCode {
		t.Errorf("a legacy exit code that is not in ExitCodes gave %v, want one exit code difference", diffs)
	}
	for code := range 6 {
		if ExitCodes[code] != code {
			t.Errorf("ExitCodes[%d] = %d: the contract gives the six codes the same meaning in both binaries", code, ExitCodes[code])
		}
	}
}

func TestCompareAllowlist(t *testing.T) {
	t.Parallel()
	v2 := sampleTrace()
	v2.ExitCode = 4
	v2.Changes[1].Mutation.Payload["version"] = "1.34"
	allow := []Allowance{
		{Scenario: "sample", Field: FieldExitCode, Reason: "the contract picks 4", Expect: json.RawMessage(`4`)},
		{Scenario: "other", Field: FieldMutations, Reason: "not this scenario", Expect: json.RawMessage(`[]`)},
	}
	diffs := Compare("sample", sampleTrace(), v2, allow)
	if len(diffs) != 2 {
		t.Fatalf("got %d differences, want 2: %v", len(diffs), diffs)
	}
	left := Unallowed(diffs)
	if len(left) != 1 || left[0].Field != FieldMutations {
		t.Errorf("differences that are not allowed = %v, want the mutations only", left)
	}
	if unused := Unused("sample", diffs, append(allow, Allowance{Scenario: "sample", Field: FieldWorld})); len(unused) != 1 || unused[0].Field != FieldWorld {
		t.Errorf("unused allowances = %+v, want the one for the world", unused)
	}
	if text := diffs[1].String(); !strings.Contains(text, "allowed: the contract picks 4") {
		t.Errorf("the allowed difference reads %q", text)
	}

	// An allowance allows the value it expects, and no other. An object
	// names what it fixes, and a list fixes its length.
	tests := []struct {
		field  Field
		expect string
		want   bool
	}{
		{FieldExitCode, `4`, true},
		{FieldExitCode, `3`, false},
		{FieldMutations, `[{"operation":"eks:UpdateClusterVersion","target":"prod"},{"operation":"eks:UpdateNodegroupVersion","payload":{"version":"1.34"}}]`, true},
		{FieldMutations, `[{"operation":"eks:UpdateClusterVersion"},{"payload":{"version":"1.33"}}]`, false},
		{FieldMutations, `[{"operation":"eks:UpdateClusterVersion"}]`, false},
		{FieldMutations, `[{"operation":"eks:UpdateClusterVersion"},{"force":true}]`, false},
		{FieldMutations, `[]`, false},
		{FieldMutations, `{"includes":[{"target":"prod/ng-a"}]}`, true},
		{FieldMutations, `{"includes":[{"target":"prod/ng-b"}]}`, false},
	}
	for _, tt := range tests {
		pinned := []Allowance{{Scenario: "sample", Field: tt.field, Reason: "r", Expect: json.RawMessage(tt.expect)}}
		for _, d := range Compare("sample", sampleTrace(), v2, pinned) {
			if d.Field == tt.field && d.Allowed != tt.want {
				t.Errorf("with expect %s the %s is allowed = %t, want %t:\n%s", tt.expect, tt.field, d.Allowed, tt.want, d)
			}
		}
	}
}

// TestArrivalIsNotKnowledge runs two real binaries that send the same two
// requests: a read of a nodegroup and its roll. The careful one has the
// answer to the read before it sends the roll. The eager one sends the roll
// while the read is unanswered: the runner holds the read until the roll is
// accepted, and the roll until the read has arrived. In arrival order the
// two runs are the same. The comparison must still tell them apart.
func TestArrivalIsNotKnowledge(t *testing.T) {
	t.Parallel()
	isRead := func(r awsxtest.Request) bool { return r.Operation == fleettest.EKSDescribeNodegroup }
	isRoll := func(r awsxtest.Request) bool { return r.Operation == fleettest.EKSUpdateNodegroupVersion }
	racing := []Hold{
		{Request: isRead, Until: func(r awsxtest.Request, answered bool) bool { return isRoll(r) && answered }},
		{Request: isRoll, Until: func(r awsxtest.Request, _ bool) bool { return isRead(r) }},
	}
	traces := map[string]Trace{}
	for mode, holds := range map[string][]Hold{"careful": nil, "eager": racing} {
		r, args := stand(mode)
		// The eager binary sends its roll once its read is on the wire.
		// The two travel on two connections, so now and then the roll is
		// the first to arrive. That run does not show the case: try again.
		for attempt := 1; ; attempt++ {
			trace, err := r.Run(t.Context(), Scenario{Seed: fleet(), Holds: holds}, args, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if trace.ExitCode != 0 || len(trace.Requests) != 2 {
				t.Fatalf("the %s run exited %d with requests %+v, want exit 0 and two requests\n%s", mode, trace.ExitCode, trace.Requests, trace.Stderr)
			}
			if trace.Requests[0].Operation == fleettest.EKSDescribeNodegroup && trace.Requests[1].Mutation != nil {
				traces[mode] = trace
				break
			}
			if attempt == 20 {
				t.Fatalf("in 20 %s runs the roll always arrived before the read", mode)
			}
		}
	}
	if read, roll := traces["eager"].Requests[0], traces["eager"].Requests[1]; read.Arrived > roll.Arrived || read.Completed < roll.Completed {
		t.Fatalf("the eager run's read is %d to %d and its roll %d to %d, want the read to arrive first and to be answered last",
			read.Arrived, read.Completed, roll.Arrived, roll.Completed)
	}

	diffs := Compare("careful-then-eager", traces["careful"], traces["eager"], nil)
	const want = "target obligation not met: before eks:UpdateNodegroupVersion prod/ng-a (change 1), the old binary had seen nodegroup prod/ng-a status=ACTIVE, and the new binary did not read it there"
	if len(diffs) != 1 || diffs[0].Field != FieldEvidence || !strings.Contains(diffs[0].Detail, want) {
		t.Errorf("differences = %v, want one in the evidence that says %q", diffs, want)
	}
	if diffs := Compare("careful-twice", traces["careful"], traces["careful"], nil); len(diffs) != 0 {
		t.Errorf("the careful run differs from itself: %v", diffs)
	}
}

// TestAllowlistFile checks the allowlist in testdata: it loads, and every
// entry names a scenario of the suite.
func TestAllowlistFile(t *testing.T) {
	t.Parallel()
	list, err := LoadAllowlist(allowlistPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("the allowlist is empty")
	}
	var names []string
	for _, sc := range scenarios() {
		names = append(names, sc.Name)
	}
	rows, err := ContractRows("../../CONTRACT.md")
	if err != nil {
		t.Fatal(err)
	}
	expecting := map[string]bool{}
	for _, sc := range scenarios() {
		if len(sc.V2Require)+len(sc.V2Checks) > 0 {
			expecting[sc.Name] = true
		}
	}
	for _, a := range list {
		expecting[a.Scenario] = true
	}
	t.Logf("%d scenarios carry expectations of the v2 binary that differ from the old trace", len(expecting))
	for _, a := range list {
		if !slices.Contains(names, a.Scenario) {
			t.Errorf("the allowlist names the scenario %q, which the suite does not have", a.Scenario)
		}
		// The source is the first words of the old-behaviour cell of a row
		// of the contract's table.
		if !slices.ContainsFunc(rows, func(row string) bool { return strings.HasPrefix(row, a.Source) }) {
			t.Errorf("the allowlist entry for %s (%s) cites %q, and no row of the contract's table starts with that", a.Scenario, a.Field, a.Source)
		}
	}
}

func TestContractRows(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "CONTRACT.md")
	const doc = "# x\n\n## Behaviour the new CLI does not copy\n\nText.\n\n| Old behaviour | New behaviour |\n|---|---|\n| `a b` does X | Y |\n| Z happens | W |\n\nOne is kept.\n\n## Next\n\n| not | this |\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := ContractRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"`a b` does X", "Z happens"}, rows); diff != "" {
		t.Errorf("rows (-want +got):\n%s", diff)
	}
	if err := os.WriteFile(path, []byte("# nothing here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ContractRows(path); err == nil {
		t.Error("a contract with no such table gave no error")
	}
}

func TestLoadAllowlistRejects(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, file, want string }{
		{"an entry the owner has not approved", `[{"scenario":"s","field":"world","reason":"r","source":"c","ownerApproved":false,"expect":{}}]`, "not approved by the owner"},
		{"no reason", `[{"scenario":"s","field":"world","source":"c","ownerApproved":true,"expect":{}}]`, "needs a scenario, a reason, and a source"},
		{"no source", `[{"scenario":"s","field":"world","reason":"r","ownerApproved":true,"expect":{}}]`, "needs a scenario, a reason, and a source"},
		{"an unknown field", `[{"scenario":"s","field":"stdout","reason":"r","source":"c","ownerApproved":true,"expect":{}}]`, `unknown field "stdout"`},
		{"nothing expected in its place", `[{"scenario":"s","field":"world","reason":"r","source":"c","ownerApproved":true}]`, "has no expect"},
		{"not JSON", `[`, "unexpected end"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "allowlist.json")
			if err := os.WriteFile(path, []byte(tt.file), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadAllowlist(path); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("LoadAllowlist = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

// pair runs a scenario with the legacy binary and with another binary, and
// returns everything that is wrong with the other one: each difference the
// allowlist does not allow, each allowance of the scenario that no
// difference used, and each of the scenario's own checks that the other
// trace fails. A check about a field whose difference is allowed is
// skipped: it describes the old binary only. With isV2, the other binary is
// also held to what the scenario asks of the v2 binary alone.
func pair(t *testing.T, sc Scenario, other Runner, preview, args []string, allow []Allowance, isV2 bool) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), runLimit)
	defer cancel()
	got, err := other.RunAfter(ctx, sc, preview, args, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	diffs := Compare(sc.Name, run(t, sc), got, allow)
	allowed := map[Field]bool{}
	for _, d := range diffs {
		if d.Allowed {
			allowed[d.Field] = true
			t.Log(d)
			continue
		}
		problems = append(problems, d.String())
	}
	for _, a := range Unused(sc.Name, diffs, allow) {
		problems = append(problems, fmt.Sprintf("%s: the allowlist lets %s differ (%s), and it does not differ as allowed: remove the entry, or the binary has not made the change", sc.Name, a.Field, a.Source))
	}
	if err := sc.check(got, func(f Field) bool { return allowed[f] }); err != nil {
		problems = append(problems, fmt.Sprintf("%s: the run does not show what the scenario is for:\n%v\n%s", sc.Name, err, brief(got)))
	}
	if isV2 {
		if err := sc.checkV2(got); err != nil {
			problems = append(problems, fmt.Sprintf("%s: the run does not show what the contract asks of the new binary:\n%v\n%s", sc.Name, err, brief(got)))
		}
	}
	return problems
}

func scenarioNamed(t *testing.T, name string) Scenario {
	t.Helper()
	for _, sc := range scenarios() {
		if sc.Name == name {
			return sc
		}
	}
	t.Fatalf("no scenario %q", name)
	return Scenario{}
}

// TestPairedSelf pairs the legacy binary with itself, through the code
// TestPaired uses for the v2 binary: two real runs of one binary must
// compare as equal and pass the scenario's checks, whatever the reads and
// the clock did in between. The scenarios cover a preview, a dry run, a
// lost response, a signal, a second cluster, and a Kubernetes gate.
func TestPairedSelf(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"addon-update-lost-response", "nodegroup-update-interrupt", "cluster-upgrade-yes", "cluster-rollback-blocked",
		"nodegroup-update-all-clusters", "nodegroup-update-dry-run", "nodegroup-update-pdb-tightens-after-dry-run", "cluster-upgrade-pdb-blocked",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sc := scenarioNamed(t, name)
			for _, problem := range pair(t, sc, legacy, sc.LegacyPreview, sc.Legacy, nil, false) {
				t.Error(problem)
			}
		})
	}
}

// TestPairedFindsProblems pairs the legacy binary with itself in ways that
// must fail: an allowance that nothing uses, and a "new" command line that
// does something else than the scenario's.
func TestPairedFindsProblems(t *testing.T) {
	t.Parallel()
	t.Run("an allowance that no difference uses", func(t *testing.T) {
		t.Parallel()
		sc := scenarioNamed(t, "addon-update-yes")
		allow := []Allowance{{Scenario: sc.Name, Field: FieldExitCode, Reason: "r", Source: "s", OwnerApproved: true, Expect: json.RawMessage(`1`)}}
		problems := pair(t, sc, legacy, nil, sc.Legacy, allow, false)
		if len(problems) != 1 || !strings.Contains(problems[0], "does not differ as allowed") {
			t.Errorf("problems = %q, want the unused allowance", problems)
		}
	})
	t.Run("what the contract asks of the new binary", func(t *testing.T) {
		t.Parallel()
		// The old binary held to the new contract: it calls AWS, and rolls.
		sc := scenarioNamed(t, "nodegroup-update-no-yes")
		problems := strings.Join(pair(t, sc, legacy, nil, sc.Legacy, nil, true), "\n")
		for _, want := range []string{"does not show what the contract asks of the new binary", "the binary sent"} {
			if !strings.Contains(problems, want) {
				t.Errorf("the problems lack %q:\n%s", want, problems)
			}
		}
	})
	t.Run("a preview that is skipped", func(t *testing.T) {
		t.Parallel()
		// Without its dry run first, the world is not tightened... and with
		// it, it is. Run the scenario's real command with no preview on a
		// world whose AfterPreview never ran: the gate passes and the roll
		// starts, which the scenario's checks and the comparison refuse.
		sc := scenarioNamed(t, "nodegroup-update-pdb-tightens-after-dry-run")
		problems := strings.Join(pair(t, sc, legacy, nil, sc.Legacy, nil, false), "\n")
		for _, want := range []string{"mutations differs (NOT ALLOWED)", "update requests [UpdateNodegroupVersion prod/ng-a], want none"} {
			if !strings.Contains(problems, want) {
				t.Errorf("the problems lack %q:\n%s", want, problems)
			}
		}
	})
	t.Run("a refused run that skipped the budgets", func(t *testing.T) {
		t.Parallel()
		// The baseline rule excuses the baseline of a refused run, and no
		// gate read. The scenario's run is refused by its drain gate, after
		// the baseline and the budgets. The other run is refused too, and
		// read neither: it has no --yes for a pattern, and no health gate.
		sc := scenarioNamed(t, "nodegroup-update-pdb-blocked")
		skipped := []string{"nodegroup", "update", "-c", "prod", "-n", "ng-", "--skip-health-check", "-o", "json"}
		problems := strings.Join(pair(t, sc, legacy, nil, skipped, nil, false), "\n")
		if want := "the old binary had seen pdb prod/default/web allowed=0, and the new binary did not read it there"; !strings.Contains(problems, want) {
			t.Errorf("the problems lack %q:\n%s", want, problems)
		}
		if excused := "pods prod pending="; strings.Contains(problems, excused) {
			t.Errorf("the problems hold the refused run to the baseline (%q):\n%s", excused, problems)
		}
		if strings.Contains(problems, "mutations differs") || strings.Contains(problems, "refusedBeforeMutation differs") {
			t.Errorf("the other run is not a refusal before any mutation:\n%s", problems)
		}
	})
	t.Run("a dry run in place of the change", func(t *testing.T) {
		t.Parallel()
		sc := scenarioNamed(t, "nodegroup-update-yes")
		dryRun := scenarioNamed(t, "nodegroup-update-dry-run").Legacy
		problems := strings.Join(pair(t, sc, legacy, nil, dryRun, nil, false), "\n")
		for _, want := range []string{"mutations differs", "world differs", "no update request, want at least one"} {
			if !strings.Contains(problems, want) {
				t.Errorf("the problems lack %q:\n%s", want, problems)
			}
		}
	})
}

// TestPaired compares the v2 binary with the legacy binary on every
// scenario that has a v2 command line, and runs the scenario's own checks
// on the v2 trace. TestMain builds the v2 binary, so the comparison runs in
// every test run. A scenario joins it when it gets a v2 command line.
func TestPaired(t *testing.T) {
	t.Parallel()
	v2 := Runner{Binary: v2Binary}
	allow, err := LoadAllowlist(allowlistPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range scenarios() {
		if len(sc.V2) == 0 {
			continue
		}
		t.Run(sc.Name, func(t *testing.T) {
			t.Parallel()
			for _, problem := range pair(t, sc, v2, sc.V2Preview, sc.V2, allow, true) {
				t.Error(problem)
			}
		})
	}
}
