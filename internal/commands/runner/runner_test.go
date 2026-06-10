package runner

import (
	"context"
	"flag"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/urfave/cli/v2"
)

// newTestContext builds a *cli.Context with the given flags pre-registered and
// args parsed. flags is name→value; "" value means the flag is registered but
// not explicitly set.
func newTestContext(t *testing.T, args []string, flags map[string]string) *cli.Context {
	t.Helper()
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	for name := range flags {
		set.String(name, "", "")
	}
	for name, value := range flags {
		if value != "" {
			if err := set.Set(name, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := set.Parse(args); err != nil {
		t.Fatal(err)
	}
	return cli.NewContext(cli.NewApp(), set, nil)
}

// ── setupAWS context propagation ──────────────────────────────────────────────

// Regression: setupAWS must derive its context from c.Context (cancelled on
// Ctrl+C / SIGTERM by main) rather than context.Background(), so signal
// cancellation propagates to in-flight AWS calls.
func TestSetupAWS_DerivesFromCLIContext(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	set.Duration("timeout", time.Minute, "")
	c := cli.NewContext(cli.NewApp(), set, nil)
	c.Context = parent

	var got context.Context
	_, cancel, _, err := setupAWS(c, 0, func(ctx context.Context, _ aws.Config) error {
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

// setupAWS must tolerate a nil c.Context (hand-constructed contexts in tests).
func TestSetupAWS_NilCLIContextFallsBack(t *testing.T) {
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	set.Duration("timeout", time.Minute, "")
	c := cli.NewContext(cli.NewApp(), set, nil)
	c.Context = nil

	_, cancel, _, err := setupAWS(c, 0, func(context.Context, aws.Config) error { return nil })
	if err != nil {
		t.Fatalf("setupAWS() = %v", err)
	}
	cancel()
}

// ── PositionalSlot ────────────────────────────────────────────────────────────

// Regression for the addon update bug: `--addon=foo my-cluster v1.2.3` must
// resolve version to "v1.2.3", not "" (which would silently default to
// "latest"). The version slot is third positional after (cluster, addon).
// With --addon set, the positional index shifts from 2 down to 1.
func TestPositionalSlot_FlagShiftsLaterPositionals(t *testing.T) {
	ctx := newTestContext(t,
		[]string{"my-cluster", "v1.2.3"},
		map[string]string{"cluster": "", "addon": "foo", "version": ""},
	)
	if got := PositionalSlot(ctx, "version", "cluster", "addon"); got != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3 (positional shifted because --addon set)", got)
	}
}

func TestPositionalSlot_AllPositional(t *testing.T) {
	ctx := newTestContext(t,
		[]string{"my-cluster", "vpc-cni", "v1.2.3"},
		map[string]string{"cluster": "", "addon": "", "version": ""},
	)
	if got := PositionalSlot(ctx, "version", "cluster", "addon"); got != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3", got)
	}
	if got := PositionalSlot(ctx, "addon", "cluster"); got != "vpc-cni" {
		t.Errorf("addon = %q, want vpc-cni", got)
	}
	if got := PositionalSlot(ctx, "cluster"); got != "my-cluster" {
		t.Errorf("cluster = %q, want my-cluster", got)
	}
}

func TestPositionalSlot_FlagWinsWhenSet(t *testing.T) {
	ctx := newTestContext(t,
		[]string{"my-cluster", "vpc-cni"},
		map[string]string{"cluster": "", "addon": "", "version": "v9.9.9"},
	)
	if got := PositionalSlot(ctx, "version", "cluster", "addon"); got != "v9.9.9" {
		t.Errorf("version = %q, want v9.9.9 (from flag)", got)
	}
}

func TestPositionalSlot_AllFlagsLeavesNoPositional(t *testing.T) {
	ctx := newTestContext(t,
		nil,
		map[string]string{"cluster": "my-cluster", "addon": "vpc-cni", "version": "v1.2.3"},
	)
	if got := PositionalSlot(ctx, "version", "cluster", "addon"); got != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3", got)
	}
}

func TestPositionalSlot_MissingSlotReturnsEmpty(t *testing.T) {
	ctx := newTestContext(t,
		[]string{"my-cluster"},
		map[string]string{"cluster": "", "addon": "", "version": ""},
	)
	if got := PositionalSlot(ctx, "version", "cluster", "addon"); got != "" {
		t.Errorf("version = %q, want empty (no positional, no flag)", got)
	}
}

// ── PositionalAt (legacy) ─────────────────────────────────────────────────────

func TestPositionalAt_FlagWins(t *testing.T) {
	ctx := newTestContext(t,
		[]string{"alpha", "beta"},
		map[string]string{"thing": "from-flag"},
	)
	if got := PositionalAt(ctx, "thing", 1); got != "from-flag" {
		t.Errorf("got %q, want from-flag", got)
	}
}

func TestPositionalAt_FallsBackToPositional(t *testing.T) {
	ctx := newTestContext(t,
		[]string{"alpha", "beta"},
		map[string]string{"thing": ""},
	)
	if got := PositionalAt(ctx, "thing", 1); got != "beta" {
		t.Errorf("got %q, want beta", got)
	}
}

func TestPositionalAt_OutOfRangeReturnsEmpty(t *testing.T) {
	ctx := newTestContext(t,
		[]string{"alpha"},
		map[string]string{"thing": ""},
	)
	if got := PositionalAt(ctx, "thing", 1); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
