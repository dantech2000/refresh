//go:build unix

package parity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/google/go-cmp/cmp"
)

// Field is a part of a trace that Compare holds two binaries to.
type Field string

// The compared fields. Output is not one of them: the old and the new CLI
// print different documents and messages by design.
const (
	// FieldMutations is the mutating requests in order: operation, region,
	// target, payload, and outcome. Token names keep which requests shared
	// a token.
	FieldMutations Field = "mutations"
	// FieldPreconditions is, for each mutation, what the binary had
	// observed about its target, the cluster, and the previous target since
	// the mutation before.
	FieldPreconditions Field = "preconditions"
	// FieldPostconditions is what the binary observed about the resources
	// it changed, and their clusters, after its last mutation: the wait for
	// the last update and the verification that follows it.
	FieldPostconditions Field = "postconditions"
	// FieldWorld is the final state of every cluster the run touched.
	FieldWorld Field = "world"
	// FieldRefused is whether the run failed before any mutating request.
	FieldRefused Field = "refusedBeforeMutation"
	// FieldExitCode is the exit code, through ExitCodes.
	FieldExitCode Field = "exitCode"
)

// fields are every Field, in the order Compare reports them.
var fields = []Field{FieldMutations, FieldPreconditions, FieldPostconditions, FieldWorld, FieldRefused, FieldExitCode}

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
	// Expect, when set, is the value the field must have in the v2 trace,
	// as JSON: the exit code, the list of mutations, and so on. The
	// difference is then allowed only for that value, so the field is
	// pinned and not ignored. Without it any v2 value is allowed.
	Expect json.RawMessage `json:"expect,omitempty"`
}

// LoadAllowlist reads the allowlist file. Every entry must name a scenario
// and a field, give a reason and a source, and be approved by the owner.
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

// Compare returns the fields in which the v2 trace of a scenario differs
// from the legacy trace, each marked allowed or not.
//
// Preconditions and postconditions are compared for equality, not for "the
// new binary saw at least what the old one saw". An observation more is not
// harmless: it can be the read that showed the resource degraded, after
// which the binary should not have gone on. A new binary that reads more on
// purpose gets an allowance with the value it must then show (Expect).
func Compare(scenario string, legacy, v2 Trace, allow []Allowance) []Difference {
	var out []Difference
	report := func(field Field, a, b any) {
		detail := cmp.Diff(a, b)
		if detail == "" {
			return
		}
		d := Difference{Scenario: scenario, Field: field, Detail: "(-legacy +v2)\n" + detail}
		for _, al := range allow {
			if al.Scenario != scenario || al.Field != field {
				continue
			}
			if why := al.mismatch(b); why != "" {
				d.Detail += "\nthe allowlist expects another value: " + why
				continue
			}
			d.Allowed, d.Reason = true, al.Reason
		}
		out = append(out, d)
	}

	report(FieldMutations, legacy.Mutations(), v2.Mutations())
	report(FieldPreconditions, legacy.Preconditions(), v2.Preconditions())
	report(FieldPostconditions, legacy.Postconditions(), v2.Postconditions())
	report(FieldWorld, legacy.World, v2.World)
	report(FieldRefused, legacy.RefusedBeforeMutation, v2.RefusedBeforeMutation)
	want, known := ExitCodes[legacy.ExitCode]
	if !known {
		want = -1 // no v2 code stands for it: always a difference
	}
	report(FieldExitCode, want, v2.ExitCode)
	return out
}

// mismatch returns how the v2 value of the field differs from what the
// allowance expects, or "" when it expects nothing or that value.
func (a Allowance) mismatch(got any) string {
	if len(a.Expect) == 0 {
		return ""
	}
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
	var compact bytes.Buffer
	_ = json.Compact(&compact, a.Expect) // it decoded above
	if diff := cmp.Diff(want, have); diff != "" {
		return fmt.Sprintf("want %s (-want +v2)\n%s", compact.String(), diff)
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
