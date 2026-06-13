package nodegroup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/health"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
)

// captureStdout is defined in health_decision_test.go (redirects both
// os.Stdout and color.Output).

// ──────────────────────────────────────────────────────────────────────────────
// outputNodegroupsTable
// ──────────────────────────────────────────────────────────────────────────────

func TestOutputNodegroupsTable_Empty(t *testing.T) {
	out := captureStdout(t, func() {
		if err := outputNodegroupsTable("my-cluster", nil, time.Second); err != nil {
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
		if err := outputNodegroupsTable("my-cluster", items, 2*time.Second); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	for _, want := range []string{"NODEGROUPS", "my-cluster"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q; got:\n%s", want, out)
		}
	}

	// Plain path keeps the original cluster banner.
	ui.SetPlainOutput(true)
	defer ui.SetPlainOutput(false)
	plain := captureStdout(t, func() {
		if err := outputNodegroupsTable("my-cluster", items, 2*time.Second); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	if !strings.Contains(plain, "Nodegroups for cluster: my-cluster") {
		t.Errorf("plain output missing cluster banner; got:\n%s", plain)
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

// ──────────────────────────────────────────────────────────────────────────────
// printScaleDownPDBImpact
// ──────────────────────────────────────────────────────────────────────────────

func TestPrintScaleDownPDBImpact_None(t *testing.T) {
	out := captureStdout(t, func() { printScaleDownPDBImpact(nil) })
	if !strings.Contains(out, "none found") {
		t.Errorf("expected a 'none found' message, got: %q", out)
	}
}

func TestPrintScaleDownPDBImpact_AllHealthy(t *testing.T) {
	pdbs := []health.PDBInfo{
		{Namespace: "app", Name: "web", DisruptionsAllowed: 2},
		{Namespace: "app", Name: "api", DisruptionsAllowed: 1},
	}
	out := captureStdout(t, func() { printScaleDownPDBImpact(pdbs) })
	if !strings.Contains(out, "none should block") {
		t.Errorf("all-healthy PDBs should report nothing blocks, got: %q", out)
	}
}

func TestPrintScaleDownPDBImpact_AtRisk(t *testing.T) {
	pdbs := []health.PDBInfo{
		{Namespace: "app", Name: "web", DisruptionsAllowed: 0, CurrentHealthy: 1, DesiredHealthy: 1},
		{Namespace: "app", Name: "api", DisruptionsAllowed: 3},
	}
	out := captureStdout(t, func() { printScaleDownPDBImpact(pdbs) })
	if !strings.Contains(out, "at risk") {
		t.Errorf("at-risk PDBs should be flagged, got: %q", out)
	}
	if !strings.Contains(out, "app/web") {
		t.Errorf("the at-risk PDB should be named, got: %q", out)
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
