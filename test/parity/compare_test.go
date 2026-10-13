//go:build unix

package parity

import (
	"context"
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
			v2.Steps = append(v2.Steps, Step{Mutation: &Mutation{Operation: "eks:UpdateAddon", Region: Region, Target: "prod/coredns", Outcome: "accepted update 3"}})
		}, []Field{FieldMutations, FieldPreconditions}},
		{"the roll started before the control plane was seen to finish", func(v2 *Trace) {
			v2.Steps[1].Preconditions = v2.Steps[1].Preconditions[:1]
		}, []Field{FieldPreconditions}},
		{"the roll started while the control plane was still updating", func(v2 *Trace) {
			v2.Steps[1].Preconditions[1] = "eks:DescribeUpdate prod: update 1 VersionUpdate InProgress"
		}, []Field{FieldPreconditions}},
		{"the nodegroup was not read before its roll", func(v2 *Trace) {
			v2.Steps[1].Preconditions = v2.Steps[1].Preconditions[1:]
		}, []Field{FieldPreconditions}},
		{"another final state", func(v2 *Trace) {
			v2.World[Region+"/prod"].Nodegroups["ng-a"] = "1.33 r2 DEGRADED 2/1/3 issues=NodeCreationFailure"
		}, []Field{FieldWorld}},
		{"another cluster touched", func(v2 *Trace) { v2.World[Region+"/staging"] = ClusterState{Cluster: "1.32 ACTIVE"} }, []Field{FieldWorld}},
		{"a refusal", func(v2 *Trace) {
			v2.Steps, v2.RefusedBeforeMutation, v2.ExitCode = nil, true, 3
			v2.World[Region+"/prod"] = ClusterState{Cluster: "1.32 ACTIVE"}
		}, []Field{FieldMutations, FieldPreconditions, FieldWorld, FieldRefused, FieldExitCode}},
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
	for _, a := range list {
		if !slices.Contains(names, a.Scenario) {
			t.Errorf("the allowlist names the scenario %q, which the suite does not have", a.Scenario)
		}
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

// TestPairedSelf pairs the legacy binary with itself on a few scenarios:
// two real runs of one binary must compare as equal, whatever the reads and
// the clock did in between.
func TestPairedSelf(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"addon-update-lost-response", "nodegroup-update-interrupt", "cluster-upgrade-yes", "cluster-rollback-blocked", "nodegroup-update-all-clusters"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var sc Scenario
			for _, s := range scenarios() {
				if s.Name == name {
					sc = s
				}
			}
			if sc.Name == "" {
				t.Fatalf("no scenario %q", name)
			}
			for _, d := range Compare(name, run(t, sc), run(t, sc), nil) {
				t.Errorf("two runs of the legacy binary differ:\n%s", d)
			}
		})
	}
}

// TestPaired compares the v2 binary with the legacy binary on every
// scenario that has a v2 command line. Set REFRESH_PARITY_V2 to the v2
// binary. Until commands exist no scenario has a v2 command line.
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
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			got, err := v2.Run(ctx, sc, sc.V2, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range Compare(sc.Name, run(t, sc), got, allow) {
				if d.Allowed {
					t.Log(d)
					continue
				}
				t.Error(d)
			}
		})
	}
}
