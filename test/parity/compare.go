//go:build unix

package parity

import (
	"fmt"
	"slices"
	"strings"

	gocmp "github.com/google/go-cmp/cmp"
)

// Field is a part of a trace that Compare holds a run to.
type Field string

// The compared fields. Output is not one of them: the command goldens of
// internal/cmd hold it.
//
// Mutations, the final world, the refusal, and the exit code are compared
// for equality. Evidence, settlement, verification, and decision are
// obligations: what the recorded run had seen, a run must have seen too, in
// the same part of its run. README.md has the rule.
const (
	// FieldMutations is the mutating requests in order: operation, region,
	// target, payload, and outcome. Token names keep which requests shared
	// a token.
	FieldMutations Field = "mutations"
	// FieldEvidence is, for each mutation, what the binary had seen when it
	// sent it: the target, the prerequisites, and the gates.
	FieldEvidence Field = "evidence"
	// FieldSettlement is what the binary saw of the changed resources and
	// their updates after its last mutation.
	FieldSettlement Field = "settlement"
	// FieldVerification is what else the binary saw after its last update
	// ended.
	FieldVerification Field = "verification"
	// FieldDecision is what a run that changed nothing saw.
	FieldDecision Field = "decision"
	// FieldWorld is the final state of every cluster the run touched.
	FieldWorld Field = "world"
	// FieldRefused is whether the run failed before any mutating request.
	FieldRefused Field = "refusedBeforeMutation"
	// FieldExitCode is the exit code.
	FieldExitCode Field = "exitCode"
)

// Difference is one field in which a run of a scenario does not hold to
// the recorded trace of the scenario.
type Difference struct {
	Scenario string
	Field    Field
	// Detail is the difference, the recorded trace first.
	Detail string
}

func (d Difference) String() string {
	return fmt.Sprintf("%s: %s differs\n%s", d.Scenario, d.Field, d.Detail)
}

// Compare returns the fields in which the trace got of a run of a scenario
// does not hold to the recorded trace of the scenario.
func Compare(scenario string, recorded, got Trace) []Difference {
	var out []Difference
	report := func(field Field, detail string) {
		if detail != "" {
			out = append(out, Difference{Scenario: scenario, Field: field, Detail: detail})
		}
	}
	equal := func(field Field, a, b any) {
		if detail := gocmp.Diff(a, b); detail != "" {
			report(field, "(-recorded +got)\n"+detail)
		}
	}

	old, next := recorded.Mutations(), got.Mutations()
	equal(FieldMutations, old, next)

	// Evidence is compared change by change, for as long as the two runs
	// make the same change. Where they part, the mutations differ, and that
	// difference carries the rest.
	var unmetEvidence []string
	for i := range min(len(old), len(next)) {
		if old[i].Operation != next[i].Operation || old[i].Target != next[i].Target {
			break
		}
		where := fmt.Sprintf("before %s %s (change %d)", old[i].Operation, old[i].Target, i+1)
		unmetEvidence = append(unmetEvidence, unmet(where, recorded.Changes[i].Evidence, got.Changes[i].Evidence)...)
	}
	report(FieldEvidence, strings.Join(unmetEvidence, "\n"))
	switch {
	case len(old) > 0 && len(next) > 0:
		report(FieldSettlement, strings.Join(unmet("after its last change", recorded.Settlement, got.Settlement), "\n"))
		report(FieldVerification, strings.Join(unmet("after its last update ended", recorded.Verification, got.Verification), "\n"))
	case len(old) == 0 && len(next) == 0:
		report(FieldDecision, strings.Join(unmet("in the run", owedByDecision(recorded, got), got.Decision), "\n"))
	}

	equal(FieldWorld, recorded.World, got.World)
	equal(FieldRefused, recorded.RefusedBeforeMutation, got.RefusedBeforeMutation)
	equal(FieldExitCode, recorded.ExitCode, got.ExitCode)
	return out
}

// owedByDecision returns the decision evidence of the recorded trace that
// a run is held to. It is all of it, less what the baseline rule takes out.
//
// The baseline rule: evidence of the verification baseline is owed only
// when a mutation follows it. The baseline is what a roll compares its
// verification with, so a run that changes nothing has no use for it, and
// a run that a gate refuses may or may not have read it first. Those reads
// create no obligation for another run that was refused before any
// mutation too.
//
// The rule covers baseline evidence and nothing else (see baselineOnly),
// and only a run that followed no update: after an update that the run
// attached to, the same read is the verification, which is owed.
func owedByDecision(recorded, got Trace) []Evidence {
	if !recorded.RefusedBeforeMutation || !got.RefusedBeforeMutation {
		return recorded.Decision
	}
	if slices.ContainsFunc(recorded.Decision, func(e Evidence) bool { return e.kind() == "update" }) {
		return recorded.Decision
	}
	return slices.DeleteFunc(slices.Clone(recorded.Decision), func(e Evidence) bool { return e.baselineOnly() })
}

// baselineOnly reports whether only the read of a verification baseline
// gives the fact: which pods of the whole cluster are Pending. No gate
// reads that list. A gate lists the pods of one namespace, and such a list
// gives no "pending" fact (see kubeFacts).
//
// The baseline also lists the nodes. That list gives the same facts as the
// node list of a gate, so the rule leaves them owed: a refused run must
// still have read the nodes that its gate judged.
func (f Fact) baselineOnly() bool {
	return f.kind() == "pods" && f.Attribute == "pending"
}

// unmet returns, in words, each piece of the recorded evidence that the
// run's evidence for the same part of the run does not hold: the run never
// saw the fact there, or the last value it saw is another. What the run saw
// besides is not a difference.
func unmet(where string, owed, held []Evidence) []string {
	var out []string
	for _, want := range owed {
		when := where
		if want.BeforePrompt {
			when += ", before it asked the user"
		}
		if want.Carried {
			when += ", from before its previous change"
		}
		// Carried evidence may be older than the previous change. The run
		// holds it too if it read the fact then, or since.
		i := slices.IndexFunc(held, func(e Evidence) bool {
			same := e.Subject == want.Subject && e.Attribute == want.Attribute && e.BeforePrompt == want.BeforePrompt
			return same && (want.Carried || !e.Carried)
		})
		switch {
		case i < 0:
			out = append(out, fmt.Sprintf("%s obligation not met: %s, the recorded run had seen %s %s=%s, and this run did not read it there",
				want.Kind, when, want.Subject, want.Attribute, want.Value))
		case held[i].Value != want.Value:
			out = append(out, fmt.Sprintf("%s obligation not met: %s, the recorded run had seen %s %s=%s, and this run last saw %s",
				want.Kind, when, want.Subject, want.Attribute, want.Value, held[i].Value))
		}
	}
	return out
}
