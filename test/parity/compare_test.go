//go:build unix

package parity

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

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
// wrong tested binary would, and checks that Compare reports that field and no
// other, and says in words which obligation is not met.
func TestCompareFindsDifferences(t *testing.T) {
	t.Parallel()
	steps := sampleSteps
	edit := func(f func(tested *Trace)) func() Trace {
		return func() Trace {
			tested := sampleTrace()
			f(&tested)
			return tested
		}
	}
	rebuilt := func(s []step) func() Trace {
		return func() Trace {
			tested := build(s...)
			tested.World = sampleTrace().World
			return tested
		}
	}
	// held makes the read at index i arrive where it is and get its answer
	// only after everything else: the binary did not wait for it.
	held := func(i int) func() Trace {
		return func() Trace {
			tested := sampleTrace()
			reads := 0
			for j := range tested.Requests {
				if reads == i {
					tested.Requests[j].Completed = seq(&tested)
					break
				}
				reads++
			}
			tested.derive()
			return tested
		}
	}
	tests := []struct {
		name   string
		tested func() Trace
		want   []Field
		says   string
	}{
		{name: "nothing", tested: sampleTrace},
		{name: "other words on stdout and stderr", tested: edit(func(tested *Trace) { tested.Stdout, tested.Stderr = "new words", "new words" })},

		// Harmless: the run reads more, or otherwise, and knows the same.
		{name: "a read more of something else", tested: rebuilt(slices.Insert(steps(), 8, read("eks:DescribeAddon", "prod/coredns", fact("addon prod/coredns", "status", "ACTIVE"))))},
		{name: "the same read twice", tested: rebuilt(slices.Insert(steps(), 8, nodegroup, nodegroup))}, //nolint:gocritic // the same read twice, on purpose
		{name: "two reads in one answer", tested: rebuilt(slices.Insert(without(steps(), 6, 7), 6, read("eks:ListNodegroups", "prod",
			fact("nodegroup prod/ng-a", "status", "ACTIVE"), fact("nodegroup prod/ng-a", "version", "1.32"), fact("nodegroup prod/ng-b", "status", "ACTIVE"))))},
		{name: "a gate read earlier in the same window", tested: rebuilt(moved(steps(), 8, 5))},
		{name: "the verification in another order", tested: rebuilt(moved(steps(), 14, 12))},
		{name: "an error that a later read replaced", tested: rebuilt(slices.Insert(steps(), 6, read("eks:DescribeNodegroup", "prod/ng-a", fact("nodegroup prod/ng-a", "read", "error=ThrottlingException"))))},

		// One per kind of obligation.
		{name: "the nodegroup was not read before its roll", tested: rebuilt(without(steps(), 6)), want: []Field{FieldEvidence},
			says: "target obligation not met: before eks:UpdateNodegroupVersion prod/ng-a (change 2), the recorded run had seen nodegroup prod/ng-a status=ACTIVE, and this run did not read it there"},
		{name: "the roll was sent while the read of the nodegroup was unanswered", tested: held(6), want: []Field{FieldEvidence},
			says: "target obligation not met"},
		{name: "the roll started before the control plane was seen to finish", tested: rebuilt(without(steps(), 4)), want: []Field{FieldEvidence},
			says: "prerequisite obligation not met: before eks:UpdateNodegroupVersion prod/ng-a (change 2), the recorded run had seen update prod 1 status=Successful, and this run last saw InProgress"},
		{name: "the budgets were read before the control plane changed, and not again", tested: rebuilt(moved(steps(), 8, 2)), want: []Field{FieldEvidence},
			says: "gate obligation not met: before eks:UpdateNodegroupVersion prod/ng-a (change 2), the recorded run had seen pdb prod/default/web allowed=1"},
		{name: "the gate saw another value", tested: rebuilt(slices.Insert(without(steps(), 8), 8, read("kube:ListPodDisruptionBudgets", "prod", fact("pdb prod/default/web", "allowed", "0")))),
			want: []Field{FieldEvidence}, says: "and this run last saw 0"},
		{name: "the gate could not read", tested: rebuilt(slices.Insert(steps(), 9, read("eks:DescribeNodegroup", "prod/ng-b", fact("nodegroup prod/ng-b", "read", "error=AccessDeniedException")))),
			want: nil}, // the recorded run has no such obligation; the checks of a scenario catch a binary that goes on
		{name: "the last update was not seen to finish", tested: rebuilt(without(steps(), 11, 13, 14)), want: []Field{FieldSettlement, FieldVerification},
			says: "settlement obligation not met: after its last change, the recorded run had seen update prod/ng-a 2 status=Successful, and this run last saw InProgress"},
		{name: "no verification after the last change", tested: rebuilt(without(steps(), 13, 14)), want: []Field{FieldVerification},
			says: "verification obligation not met: after its last update ended, the recorded run had seen node prod/a-1 condition=Ready, and this run did not read it there"},
		{name: "the pods were checked before the roll had ended", tested: rebuilt(moved(steps(), 13, 11)), want: []Field{FieldVerification},
			says: "the recorded run had seen pods prod pending=none"},

		// Compared for equality.
		{name: "another target version", tested: edit(func(tested *Trace) { tested.Changes[1].Mutation.Payload["version"] = "1.34" }), want: []Field{FieldMutations}},
		{name: "a field more in the payload", tested: edit(func(tested *Trace) { tested.Changes[1].Mutation.Payload["force"] = true }), want: []Field{FieldMutations}},
		{name: "one token for two requests", tested: edit(func(tested *Trace) { tested.Changes[1].Mutation.Payload["clientRequestToken"] = "token-1" }), want: []Field{FieldMutations}},
		{name: "a mutation that was refused", tested: edit(func(tested *Trace) { tested.Changes[1].Mutation.Outcome = "error=ResourceInUseException" }), want: []Field{FieldMutations}},
		{name: "the target of the second change was read before the first change only", tested: rebuilt(moved(steps(), 6, 2)), want: []Field{FieldEvidence},
			says: "the recorded run had seen nodegroup prod/ng-a status=ACTIVE, and this run did not read it there"},
		{name: "a mutation more", tested: rebuilt(append(steps(), change("eks:UpdateAddon", "prod/coredns", "token-3", "accepted"))), want: []Field{FieldMutations, FieldSettlement, FieldVerification}},
		{name: "the nodegroup rolled before the control plane", tested: rebuilt(moved(steps(), 9, 2)), want: []Field{FieldMutations}},
		{name: "another final state", tested: edit(func(tested *Trace) {
			tested.World[Region+"/prod"].Nodegroups["ng-a"] = "1.33 r2 DEGRADED 2/1/3 issues=NodeCreationFailure"
		}), want: []Field{FieldWorld}},
		{name: "another cluster touched", tested: edit(func(tested *Trace) { tested.World[Region+"/staging"] = ClusterState{Cluster: "1.32 ACTIVE"} }), want: []Field{FieldWorld}},
		{name: "a refusal", tested: func() Trace {
			tested := build(clusterOld, nodegroup)
			tested.RefusedBeforeMutation, tested.ExitCode = true, 3
			tested.World = map[string]ClusterState{Region + "/prod": {Cluster: "1.32 ACTIVE"}}
			return tested
		}, want: []Field{FieldMutations, FieldWorld, FieldRefused, FieldExitCode}},
		{name: "another exit code", tested: edit(func(tested *Trace) { tested.ExitCode = 5 }), want: []Field{FieldExitCode}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []Field
			var text []string
			for _, d := range Compare("sample", sampleTrace(), tt.tested()) {
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
		name   string
		tested Trace
		says   string
	}{
		{"the same, in another order", build(done, clusterOld), ""},
		{"it did not wait for the update it attached to", build(clusterOld, updateIs("prod", "1", "InProgress")),
			"decision obligation not met: in the run, the recorded run had seen update prod 1 status=Successful, and this run last saw InProgress"},
		{"it did not look at the update", build(clusterOld), "this run did not read it there"},
	}
	for _, tt := range tests {
		diffs := Compare("sample", old, tt.tested)
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
// a run that makes one meets the obligation too. One that never read
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
		name   string
		tested Trace
		says   string
	}{
		{"the same", build(other, nodegroup, a, done, b), ""},
		{"it reads the second target again", build(nodegroup, other, a, done, other, b), ""}, //nolint:gocritic // the same read twice, on purpose
		{"it reads the second target only between the changes", build(nodegroup, a, done, other, b), "gate obligation not met: before eks:UpdateNodegroupVersion prod/ng-a (change 1)"},
		{"it never read the second target", build(nodegroup, a, done, b), "from before its previous change, the recorded run had seen nodegroup prod/ng-b status=ACTIVE, and this run did not read it there"},
		{"it read the second target again and it had degraded", build(nodegroup, other, a, done, degraded, b), "and this run last saw DEGRADED"},
		{"it read the second target only before the prompt", build(nodegroup, other, confirm, nodegroup, a, done, b), "did not read it there"}, //nolint:gocritic // the same read twice, on purpose
	}
	for _, tt := range tests {
		var text []string
		for _, d := range Compare("sample", old, tt.tested) {
			text = append(text, d.String())
		}
		switch all := strings.Join(text, "\n"); {
		case tt.says == "" && all != "":
			t.Errorf("%s: want no difference:\n%s", tt.name, all)
		case tt.says != "" && (len(text) != 1 || !strings.Contains(all, "evidence differs") || !strings.Contains(all, tt.says)):
			t.Errorf("%s: want one difference in the evidence that says %q:\n%s", tt.name, tt.says, all)
		}
	}

	// A fresh read is owed fresh: the recorded run read the second target
	// again, so a run that holds only the read from the start does
	// not meet it.
	careful := build(nodegroup, other, a, done, other, b) //nolint:gocritic // the same read twice, on purpose
	if diffs := Compare("sample", careful, old); len(diffs) != 1 || !strings.Contains(diffs[0].Detail, "this run did not read it there") {
		t.Errorf("a batch binary held to one that reads again: %v, want one unmet obligation", diffs)
	}
}

// TestCompareBaselineRule holds the baseline rule to its words. A
// binary may read the baseline before its drain gate, so a run that the gate
// refuses has read it. A run that is refused too owes no baseline.
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
	const baseline = "the recorded run had seen pods prod pending=none"
	tests := []struct {
		name        string
		old, tested Trace
		// says is a text of the one difference, in the decision. Empty
		// means no difference.
		says string
	}{
		{name: "refused with no baseline", old: old, tested: refused(nodegroup, nodesReady, tight)},
		{name: "refused with the baseline", old: old, tested: refused(nodegroup, pendingPods, nodesReady, tight)},
		{name: "refused, and the budgets were not read", old: old, tested: refused(nodegroup, nodesReady),
			says: "the recorded run had seen pdb prod/default/web allowed=0, and this run did not read it there"},
		{name: "refused, and the nodes were not read", old: old, tested: refused(nodegroup, tight),
			says: "the recorded run had seen node prod/a-1 condition=Ready, and this run did not read it there"},
		{name: "a run that was not refused owes the list", old: build(nodegroup, pendingPods), tested: build(nodegroup), says: baseline},
		{name: "after an update that the run followed, the list is the verification",
			old:    refused(nodegroup, updateIs("prod/ng-a", "1", "Failed"), pendingPods),
			tested: refused(nodegroup, updateIs("prod/ng-a", "1", "Failed")), says: baseline},
	}
	for _, tt := range tests {
		var text []string
		for _, d := range Compare("sample", tt.old, tt.tested) {
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
	diffs := Compare("sample", build(nodegroup, pendingPods, roll), build(nodegroup, roll))
	if len(diffs) != 1 || diffs[0].Field != FieldEvidence || !strings.Contains(diffs[0].Detail, baseline) {
		t.Errorf("a roll with no baseline before it: %v, want one unmet gate obligation for the Pending pods", diffs)
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

	diffs := Compare("careful-then-eager", traces["careful"], traces["eager"])
	const want = "target obligation not met: before eks:UpdateNodegroupVersion prod/ng-a (change 1), the recorded run had seen nodegroup prod/ng-a status=ACTIVE, and this run did not read it there"
	if len(diffs) != 1 || diffs[0].Field != FieldEvidence || !strings.Contains(diffs[0].Detail, want) {
		t.Errorf("differences = %v, want one in the evidence that says %q", diffs, want)
	}
	if diffs := Compare("careful-twice", traces["careful"], traces["careful"]); len(diffs) != 0 {
		t.Errorf("the careful run differs from itself: %v", diffs)
	}
}

// check runs a scenario with the binary and returns everything that is
// wrong with the run: each difference from the recorded trace of the
// scenario, and each of the scenario's checks that the run fails. With
// -update, a run that passes its checks becomes the recorded trace.
func check(t *testing.T, sc Scenario, bin Runner, preview, args []string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), runLimit)
	defer cancel()
	got, err := bin.RunAfter(ctx, sc, preview, args, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	if err := sc.check(got); err != nil {
		problems = append(problems, fmt.Sprintf("%s: the run does not show what the scenario is for:\n%v\n%s", sc.Name, err, brief(got)))
	}
	if *update {
		if len(problems) == 0 {
			record(t, sc, got)
		}
		return problems
	}
	for _, d := range Compare(sc.Name, recorded(t, sc), got) {
		problems = append(problems, d.String())
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

// TestCheckFindsProblems runs scenarios in ways that must fail against
// their recorded traces: a run that breaks the contract, a skipped preview,
// and a dry run in place of the change. Each case proves that the
// comparison and the checks catch that problem.
func TestCheckFindsProblems(t *testing.T) {
	t.Parallel()
	if *update {
		t.Skip("-update records the traces; there is nothing to compare")
	}
	bin := Runner{Binary: binary}
	t.Run("what the contract asks of the binary", func(t *testing.T) {
		t.Parallel()
		// With --yes, the run calls AWS and rolls, which the contract of
		// this scenario (no terminal, no --yes) refuses.
		sc := scenarioNamed(t, "nodegroup-update-no-yes")
		problems := strings.Join(check(t, sc, bin, nil, append(slices.Clone(sc.Args), "--yes")), "\n")
		for _, want := range []string{"does not show what the scenario is for", "the binary sent", "mutations differs"} {
			if !strings.Contains(problems, want) {
				t.Errorf("the problems lack %q:\n%s", want, problems)
			}
		}
	})
	t.Run("a preview that is skipped", func(t *testing.T) {
		t.Parallel()
		// Without its dry run first, the world is not tightened: the gate
		// passes and the roll starts, which the scenario's checks and the
		// comparison refuse.
		sc := scenarioNamed(t, "nodegroup-update-pdb-tightens-after-dry-run")
		problems := strings.Join(check(t, sc, bin, nil, sc.Args), "\n")
		for _, want := range []string{"mutations differs", "update requests [UpdateNodegroupVersion prod/ng-a], want none"} {
			if !strings.Contains(problems, want) {
				t.Errorf("the problems lack %q:\n%s", want, problems)
			}
		}
	})
	t.Run("a dry run in place of the change", func(t *testing.T) {
		t.Parallel()
		sc := scenarioNamed(t, "nodegroup-update-yes")
		dryRun := scenarioNamed(t, "nodegroup-update-dry-run").Args
		problems := strings.Join(check(t, sc, bin, nil, dryRun), "\n")
		for _, want := range []string{"mutations differs", "world differs", "no update request, want at least one"} {
			if !strings.Contains(problems, want) {
				t.Errorf("the problems lack %q:\n%s", want, problems)
			}
		}
	})
}

// TestScenarios runs the binary on every scenario, holds the run to the
// recorded trace of the scenario, and runs the scenario's checks. TestMain
// builds the binary, so the suite runs in every test run. With -update it
// records the trace of each scenario whose checks pass.
func TestScenarios(t *testing.T) {
	t.Parallel()
	bin := Runner{Binary: binary}
	for _, sc := range scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			t.Parallel()
			for _, problem := range check(t, sc, bin, sc.Preview, sc.Args) {
				t.Error(problem)
			}
		})
	}
}
