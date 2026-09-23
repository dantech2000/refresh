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
