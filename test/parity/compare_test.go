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
	"time"

	"github.com/google/go-cmp/cmp"
)

const allowlistPath = "testdata/allowlist.json"

// sampleTrace is a small upgrade: the control plane, then one nodegroup.
func sampleTrace() Trace {
	return Trace{
		ExitCode: 0,
		Stdout:   "old words",
		Steps: []Step{
			{
				Reads:         []Read{{Operation: "eks:DescribeCluster", Region: Region, Target: "prod", Observed: "1.32 ACTIVE"}},
				Preconditions: []string{"eks:DescribeCluster prod: 1.32 ACTIVE"},
				Mutation: &Mutation{
					Operation: "eks:UpdateClusterVersion", Region: Region, Target: "prod",
					Payload: map[string]any{"version": "1.33", "clientRequestToken": "token-1"}, Outcome: "accepted update 1",
				},
			},
			{
				Reads: []Read{
					{Operation: "eks:DescribeNodegroup", Region: Region, Target: "prod/ng-a", Observed: "1.32 r1 ACTIVE 2/1/3"},
					{Operation: "eks:DescribeUpdate", Region: Region, Target: "prod", Observed: "update 1 VersionUpdate Successful"},
				},
				Preconditions: []string{"eks:DescribeNodegroup prod/ng-a: 1.32 r1 ACTIVE 2/1/3", "eks:DescribeUpdate prod: update 1 VersionUpdate Successful"},
				Mutation: &Mutation{
					Operation: "eks:UpdateNodegroupVersion", Region: Region, Target: "prod/ng-a",
					Payload: map[string]any{"version": "1.33", "clientRequestToken": "token-2"}, Outcome: "accepted update 2",
				},
			},
			{Reads: []Read{
				{Operation: "eks:DescribeUpdate", Region: Region, Target: "prod/ng-a", Observed: "update 2 VersionUpdate Successful"},
				{Operation: "eks:DescribeNodegroup", Region: Region, Target: "prod/ng-a", Observed: "1.33 r2 ACTIVE 2/1/3"},
				{Operation: "kube:ListPods", Region: Region, Target: "prod", Observed: "[status.phase=Pending]"},
			}},
		},
		World: map[string]ClusterState{Region + "/prod": {Cluster: "1.33 ACTIVE", Nodegroups: map[string]string{"ng-a": "1.33 r2 ACTIVE 2/1/3"}}},
	}
}

// TestCompareFindsDifferences alters one thing in a copy of a trace, as a
// wrong v2 binary would, and checks that Compare reports that field and no
// other.
func TestCompareFindsDifferences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		alter func(v2 *Trace)
		want  []Field
	}{
		{"nothing", func(*Trace) {}, nil},
		{"other words on stdout and stderr", func(v2 *Trace) { v2.Stdout, v2.Stderr = "new words", "new words" }, nil},
		{"another number of reads that show the same", func(v2 *Trace) {
			v2.Steps[0].Reads = append(v2.Steps[0].Reads, Read{Operation: "eks:ListClusters", Region: Region, Observed: "[prod]"})
		}, nil},
		{"the nodegroup rolled before the control plane", func(v2 *Trace) {
			v2.Steps[0].Mutation, v2.Steps[1].Mutation = v2.Steps[1].Mutation, v2.Steps[0].Mutation
		}, []Field{FieldMutations, FieldPreconditions}},
		{"another target version", func(v2 *Trace) { v2.Steps[1].Mutation.Payload["version"] = "1.34" }, []Field{FieldMutations}},
		{"a field more in the payload", func(v2 *Trace) { v2.Steps[1].Mutation.Payload["force"] = true }, []Field{FieldMutations}},
		{"one token for two requests", func(v2 *Trace) { v2.Steps[1].Mutation.Payload["clientRequestToken"] = "token-1" }, []Field{FieldMutations}},
		{"a mutation that was refused", func(v2 *Trace) { v2.Steps[1].Mutation.Outcome = "error=ResourceInUseException" }, []Field{FieldMutations}},
		{"a mutation more", func(v2 *Trace) {
			v2.Steps[2].Mutation = &Mutation{Operation: "eks:UpdateAddon", Region: Region, Target: "prod/coredns", Outcome: "accepted update 3"}
		}, []Field{FieldMutations, FieldPreconditions, FieldPostconditions}},
		{"the roll started before the control plane was seen to finish", func(v2 *Trace) {
			v2.Steps[1].Preconditions = v2.Steps[1].Preconditions[:1]
		}, []Field{FieldPreconditions}},
		{"the roll started while the control plane was still updating", func(v2 *Trace) {
			v2.Steps[1].Preconditions[1] = "eks:DescribeUpdate prod: update 1 VersionUpdate InProgress"
		}, []Field{FieldPreconditions}},
		{"the nodegroup was not read before its roll", func(v2 *Trace) {
			v2.Steps[1].Preconditions = v2.Steps[1].Preconditions[1:]
		}, []Field{FieldPreconditions}},
		{"no verification after the last change", func(v2 *Trace) { v2.Steps = v2.Steps[:2] }, []Field{FieldPostconditions}},
		{"the last update was not seen to finish", func(v2 *Trace) {
			v2.Steps[2].Reads = v2.Steps[2].Reads[1:]
		}, []Field{FieldPostconditions}},
		{"a read more after the last change, of something else", func(v2 *Trace) {
			v2.Steps[2].Reads = append(v2.Steps[2].Reads, Read{Operation: "eks:DescribeAddon", Region: Region, Target: "prod/coredns", Observed: "v1 ACTIVE"})
		}, nil},
		{"another final state", func(v2 *Trace) {
			v2.World[Region+"/prod"].Nodegroups["ng-a"] = "1.33 r2 DEGRADED 2/1/3 issues=NodeCreationFailure"
		}, []Field{FieldWorld}},
		{"another cluster touched", func(v2 *Trace) { v2.World[Region+"/staging"] = ClusterState{Cluster: "1.32 ACTIVE"} }, []Field{FieldWorld}},
		{"a refusal", func(v2 *Trace) {
			v2.Steps, v2.RefusedBeforeMutation, v2.ExitCode = nil, true, 3
			v2.World[Region+"/prod"] = ClusterState{Cluster: "1.32 ACTIVE"}
		}, []Field{FieldMutations, FieldPreconditions, FieldPostconditions, FieldWorld, FieldRefused, FieldExitCode}},
		{"another exit code", func(v2 *Trace) { v2.ExitCode = 5 }, []Field{FieldExitCode}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v2 := sampleTrace()
			tt.alter(&v2)
			var got []Field
			for _, d := range Compare("sample", sampleTrace(), v2, nil) {
				if d.Allowed {
					t.Errorf("%s is allowed with an empty allowlist", d.Field)
				}
				got = append(got, d.Field)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("fields that differ (-want +got):\n%s", diff)
			}
		})
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
	v2.Steps[1].Mutation.Payload["version"] = "1.34"
	allow := []Allowance{
		{Scenario: "sample", Field: FieldExitCode, Reason: "the contract picks 4"},
		{Scenario: "other", Field: FieldMutations, Reason: "not this scenario"},
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

	// An allowance with an expected value allows that value only.
	for value, wantAllowed := range map[string]bool{"4": true, "3": false} {
		pinned := []Allowance{{Scenario: "sample", Field: FieldExitCode, Reason: "the contract picks 4", Expect: json.RawMessage(value)}}
		for _, d := range Compare("sample", sampleTrace(), v2, pinned) {
			if d.Field == FieldExitCode && d.Allowed != wantAllowed {
				t.Errorf("with expect %s the exit code 4 is allowed = %t, want %t:\n%s", value, d.Allowed, wantAllowed, d)
			}
		}
	}
	if text := diffs[1].String(); !strings.Contains(text, "allowed: the contract picks 4") {
		t.Errorf("the allowed difference reads %q", text)
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
		{"an entry the owner has not approved", `[{"scenario":"s","field":"world","reason":"r","source":"c","ownerApproved":false}]`, "not approved by the owner"},
		{"no reason", `[{"scenario":"s","field":"world","source":"c","ownerApproved":true}]`, "needs a scenario, a reason, and a source"},
		{"no source", `[{"scenario":"s","field":"world","reason":"r","ownerApproved":true}]`, "needs a scenario, a reason, and a source"},
		{"an unknown field", `[{"scenario":"s","field":"stdout","reason":"r","source":"c","ownerApproved":true}]`, `unknown field "stdout"`},
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
// skipped: it describes the old binary only.
func pair(t *testing.T, sc Scenario, other Runner, preview, args []string, allow []Allowance) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
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
			for _, problem := range pair(t, sc, legacy, sc.LegacyPreview, sc.Legacy, nil) {
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
		allow := []Allowance{{Scenario: sc.Name, Field: FieldExitCode, Reason: "r", Source: "s", OwnerApproved: true}}
		problems := pair(t, sc, legacy, nil, sc.Legacy, allow)
		if len(problems) != 1 || !strings.Contains(problems[0], "does not differ as allowed") {
			t.Errorf("problems = %q, want the unused allowance", problems)
		}
	})
	t.Run("a preview that is skipped", func(t *testing.T) {
		t.Parallel()
		// Without its dry run first, the world is not tightened... and with
		// it, it is. Run the scenario's real command with no preview on a
		// world whose AfterPreview never ran: the gate passes and the roll
		// starts, which the scenario's checks and the comparison refuse.
		sc := scenarioNamed(t, "nodegroup-update-pdb-tightens-after-dry-run")
		problems := strings.Join(pair(t, sc, legacy, nil, sc.Legacy, nil), "\n")
		for _, want := range []string{"mutations differs (NOT ALLOWED)", "update requests [UpdateNodegroupVersion prod/ng-a], want none"} {
			if !strings.Contains(problems, want) {
				t.Errorf("the problems lack %q:\n%s", want, problems)
			}
		}
	})
	t.Run("a dry run in place of the change", func(t *testing.T) {
		t.Parallel()
		sc := scenarioNamed(t, "nodegroup-update-yes")
		dryRun := scenarioNamed(t, "nodegroup-update-dry-run").Legacy
		problems := strings.Join(pair(t, sc, legacy, nil, dryRun, nil), "\n")
		for _, want := range []string{"mutations differs", "postconditions differs", "world differs", "no update request, want at least one"} {
			if !strings.Contains(problems, want) {
				t.Errorf("the problems lack %q:\n%s", want, problems)
			}
		}
	})
}

// TestPaired compares the v2 binary with the legacy binary on every
// scenario that has a v2 command line, and runs the scenario's own checks
// on the v2 trace. Set REFRESH_PARITY_V2 to the v2 binary. Until commands
// exist no scenario has a v2 command line.
func TestPaired(t *testing.T) {
	t.Parallel()
	v2 := Runner{Binary: os.Getenv("REFRESH_PARITY_V2")}
	if v2.Binary == "" {
		t.Skip("REFRESH_PARITY_V2 is not set")
	}
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
			for _, problem := range pair(t, sc, v2, sc.V2Preview, sc.V2, allow) {
				t.Error(problem)
			}
		})
	}
}
