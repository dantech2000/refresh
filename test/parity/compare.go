//go:build unix

package parity

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	gocmp "github.com/google/go-cmp/cmp"
)

// Field is a part of a trace that Compare holds two binaries to.
type Field string

// The compared fields. Output is not one of them: the old and the new CLI
// print different documents and messages by design.
//
// Mutations, the final world, the refusal, and the exit code are compared
// for equality. Evidence, settlement, verification, and decision are
// obligations: what the old binary had seen, the new one must have seen too,
// in the same part of its run. README.md has the rule.
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
	// FieldExitCode is the exit code, through ExitCodes.
	FieldExitCode Field = "exitCode"
)

// fields are every Field, in the order Compare reports them.
var fields = []Field{FieldMutations, FieldEvidence, FieldSettlement, FieldVerification, FieldDecision, FieldWorld, FieldRefused, FieldExitCode}

// ExitCodes maps an exit code of the legacy binary to the code the v2
// binary must give for the same outcome. The two contracts give the six
// codes the same meaning, so the table is the identity today. A scenario
// where the v2 contract picks another code on purpose has an allowlist
// entry for FieldExitCode; a change of meaning for every scenario goes
// here. A legacy code that is not in the table is a difference.
var ExitCodes = map[int]int{0: 0, 1: 1, 2: 2, 3: 3, 4: 4, 5: 5}

// Difference is one field in which the v2 trace of a scenario differs from
// the legacy trace.
type Difference struct {
	Scenario string
	Field    Field
	// Detail is the difference, legacy first.
	Detail string
	// Allowed is set when the allowlist has an entry for the scenario and
	// the field, and the v2 value is the one the entry expects, if it
	// expects one. Reason is that entry's.
	Allowed bool
	Reason  string
}

func (d Difference) String() string {
	status := "NOT ALLOWED"
	if d.Allowed {
		status = "allowed: " + d.Reason
	}
	return fmt.Sprintf("%s: %s differs (%s)\n%s", d.Scenario, d.Field, status, d.Detail)
}

// Allowance is one intended difference between the binaries.
type Allowance struct {
	Scenario string `json:"scenario"`
	Field    Field  `json:"field"`
	// Reason says what the v2 binary does instead, and why.
	Reason string `json:"reason"`
	// Source is where the owner approved the difference: the first words
	// of the old-behaviour cell of a row of the table "Behaviour the new
	// CLI does not copy" in CONTRACT.md. TestAllowlistFile checks that the
	// table has such a row.
	Source string `json:"source"`
	// OwnerApproved must be true: an entry the owner has not approved does
	// not load.
	OwnerApproved bool `json:"ownerApproved"`
	// Expect is what the field must be in the v2 trace, as JSON: the exit
	// code, the list of mutations, and so on. The difference is allowed for
	// that value only, so an entry pins the field and never just ignores
	// it. An object in Expect matches an object that has its keys with
	// matching values, so an entry names only what the contract fixes: the
	// operation and target of a mutation without its token, one nodegroup
	// of the world. An array matches an array of the same length, element
	// by element. {"includes": [...]} matches a list that has each of those
	// elements, for a list of evidence, which is long. Anything else must
	// be equal.
	Expect json.RawMessage `json:"expect"`
}

// LoadAllowlist reads the allowlist file. Every entry must name a scenario
// and a field, give a reason and a source, say what the v2 binary must show
// in its place, and be approved by the owner.
func LoadAllowlist(path string) ([]Allowance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list []Allowance
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var errs []error
	for i, a := range list {
		switch {
		case a.Scenario == "" || a.Reason == "" || a.Source == "":
			errs = append(errs, fmt.Errorf("%s: entry %d needs a scenario, a reason, and a source", path, i+1))
		case !slices.Contains(fields, a.Field):
			errs = append(errs, fmt.Errorf("%s: entry %d (%s) names the unknown field %q", path, i+1, a.Scenario, a.Field))
		case len(a.Expect) == 0:
			errs = append(errs, fmt.Errorf("%s: entry %d (%s, %s) has no expect: an allowance must say what the v2 binary shows instead", path, i+1, a.Scenario, a.Field))
		case !a.OwnerApproved:
			errs = append(errs, fmt.Errorf("%s: entry %d (%s, %s) is not approved by the owner", path, i+1, a.Scenario, a.Field))
		}
	}
	return list, errors.Join(errs...)
}

// ContractRows returns the old-behaviour cell of each row of the table
// "Behaviour the new CLI does not copy" in the contract file.
func ContractRows(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	_, section, found := strings.Cut(string(data), "## Behaviour the new CLI does not copy")
	if !found {
		return nil, fmt.Errorf("%s has no section \"Behaviour the new CLI does not copy\"", path)
	}
	if next := strings.Index(section, "\n## "); next >= 0 {
		section = section[:next]
	}
	var rows []string
	for line := range strings.SplitSeq(section, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 4 || strings.HasPrefix(strings.TrimSpace(cells[1]), "---") || strings.TrimSpace(cells[1]) == "Old behaviour" {
			continue
		}
		rows = append(rows, strings.TrimSpace(cells[1]))
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s: the table of behaviour the new CLI does not copy has no rows", path)
	}
	return rows, nil
}

// Compare returns the fields in which the v2 trace of a scenario does not
// hold to the legacy trace, each marked allowed or not.
func Compare(scenario string, legacy, v2 Trace, allow []Allowance) []Difference {
	var out []Difference
	report := func(field Field, detail string, got any) {
		if detail == "" {
			return
		}
		d := Difference{Scenario: scenario, Field: field, Detail: detail}
		for _, al := range allow {
			if al.Scenario != scenario || al.Field != field {
				continue
			}
			if why := al.mismatch(got); why != "" {
				d.Detail += "\nthe allowlist expects another value: " + why
				continue
			}
			d.Allowed, d.Reason = true, al.Reason
		}
		out = append(out, d)
	}
	equal := func(field Field, a, b any) {
		if detail := gocmp.Diff(a, b); detail != "" {
			report(field, "(-legacy +v2)\n"+detail, b)
		}
	}

	old, next := legacy.Mutations(), v2.Mutations()
	equal(FieldMutations, old, next)

	// Evidence is compared change by change, for as long as the two runs
	// make the same change. Where they part, the mutations differ, and that
	// difference carries the rest.
	var unmetEvidence []string
	evidence := [][]Evidence{}
	for _, c := range v2.Changes {
		evidence = append(evidence, c.Evidence)
	}
	for i := range min(len(old), len(next)) {
		if old[i].Operation != next[i].Operation || old[i].Target != next[i].Target {
			break
		}
		where := fmt.Sprintf("before %s %s (change %d)", old[i].Operation, old[i].Target, i+1)
		unmetEvidence = append(unmetEvidence, unmet(where, legacy.Changes[i].Evidence, v2.Changes[i].Evidence)...)
	}
	report(FieldEvidence, strings.Join(unmetEvidence, "\n"), evidence)
	switch {
	case len(old) > 0 && len(next) > 0:
		report(FieldSettlement, strings.Join(unmet("after its last change", legacy.Settlement, v2.Settlement), "\n"), v2.Settlement)
		report(FieldVerification, strings.Join(unmet("after its last update ended", legacy.Verification, v2.Verification), "\n"), v2.Verification)
	case len(old) == 0 && len(next) == 0:
		report(FieldDecision, strings.Join(unmet("in the run", legacy.Decision, v2.Decision), "\n"), v2.Decision)
	}

	equal(FieldWorld, legacy.World, v2.World)
	equal(FieldRefused, legacy.RefusedBeforeMutation, v2.RefusedBeforeMutation)
	want, known := ExitCodes[legacy.ExitCode]
	if !known {
		want = -1 // no v2 code stands for it: always a difference
	}
	equal(FieldExitCode, want, v2.ExitCode)
	return out
}

// unmet returns, in words, each piece of the old binary's evidence that the
// new binary's evidence for the same part of the run does not hold: the new
// binary never saw the fact there, or the last value it saw is another.
// What the new binary saw besides is not a difference.
func unmet(where string, old, held []Evidence) []string {
	var out []string
	for _, want := range old {
		when := where
		if want.BeforePrompt {
			when += ", before it asked the user"
		}
		if want.Carried {
			when += ", from before its previous change"
		}
		// Carried evidence may be older than the previous change. The new
		// binary holds it too if it read the fact then, or since.
		i := slices.IndexFunc(held, func(e Evidence) bool {
			same := e.Subject == want.Subject && e.Attribute == want.Attribute && e.BeforePrompt == want.BeforePrompt
			return same && (want.Carried || !e.Carried)
		})
		switch {
		case i < 0:
			out = append(out, fmt.Sprintf("%s obligation not met: %s, the old binary had seen %s %s=%s, and the new binary did not read it there",
				want.Kind, when, want.Subject, want.Attribute, want.Value))
		case held[i].Value != want.Value:
			out = append(out, fmt.Sprintf("%s obligation not met: %s, the old binary had seen %s %s=%s, and the new binary last saw %s",
				want.Kind, when, want.Subject, want.Attribute, want.Value, held[i].Value))
		}
	}
	return out
}

// mismatch returns how the v2 value of the field differs from what the
// allowance expects, or "" when it is that value.
func (a Allowance) mismatch(got any) string {
	encoded, err := json.Marshal(got)
	if err != nil {
		return err.Error()
	}
	var want, have any
	if err := json.Unmarshal(a.Expect, &want); err != nil {
		return "the expect value is not JSON: " + err.Error()
	}
	if err := json.Unmarshal(encoded, &have); err != nil {
		return err.Error()
	}
	if why := subset("", want, have); why != "" {
		var compact bytes.Buffer
		_ = json.Compact(&compact, a.Expect) // it decoded above
		return fmt.Sprintf("want %s, and the v2 trace has %s: %s", compact.String(), encoded, why)
	}
	return ""
}

// subset returns why have does not match want, or "". See Allowance.Expect.
func subset(path string, want, have any) string {
	switch w := want.(type) {
	case map[string]any:
		if list, ok := have.([]any); ok && len(w) == 1 && w["includes"] != nil {
			wanted, _ := w["includes"].([]any)
			for _, element := range wanted {
				if !slices.ContainsFunc(list, func(e any) bool { return subset("", element, e) == "" }) {
					return fmt.Sprintf("%s does not include %v", cmp.Or(path, "the value"), element)
				}
			}
			return ""
		}
		h, ok := have.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s is not an object", cmp.Or(path, "the value"))
		}
		for _, key := range slices.Sorted(maps.Keys(w)) {
			if _, has := h[key]; !has {
				return fmt.Sprintf("%s.%s is missing", path, key)
			}
			if why := subset(path+"."+key, w[key], h[key]); why != "" {
				return why
			}
		}
		return ""
	case []any:
		h, ok := have.([]any)
		if !ok && have != nil {
			return fmt.Sprintf("%s is not a list", cmp.Or(path, "the value"))
		}
		if len(h) != len(w) {
			return fmt.Sprintf("%s has %d element(s), want %d", cmp.Or(path, "the value"), len(h), len(w))
		}
		for i := range w {
			if why := subset(fmt.Sprintf("%s[%d]", path, i), w[i], h[i]); why != "" {
				return why
			}
		}
		return ""
	}
	if diff := gocmp.Diff(want, have); diff != "" {
		return fmt.Sprintf("%s is %v, want %v", cmp.Or(path, "the value"), have, want)
	}
	return ""
}

// Unallowed returns the differences that no allowlist entry covers.
func Unallowed(diffs []Difference) []Difference {
	var out []Difference
	for _, d := range diffs {
		if !d.Allowed {
			out = append(out, d)
		}
	}
	return out
}

// Unused returns the allowances for the scenario that none of its
// differences used: the binaries agree where the allowlist says they do
// not, so the entry is stale or the v2 binary has not made the change.
func Unused(scenario string, diffs []Difference, allow []Allowance) []Allowance {
	var out []Allowance
	for _, a := range allow {
		if a.Scenario != scenario {
			continue
		}
		used := slices.ContainsFunc(diffs, func(d Difference) bool { return d.Field == a.Field && d.Allowed })
		if !used {
			out = append(out, a)
		}
	}
	return out
}
