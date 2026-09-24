package nodegroup

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/health"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
	"github.com/dantech2000/refresh/internal/ui/plaintest"
)

// captureStdout is defined in health_decision_test.go (redirects both
// os.Stdout and color.Output).

// ──────────────────────────────────────────────────────────────────────────────
// outputNodegroupsTable
// ──────────────────────────────────────────────────────────────────────────────

func TestOutputNodegroupsTable_Empty(t *testing.T) {
	out := captureStdout(t, func() {
		if err := outputNodegroupsTable("my-cluster", nil, nil); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	if !strings.Contains(out, "No nodegroups found") {
		t.Errorf("empty output should note no nodegroups, got: %q", out)
	}
}

func TestOutputNodegroupsTable_WithRows(t *testing.T) {
	items := []nodegroupsvc.NodegroupSummary{
		{Name: "workers", Status: "ACTIVE", InstanceType: "m5.large", AMIStatus: types.AMILatest, ReadyNodes: 3, DesiredSize: 3},
		{Name: "spot", Status: "UPDATING", InstanceType: "t3.medium", AMIStatus: types.AMIOutdated, ReadyNodes: 1, DesiredSize: 2},
	}
	// Human path (render design system): captured in full via fmt.Println.
	out := captureStdout(t, func() {
		if err := outputNodegroupsTable("my-cluster", items, nil); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	for _, want := range []string{"NODEGROUPS", "my-cluster"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q; got:\n%s", want, out)
		}
	}

	// Plain path: header + one TSV row per nodegroup, nothing else. The header
	// names match the human table's columns.
	ui.SetPlainOutput(true)
	defer ui.SetPlainOutput(false)
	plain := captureStdout(t, func() {
		if err := outputNodegroupsTable("my-cluster", items, nil); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	headers := []string{"NAME", "STATUS", "INSTANCE", "VERSION", "AMI", "NODES"}
	rows := plaintest.Check(t, plain, headers...)
	if len(rows) != len(items) {
		t.Fatalf("got %d rows, want %d:\n%s", len(rows), len(items), plain)
	}
	if got := strings.Join(rows[1], "|"); got != "spot|UPDATING|t3.medium|-|Outdated|2" {
		t.Errorf("row = %q", got)
	}
	for _, h := range headers {
		if !strings.Contains(out, h) {
			t.Errorf("human table has no %q column; plain header must match it:\n%s", h, out)
		}
	}
}

func TestOutputNodegroupsTable_PlainEmptyIsHeaderOnly(t *testing.T) {
	ui.SetPlainOutput(true)
	defer ui.SetPlainOutput(false)
	out := captureStdout(t, func() {
		if err := outputNodegroupsTable("my-cluster", nil, nil); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	if out != "NAME\tSTATUS\tINSTANCE\tVERSION\tAMI\tNODES\n" {
		t.Errorf("empty plain list should be the header only, got %q", out)
	}
}

func TestNodegroupListPlain_BehindAndLookupFailure(t *testing.T) {
	items := []nodegroupsvc.NodegroupSummary{
		{Name: "old\tname", Status: "ACTIVE", K8sVersion: "1.29", VersionBehind: true, AMILookupFailure: amiLookupFailure("old\tname"), DesiredSize: 2, ReadyNodes: 1, ReadyKnown: true},
	}
	var buf bytes.Buffer
	nodegroupListPlain(items).Write(&buf)
	rows := plaintest.Check(t, buf.String(), "NAME", "STATUS", "INSTANCE", "VERSION", "AMI", "NODES")
	if got := strings.Join(rows[0], "|"); got != "old name|ACTIVE|-|1.29 (behind)|unknown (lookup failed)|1/2" {
		t.Errorf("row = %q", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// outputNodegroupDetailsTable
// ──────────────────────────────────────────────────────────────────────────────

func TestOutputNodegroupDetailsTable(t *testing.T) {
	details := &nodegroupsvc.NodegroupDetails{
		Name:         "workers",
		Status:       "ACTIVE",
		InstanceType: "m5.large",
		AmiType:      "AL2_x86_64",
		CapacityType: "ON_DEMAND",
		CurrentAMI:   "ami-aaa",
		LatestAMI:    "ami-bbb",
		AMIStatus:    types.AMIOutdated,
		Scaling:      nodegroupsvc.ScalingConfig{DesiredSize: 3, MinSize: 1, MaxSize: 5},
		Workloads:    nodegroupsvc.WorkloadInfo{TotalPods: 10, CriticalPods: 2, PodDisruption: "2 PDBs"},
	}
	out := captureStdout(t, func() {
		if err := outputNodegroupDetailsTable(details, time.Second); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	for _, want := range []string{"workers", "m5.large", "ami-aaa", "ami-bbb", "Workloads", "2 PDBs"} {
		if !strings.Contains(out, want) {
			t.Errorf("details output missing %q; got:\n%s", want, out)
		}
	}
}

func TestOutputNodegroupDetailsTable_Plain(t *testing.T) {
	details := &nodegroupsvc.NodegroupDetails{
		Name:         "workers",
		Status:       "ACTIVE",
		InstanceType: "m5.large",
		AmiType:      "AL2_x86_64",
		CapacityType: "ON_DEMAND",
		CurrentAMI:   "ami-aaa",
		LatestAMI:    "ami-bbb",
		AMIStatus:    types.AMIOutdated,
		Scaling:      nodegroupsvc.ScalingConfig{DesiredSize: 3, MinSize: 1, MaxSize: 5},
		Workloads:    nodegroupsvc.WorkloadInfo{TotalPods: 10, CriticalPods: 2, PodDisruption: "2 PDBs"},
		Instances: []nodegroupsvc.InstanceDetails{{
			InstanceID: "i-0123456789abcdef0123", InstanceType: "m5.large",
			LaunchTime: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Lifecycle: "on-demand", State: "running", AZ: "us-east-1a",
		}},
	}
	ui.SetPlainOutput(true)
	defer ui.SetPlainOutput(false)
	out := captureStdout(t, func() {
		if err := outputNodegroupDetailsTable(details, time.Second); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	rows := plaintest.Check(t, out, "FIELD", "VALUE")
	for f, v := range map[string]string{
		"name":       "workers",
		"ami status": "Outdated",
		"scaling":    "3 desired (1-5)",
		"pdbs":       "2 PDBs",
		// The human table truncates instance IDs; plain never does.
		"instance/i-0123456789abcdef0123": "type=m5.large launched=2026-01-02 lifecycle=on-demand state=running az=us-east-1a",
	} {
		if got, ok := plaintest.Field(rows, f); !ok || got != v {
			t.Errorf("field %q = %q (found=%v), want %q", f, got, ok, v)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// printScaleDryRunPDBGate / warnForcedScaleDown
// ──────────────────────────────────────────────────────────────────────────────

func blockedCheck(scoped bool) *nodegroupsvc.ScaleDownPDBCheck {
	return &nodegroupsvc.ScaleDownPDBCheck{
		CurrentDesired: 3, RequestedDesired: 1, ScaleDown: true, Scoped: scoped,
		Blockers: []health.PDBInfo{
			{Namespace: "app", Name: "web", CurrentHealthy: 1, DesiredHealthy: 1, ExpectedPods: 1},
			{Namespace: "app", Name: "operator", StatusNotSynced: true},
		},
	}
}

func TestPrintScaleDryRunPDBGate(t *testing.T) {
	cases := map[string]struct {
		check    *nodegroupsvc.ScaleDownPDBCheck
		checkErr error
		force    bool
		want     []string
		notWant  []string
	}{
		"not a scale-down": {
			check: &nodegroupsvc.ScaleDownPDBCheck{CurrentDesired: 2, RequestedDesired: 4},
			want:  []string{"not a scale-down"},
		},
		"no blockers": {
			check:   &nodegroupsvc.ScaleDownPDBCheck{CurrentDesired: 3, RequestedDesired: 1, ScaleDown: true, Scoped: true},
			want:    []string{"no PodDisruptionBudget blocks removing nodes from ng"},
			notWant: []string{"REFUSED"},
		},
		"blocked": {
			check: blockedCheck(true),
			want: []string{"would be REFUSED", "2 PodDisruptionBudget(s) with pods on this nodegroup's nodes",
				"app/web (1/1 pods healthy", "app/operator (PDB status not synced", "--force"},
		},
		"blocked, unscoped": {
			check: blockedCheck(false),
			want:  []string{"would be REFUSED", "could not scope"},
		},
		"blocked, forced": {
			check:   blockedCheck(true),
			force:   true,
			want:    []string{"would be overridden by --force", "app/web"},
			notWant: []string{"REFUSED"},
		},
		"check failed": {
			checkErr: errors.New("no Kubernetes client configured"),
			want:     []string{"would be REFUSED", "no Kubernetes client configured"},
		},
		"check failed, forced": {
			checkErr: errors.New("no Kubernetes client configured"),
			force:    true,
			want:     []string{"--force would scale anyway"},
			notWant:  []string{"REFUSED"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printScaleDryRunPDBGate(&buf, "prod", "ng", tc.check, tc.checkErr, tc.force)
			out := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q:\n%s", w, out)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(out, w) {
					t.Errorf("output should not contain %q:\n%s", w, out)
				}
			}
		})
	}
}

func TestWarnForcedScaleDown(t *testing.T) {
	var buf bytes.Buffer
	warnForcedScaleDown(&buf, "prod", "ng", blockedCheck(true), nil)
	out := buf.String()
	for _, w := range []string{"--force", "prod/ng down from 3 to 1", "2 PodDisruptionBudget(s)", "app/web", "app/operator"} {
		if !strings.Contains(out, w) {
			t.Errorf("warning missing %q:\n%s", w, out)
		}
	}

	buf.Reset()
	warnForcedScaleDown(&buf, "prod", "ng", &nodegroupsvc.ScaleDownPDBCheck{CurrentDesired: 3, RequestedDesired: 1, ScaleDown: true}, nil)
	if buf.Len() != 0 {
		t.Errorf("no blockers should print nothing, got %q", buf.String())
	}

	buf.Reset()
	warnForcedScaleDown(&buf, "prod", "ng", nil, errors.New("listing PodDisruptionBudgets: forbidden"))
	if !strings.Contains(buf.String(), "could not validate PodDisruptionBudgets") || !strings.Contains(buf.String(), "forbidden") {
		t.Errorf("a failed check should be reported, got %q", buf.String())
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// printVerification + PostRollVerification.OK
// ──────────────────────────────────────────────────────────────────────────────

func TestPostRollVerification_OK(t *testing.T) {
	if !(PostRollVerification{Checks: []string{"ok"}}).OK() {
		t.Error("no issues should be OK")
	}
	if (PostRollVerification{Issues: []string{"boom"}}).OK() {
		t.Error("issues present should not be OK")
	}
}

func TestPrintVerification_Passed(t *testing.T) {
	v := PostRollVerification{Checks: []string{"nodegroup workers is ACTIVE", "no new Pending pods"}}
	out := captureStdout(t, func() { printVerification(v) })
	if !strings.Contains(out, "passed") {
		t.Errorf("expected a passed banner, got: %q", out)
	}
	if !strings.Contains(out, "no new Pending pods") {
		t.Errorf("expected checks listed, got: %q", out)
	}
}

func TestPrintVerification_Issues(t *testing.T) {
	v := PostRollVerification{
		Checks: []string{"nodegroup workers is ACTIVE"},
		Issues: []string{"2 pod(s) newly Pending after roll"},
	}
	out := captureStdout(t, func() { printVerification(v) })
	if !strings.Contains(out, "found issues") {
		t.Errorf("expected an issues banner, got: %q", out)
	}
	if !strings.Contains(out, "newly Pending") {
		t.Errorf("expected the issue listed, got: %q", out)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// printChangelog + orDash
// ──────────────────────────────────────────────────────────────────────────────

func TestOrDash(t *testing.T) {
	cases := map[string]string{"": "-", "   ": "-", "v1": "v1"}
	for in, want := range cases {
		if got := orDash(in); got != want {
			t.Errorf("orDash(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrintChangelog_Degraded(t *testing.T) {
	cl := amiChangelog{Current: "ami-1", Target: "ami-2", Degraded: true, Reason: "could not parse release dates"}
	out := captureStdout(t, func() { printChangelog(cl, false) })
	if !strings.Contains(out, "release notes unavailable") {
		t.Errorf("degraded changelog should say notes unavailable, got: %q", out)
	}
}

func TestPrintChangelog_TruncatesWithoutFull(t *testing.T) {
	cl := amiChangelog{
		Current: "ami-1",
		Target:  "ami-9",
		Behind:  4,
		Notes: []releaseNote{
			{Tag: "v1", Highlights: []string{"a"}},
			{Tag: "v2"},
			{Tag: "v3"},
			{Tag: "v4"},
		},
	}
	out := captureStdout(t, func() { printChangelog(cl, false) })
	if !strings.Contains(out, "release(s) behind") {
		t.Errorf("expected behind count, got: %q", out)
	}
	if !strings.Contains(out, "more release(s)") {
		t.Errorf("non-full changelog with >3 notes should truncate, got: %q", out)
	}
}

func TestPrintChangelog_FullShowsAll(t *testing.T) {
	cl := amiChangelog{
		Current: "ami-1",
		Target:  "ami-9",
		Notes: []releaseNote{
			{Tag: "v1"}, {Tag: "v2"}, {Tag: "v3"}, {Tag: "v4"},
		},
	}
	out := captureStdout(t, func() { printChangelog(cl, true) })
	if strings.Contains(out, "more release(s)") {
		t.Errorf("full changelog should not truncate, got: %q", out)
	}
	if !strings.Contains(out, "v4") {
		t.Errorf("full changelog should list every release, got: %q", out)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// int32PtrIfSet
// ──────────────────────────────────────────────────────────────────────────────

func TestInt32PtrIfSet(t *testing.T) {
	var got *int32
	var gotErr error
	cmd := &cli.Command{
		Name:  "test",
		Flags: []cli.Flag{&cli.IntFlag{Name: "desired"}},
		Action: func(_ context.Context, c *cli.Command) error {
			got, gotErr = int32PtrIfSet(c, "desired")
			return nil
		},
	}
	if err := cmd.Run(context.Background(), []string{"test", "--desired", "7"}); err != nil {
		t.Fatal(err)
	}
	if gotErr != nil {
		t.Fatalf("set flag: unexpected error %v", gotErr)
	}
	if got == nil || *got != 7 {
		t.Errorf("set flag: got %v, want 7", got)
	}

	got = nil
	cmd2 := &cli.Command{
		Name:  "test",
		Flags: []cli.Flag{&cli.IntFlag{Name: "desired"}},
		Action: func(_ context.Context, c *cli.Command) error {
			got, gotErr = int32PtrIfSet(c, "desired")
			return nil
		},
	}
	if err := cmd2.Run(context.Background(), []string{"test"}); err != nil {
		t.Fatal(err)
	}
	if gotErr != nil {
		t.Fatalf("unset flag: unexpected error %v", gotErr)
	}
	if got != nil {
		t.Errorf("unset flag: got %v, want nil", got)
	}

	// Out-of-range value must error instead of silently wrapping to int32.
	got = nil
	gotErr = nil
	cmd3 := &cli.Command{
		Name:  "test",
		Flags: []cli.Flag{&cli.IntFlag{Name: "desired"}},
		Action: func(_ context.Context, c *cli.Command) error {
			got, gotErr = int32PtrIfSet(c, "desired")
			return nil
		},
	}
	if err := cmd3.Run(context.Background(), []string{"test", "--desired", "3000000000"}); err != nil {
		t.Fatal(err)
	}
	if gotErr == nil {
		t.Errorf("out-of-range flag: expected an error, got nil (value %v)", got)
	}
	if got != nil {
		t.Errorf("out-of-range flag: got %v, want nil", got)
	}
}
