//go:build unix

package parity

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

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
	// FieldWorld is the final state of every cluster the run touched.
	FieldWorld Field = "world"
	// FieldRefused is whether the run failed before any mutating request.
	FieldRefused Field = "refusedBeforeMutation"
	// FieldExitCode is the exit code, through ExitCodes.
	FieldExitCode Field = "exitCode"
)

// fields are every Field, in the order Compare reports them.
var fields = []Field{FieldMutations, FieldPreconditions, FieldWorld, FieldRefused, FieldExitCode}

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
	// the field. Reason is that entry's.
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
	// Source is where the owner approved the difference.
	Source string `json:"source"`
	// OwnerApproved must be true: an entry the owner has not approved does
	// not load.
	OwnerApproved bool `json:"ownerApproved"`
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

// Compare returns the fields in which the v2 trace of a scenario differs
// from the legacy trace, each marked allowed or not.
func Compare(scenario string, legacy, v2 Trace, allow []Allowance) []Difference {
	var out []Difference
	report := func(field Field, a, b any) {
		detail := cmp.Diff(a, b)
		if detail == "" {
			return
		}
		d := Difference{Scenario: scenario, Field: field, Detail: "(-legacy +v2)\n" + detail}
		for _, al := range allow {
			if al.Scenario == scenario && al.Field == field {
				d.Allowed, d.Reason = true, al.Reason
			}
		}
		out = append(out, d)
	}

	report(FieldMutations, legacy.Mutations(), v2.Mutations())
	report(FieldPreconditions, preconditionsOf(legacy), preconditionsOf(v2))
	report(FieldWorld, legacy.World, v2.World)
	report(FieldRefused, legacy.RefusedBeforeMutation, v2.RefusedBeforeMutation)
	want, known := ExitCodes[legacy.ExitCode]
	if !known {
		want = -1 // no v2 code stands for it: always a difference
	}
	report(FieldExitCode, want, v2.ExitCode)
	return out
}

// preconditionsOf lists, for each mutation of a trace, what the binary had
// observed before it.
func preconditionsOf(t Trace) []string {
	var out []string
	for _, s := range t.Steps {
		if s.Mutation == nil {
			continue
		}
		out = append(out, fmt.Sprintf("%s %s after %v", s.Mutation.Operation, s.Mutation.Target, s.Preconditions))
	}
	return out
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
