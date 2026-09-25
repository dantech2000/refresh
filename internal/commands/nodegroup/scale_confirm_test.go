package nodegroup

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// withScalePrompt sets whether stdin looks like a terminal and what the
// confirmation prompt answers. It counts the prompts asked.
func withScalePrompt(t *testing.T, tty bool, answer string) *int {
	t.Helper()
	asked := new(int)
	origTTY, origPrompt := runner.StdinIsTerminal, runner.PromptLine
	runner.StdinIsTerminal = func() bool { return tty }
	runner.PromptLine = func(context.Context) (string, error) { *asked++; return answer, nil }
	t.Cleanup(func() { runner.StdinIsTerminal, runner.PromptLine = origTTY, origPrompt })
	return asked
}

// nodegroup scale asks before it changes a size (REF-164): the prompt names
// the cluster, the nodegroup, and each requested bound, and a "no" leaves
// the nodegroup alone. --yes skips the prompt; without a terminal --yes is
// required, checked before any AWS call; --dry-run never prompts.
func TestScale_Confirmation(t *testing.T) {
	cases := []struct {
		name      string
		tty       bool
		answer    string
		args      []string
		wantErr   string
		wantAsked int
		wantScale bool
		wantNoAWS bool
	}{
		{name: "tty yes", tty: true, answer: "y", wantAsked: 1, wantScale: true},
		{name: "tty no", tty: true, answer: "n", wantAsked: 1, wantErr: "cancelled"},
		{name: "tty bare enter", tty: true, answer: "", wantAsked: 1, wantErr: "cancelled"},
		{name: "--yes skips the prompt", tty: true, args: []string{"--yes"}, wantScale: true},
		{name: "-y skips the prompt", tty: false, args: []string{"-y"}, wantScale: true},
		{name: "no tty needs --yes", tty: false, wantErr: "add --yes to proceed", wantNoAWS: true},
		{name: "dry run never prompts", tty: false, args: []string{"-d"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asked := withScalePrompt(t, tc.tty, tc.answer)
			srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "ng-a", Version: "1.31", Desired: 3, Min: 1, Max: 5}))
			args := append([]string{"scale", "prod", "-n", "ng-a", "--desired", "1"}, tc.args...)
			_, stderr, err := runNodegroup(t, args...)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q\nstderr:\n%s", err, tc.wantErr, stderr)
				}
			} else if err != nil {
				t.Fatalf("scale: %v\nstderr:\n%s", err, stderr)
			}
			if *asked != tc.wantAsked {
				t.Errorf("prompts = %d, want %d", *asked, tc.wantAsked)
			}
			if tc.wantAsked > 0 && !strings.Contains(stderr, "Scale prod/ng-a desired 3 → 1? [y/N]") {
				t.Errorf("stderr = %q, want the scale prompt", stderr)
			}
			if got := calledPath(srv, "/update-config"); got != tc.wantScale {
				t.Errorf("UpdateNodegroupConfig called = %v, want %v", got, tc.wantScale)
			}
			if tc.wantNoAWS && len(srv.Calls()) != 0 {
				t.Errorf("AWS called before the --yes check: %v", srv.Calls())
			}
		})
	}
}

// A --min/--max that excludes the current desired size fails the same way
// with --dry-run as without it, and before the confirmation prompt.
func TestScale_BoundsCheckInDryRunAndBeforePrompt(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"dry run --max", []string{"--max", "1", "--dry-run"}, "--max 1 is below the current desired size 3"},
		{"dry run --min", []string{"--min", "4", "-d"}, "--min 4 is above the current desired size 3"},
		{"before the prompt", []string{"--max", "1"}, "--max 1 is below the current desired size 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := withScalePrompt(t, true, "y")
			srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "ng-a", Version: "1.31", Desired: 3, Min: 1, Max: 5}))
			stdout, _, err := runNodegroup(t, append([]string{"scale", "prod", "-n", "ng-a"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if strings.Contains(stdout, "DRY RUN") {
				t.Errorf("preview printed despite the bounds error:\n%s", stdout)
			}
			if *asked != 0 {
				t.Errorf("prompts = %d, want 0", *asked)
			}
			if calledPath(srv, "/update-config") {
				t.Error("UpdateNodegroupConfig called")
			}
		})
	}
}

// Ordering with --check-pdbs: the prompt comes first, then the gate. A "no"
// stops with nothing checked or changed; a "yes" (or --yes) reaches the gate,
// which fails closed without Kubernetes access. (A refused scale-down exits
// 3; see scale_exit_test.go.)
func TestScale_PromptThenPDBGate(t *testing.T) {
	t.Setenv("KUBECONFIG", t.TempDir()+"/none")
	for _, tc := range []struct {
		name    string
		answer  string
		args    []string
		wantErr string
	}{
		{"declined", "n", nil, "cancelled"},
		{"accepted", "y", nil, "the PodDisruptionBudgets could not be checked"},
		{"--yes", "", []string{"--yes"}, "the PodDisruptionBudgets could not be checked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withScalePrompt(t, true, tc.answer)
			srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "ng-a", Version: "1.31", Desired: 3, Min: 1, Max: 5}))
			args := append([]string{"scale", "prod", "-n", "ng-a", "--desired", "1", "--check-pdbs"}, tc.args...)
			stdout, stderr, err := runNodegroup(t, args...)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q\nstderr:\n%s", err, tc.wantErr, stderr)
			}
			if tc.answer != "n" {
				requireTableFailure(t, stdout, stderr, "cluster prod (us-east-1): Unknown: listing PodDisruptionBudgets: no Kubernetes client configured")
			}
			if calledPath(srv, "/update-config") {
				t.Error("UpdateNodegroupConfig called")
			}
		})
	}
}

func TestFormatScaleQuestion(t *testing.T) {
	sc := ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(3), MinSize: aws.Int32(1), MaxSize: aws.Int32(5)}
	for _, tc := range []struct {
		desired, minSize, maxSize *int32
		want                      string
	}{
		{aws.Int32(1), nil, nil, "Scale prod/ng-a desired 3 → 1?"},
		{aws.Int32(6), nil, aws.Int32(8), "Scale prod/ng-a desired 3 → 6, max 5 → 8?"},
		{nil, aws.Int32(0), nil, "Scale prod/ng-a min 1 → 0?"},
		{nil, nil, nil, "Scale prod/ng-a (no size change requested)?"},
	} {
		if got := formatScaleQuestion("prod", "ng-a", sc, tc.desired, tc.minSize, tc.maxSize); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

// A --check-pdbs dry run whose PDBs can't be read shows the preview, then
// fails (exit 1) as the real run would; with --force it exits 4: the scale
// would go ahead, but the PDBs could not be checked.
func TestScale_DryRunPDBGateExit(t *testing.T) {
	t.Setenv("KUBECONFIG", t.TempDir()+"/none")
	for _, tc := range []struct {
		name     string
		args     []string
		wantCode int
	}{
		{"unreadable PDBs fail", nil, runner.ExitError},
		{"--force is incomplete", []string{"--force"}, runner.ExitIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withScalePrompt(t, false, "")
			srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "ng-a", Version: "1.31", Desired: 3, Min: 1, Max: 5}))
			args := append([]string{"scale", "prod", "-n", "ng-a", "--desired", "1", "--check-pdbs", "--dry-run"}, tc.args...)
			stdout, stderr, err := runNodegroup(t, args...)
			if got := runner.ExitCodeOf(err); got != tc.wantCode {
				t.Fatalf("exit code = %d (err %v), want %d\nstderr:\n%s", got, err, tc.wantCode, stderr)
			}
			if tc.wantCode == runner.ExitError && !strings.Contains(err.Error(), "the PodDisruptionBudgets could not be checked") {
				t.Errorf("err = %v, want the PDB gate error", err)
			}
			requireTableFailure(t, stdout, stderr, "cluster prod (us-east-1): Unknown: listing PodDisruptionBudgets: no Kubernetes client configured")
			if !strings.Contains(stdout, "No changes were made") {
				t.Errorf("stdout missing the preview:\n%s", stdout)
			}
			if calledPath(srv, "/update-config") {
				t.Error("UpdateNodegroupConfig called")
			}
		})
	}
}

// A scale with no --desired, --min, or --max is a usage error before any AWS
// call: UpdateNodegroupConfig would otherwise start an update that changes
// nothing.
func TestScale_NoSizeIsAUsageError(t *testing.T) {
	for _, extra := range [][]string{{"--yes"}, {"--dry-run"}, nil} {
		withScalePrompt(t, false, "")
		srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "ng-a", Version: "1.31", Desired: 3, Min: 1, Max: 5}))
		_, _, err := runNodegroup(t, append([]string{"scale", "prod", "-n", "ng-a"}, extra...)...)
		if err == nil || !strings.Contains(err.Error(), "needs at least one of --desired, --min, or --max") {
			t.Errorf("%v: err = %v, want the missing-size error", extra, err)
		}
		if calls := srv.Calls(); len(calls) != 0 {
			t.Errorf("%v: AWS called: %v", extra, calls)
		}
	}
}

// --wait polls EKS only. Without --health-check or --check-pdbs it needs no
// Kubernetes client, so an unreachable cluster API prints no "checks will be
// skipped" note.
func TestScale_WaitAloneNeedsNoKubeClient(t *testing.T) {
	t.Setenv("KUBECONFIG", t.TempDir()+"/none")
	withScalePrompt(t, false, "")
	srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "ng-a", Version: "1.31", Desired: 3, Min: 1, Max: 5}))
	_, stderr, err := runNodegroup(t, "scale", "prod", "-n", "ng-a", "--desired", "4", "--yes", "--wait")
	if err != nil {
		t.Fatalf("scale --wait: %v\nstderr:\n%s", err, stderr)
	}
	if strings.Contains(stderr, "will be skipped") {
		t.Errorf("stderr has a misleading skip note:\n%s", stderr)
	}
	if !calledPath(srv, "/update-config") {
		t.Error("UpdateNodegroupConfig not called")
	}
}

// Health-check warnings are printed on stderr after the spinner, one block
// per stage, not as log lines under it.
func TestScaleHealthWarnings_Print(t *testing.T) {
	var h scaleHealthWarnings
	h.add("pre-scaling", []string{"node a: DiskPressure"})
	h.add("post-scaling", []string{"pod b pending"})
	var buf strings.Builder
	h.print(&buf)
	// Each block opens with a warning token (its glyph depends on the
	// locale: ▲ or [!]).
	got := buf.String()
	for _, want := range []string{
		" The pre-scaling health check reported warnings:\n  - node a: DiskPressure\n",
		" The post-scaling health check reported warnings:\n  - pod b pending\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("printed:\n%q\nwant it to contain:\n%q", got, want)
		}
	}
	if n := strings.Count(got, "\n"); n != 4 {
		t.Errorf("printed %d lines, want 4:\n%s", n, got)
	}
	var empty scaleHealthWarnings
	buf.Reset()
	empty.print(&buf)
	if buf.Len() != 0 {
		t.Errorf("no warnings printed %q", buf.String())
	}
}
