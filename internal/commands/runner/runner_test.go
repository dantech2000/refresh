package runner

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/urfave/cli/v3"
)

// ── setupAWS context propagation ──────────────────────────────────────────────

// newTimeoutCommand returns a parsed *cli.Command carrying a timeout flag,
// mirroring the root command's global --timeout.
func newTimeoutCommand(t *testing.T) *cli.Command {
	t.Helper()
	var captured *cli.Command
	cmd := &cli.Command{
		Name:  "test",
		Flags: []cli.Flag{&cli.DurationFlag{Name: "timeout", Value: time.Minute}},
		Action: func(_ context.Context, c *cli.Command) error {
			captured = c
			return nil
		},
	}
	if err := cmd.Run(context.Background(), []string{"test"}); err != nil {
		t.Fatal(err)
	}
	return captured
}

// Regression: setupAWS must derive its context from the action's context
// (cancelled on Ctrl+C / SIGTERM by main) rather than context.Background(),
// so signal cancellation propagates to in-flight AWS calls.
func TestSetupAWS_DerivesFromParentContext(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cmd := newTimeoutCommand(t)

	var got context.Context
	_, cancel, _, err := setupAWS(parent, cmd, 0, func(ctx context.Context, _ aws.Config) error {
		got = ctx
		return nil
	})
	if err != nil {
		t.Fatalf("setupAWS() = %v", err)
	}
	defer cancel()

	select {
	case <-got.Done():
		t.Fatal("context cancelled prematurely")
	default:
	}
	cancelParent()
	select {
	case <-got.Done():
		// parent cancellation propagated — correct
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not propagate to setupAWS context")
	}
}

// setupAWS must tolerate a nil context (hand-constructed invocations in tests).
func TestSetupAWS_NilContextFallsBack(t *testing.T) {
	cmd := newTimeoutCommand(t)

	//nolint:staticcheck // nil context is the case under test
	_, cancel, _, err := setupAWS(nil, cmd, 0, func(context.Context, aws.Config) error { return nil })
	if err != nil {
		t.Fatalf("setupAWS() = %v", err)
	}
	cancel()
}

// newTestCommand builds a parsed *cli.Command with the given string flags
// registered and args parsed. flags is name→value; "" value means the flag is
// registered but not explicitly set. Set flags are passed as --name=value
// tokens before the positionals (v3 parses flags anywhere, so placement is
// irrelevant).
func newTestCommand(t *testing.T, args []string, flags map[string]string) *cli.Command {
	t.Helper()
	var captured *cli.Command
	cmd := &cli.Command{
		Name: "test",
		Action: func(_ context.Context, c *cli.Command) error {
			captured = c
			return nil
		},
	}
	argv := []string{"test"}
	for name, value := range flags {
		cmd.Flags = append(cmd.Flags, &cli.StringFlag{Name: name})
		if value != "" {
			argv = append(argv, "--"+name+"="+value)
		}
	}
	argv = append(argv, args...)
	if err := cmd.Run(context.Background(), argv); err != nil {
		t.Fatal(err)
	}
	return captured
}

// ── PositionalSlot ────────────────────────────────────────────────────────────

// Regression for the addon update bug: `--addon=foo my-cluster v1.2.3` must
// resolve version to "v1.2.3", not "" (which would silently default to
// "latest"). The version slot is third positional after (cluster, addon).
// With --addon set, the positional index shifts from 2 down to 1.
func TestPositionalSlot_FlagShiftsLaterPositionals(t *testing.T) {
	cmd := newTestCommand(t,
		[]string{"my-cluster", "v1.2.3"},
		map[string]string{"cluster": "", "addon": "foo", "version": ""},
	)
	if got := PositionalSlot(cmd, "version", "cluster", "addon"); got != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3 (positional shifted because --addon set)", got)
	}
}

func TestPositionalSlot_AllPositional(t *testing.T) {
	cmd := newTestCommand(t,
		[]string{"my-cluster", "vpc-cni", "v1.2.3"},
		map[string]string{"cluster": "", "addon": "", "version": ""},
	)
	if got := PositionalSlot(cmd, "version", "cluster", "addon"); got != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3", got)
	}
	if got := PositionalSlot(cmd, "addon", "cluster"); got != "vpc-cni" {
		t.Errorf("addon = %q, want vpc-cni", got)
	}
	if got := PositionalSlot(cmd, "cluster"); got != "my-cluster" {
		t.Errorf("cluster = %q, want my-cluster", got)
	}
}

func TestPositionalSlot_FlagWinsWhenSet(t *testing.T) {
	cmd := newTestCommand(t,
		[]string{"my-cluster", "vpc-cni"},
		map[string]string{"cluster": "", "addon": "", "version": "v9.9.9"},
	)
	if got := PositionalSlot(cmd, "version", "cluster", "addon"); got != "v9.9.9" {
		t.Errorf("version = %q, want v9.9.9 (from flag)", got)
	}
}

func TestPositionalSlot_AllFlagsLeavesNoPositional(t *testing.T) {
	cmd := newTestCommand(t,
		nil,
		map[string]string{"cluster": "my-cluster", "addon": "vpc-cni", "version": "v1.2.3"},
	)
	if got := PositionalSlot(cmd, "version", "cluster", "addon"); got != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3", got)
	}
}

func TestPositionalSlot_MissingSlotReturnsEmpty(t *testing.T) {
	cmd := newTestCommand(t,
		[]string{"my-cluster"},
		map[string]string{"cluster": "", "addon": "", "version": ""},
	)
	if got := PositionalSlot(cmd, "version", "cluster", "addon"); got != "" {
		t.Errorf("version = %q, want empty (no positional, no flag)", got)
	}
}

// v3 parses flags after positionals natively — the old trailing-flag replay
// machinery is gone, so verify the framework keeps this working end to end.
func TestPositionalSlot_TrailingFlagParsedNatively(t *testing.T) {
	cmd := newTestCommand(t,
		[]string{"my-cluster", "--version=v2.0.0"},
		map[string]string{"cluster": "", "addon": "", "version": ""},
	)
	if got := PositionalSlot(cmd, "cluster"); got != "my-cluster" {
		t.Errorf("cluster = %q, want my-cluster", got)
	}
	if got := PositionalSlot(cmd, "version", "cluster", "addon"); got != "v2.0.0" {
		t.Errorf("version = %q, want v2.0.0 (trailing flag parsed by v3)", got)
	}
}

// ── PositionalAt (legacy) ─────────────────────────────────────────────────────

func TestPositionalAt_FlagWins(t *testing.T) {
	cmd := newTestCommand(t,
		[]string{"alpha", "beta"},
		map[string]string{"thing": "from-flag"},
	)
	if got := PositionalAt(cmd, "thing", 1); got != "from-flag" {
		t.Errorf("got %q, want from-flag", got)
	}
}

func TestPositionalAt_FallsBackToPositional(t *testing.T) {
	cmd := newTestCommand(t,
		[]string{"alpha", "beta"},
		map[string]string{"thing": ""},
	)
	if got := PositionalAt(cmd, "thing", 1); got != "beta" {
		t.Errorf("got %q, want beta", got)
	}
}

func TestPositionalAt_OutOfRangeReturnsEmpty(t *testing.T) {
	cmd := newTestCommand(t,
		[]string{"alpha"},
		map[string]string{"thing": ""},
	)
	if got := PositionalAt(cmd, "thing", 1); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// ── ParseFilters ──────────────────────────────────────────────────────────────

func TestParseFilters(t *testing.T) {
	got := ParseFilters([]string{"name=prod", "env=staging", "malformed", "k=v=extra"})
	if len(got) != 3 {
		t.Fatalf("ParseFilters returned %d entries, want 3: %v", len(got), got)
	}
	if got["name"] != "prod" || got["env"] != "staging" {
		t.Errorf("ParseFilters = %v", got)
	}
	// SplitN(2) keeps everything after the first "=" as the value.
	if got["k"] != "v=extra" {
		t.Errorf(`got["k"] = %q, want "v=extra"`, got["k"])
	}
}

func TestParseFilters_Empty(t *testing.T) {
	if got := ParseFilters(nil); len(got) != 0 {
		t.Errorf("ParseFilters(nil) = %v, want empty map", got)
	}
}
