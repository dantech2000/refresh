package nodegroup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/monitoring"
)

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	if ec, ok := err.(cli.ExitCoder); ok {
		return ec.ExitCode()
	}
	return -1
}

func warnSummary() health.HealthSummary {
	return health.HealthSummary{Decision: health.DecisionWarn, Warnings: []string{"moderate cpu"}}
}

func TestApplyHealthDecision_RequireHealthyBlocksWarn(t *testing.T) {
	var done bool
	var err error
	captureStdout(t, func() {
		done, err = applyHealthDecision(warnSummary(), updateAMIFlags{requireHealthy: true})
	})
	if !done {
		t.Fatal("expected done=true")
	}
	if exitCodeOf(err) != 2 {
		t.Errorf("exit code = %d, want 2", exitCodeOf(err))
	}
}

func TestApplyHealthDecision_YesProceedsPastWarn(t *testing.T) {
	done, err := applyHealthDecision(warnSummary(), updateAMIFlags{yes: true})
	if done || err != nil {
		t.Errorf("--yes should proceed past warnings: done=%v err=%v", done, err)
	}
}

func TestApplyHealthDecision_WarnNoTTYFailsFast(t *testing.T) {
	// In `go test` stdin is not a terminal, so without --yes/--require-healthy a
	// warn-level result must fail fast rather than block on a prompt.
	done, err := applyHealthDecision(warnSummary(), updateAMIFlags{})
	if !done || err == nil {
		t.Fatalf("expected fail-fast: done=%v err=%v", done, err)
	}
}

func TestUpdateExit(t *testing.T) {
	if got := exitCodeOf(updateExit(updateOutcomes{}, nil, false)); got != 0 {
		t.Errorf("clean run exit = %d, want 0", got)
	}
	if got := exitCodeOf(updateExit(updateOutcomes{Failed: []string{"ng-a"}}, nil, false)); got != 4 {
		t.Errorf("start-failure exit = %d, want 4", got)
	}
	if got := exitCodeOf(updateExit(updateOutcomes{}, nil, true)); got != 5 {
		t.Errorf("verification-failure exit = %d, want 5", got)
	}
	if got := exitCodeOf(updateExit(updateOutcomes{}, cli.Exit("boom", 1), false)); got != 1 {
		t.Errorf("monitoring-error exit = %d, want 1 (propagated)", got)
	}
}

func TestUpdateExit_UserInterruptIsNonZeroWithHint(t *testing.T) {
	err := updateExit(updateOutcomes{Cluster: "prod"}, monitoring.ErrCancelled, false)
	if err == nil {
		t.Fatal("an interrupted run must not exit 0")
	}
	if !errors.Is(err, monitoring.ErrCancelled) {
		t.Errorf("error should wrap ErrCancelled, got %v", err)
	}
	if !strings.Contains(err.Error(), "refresh nodegroup list prod") {
		t.Errorf("error should point at 'refresh nodegroup list prod', got %q", err.Error())
	}
	// The interrupt wins over a stale verifyFailed flag: it must not map to 5.
	if got := exitCodeOf(updateExit(updateOutcomes{Cluster: "prod"}, monitoring.ErrCancelled, true)); got == 5 || got == 0 {
		t.Errorf("interrupt exit = %d, want a general error (not 0 or 5)", got)
	}
}

func TestShouldVerifyPostRoll(t *testing.T) {
	if !shouldVerifyPostRoll(context.Background(), nil) {
		t.Error("a clean monitor on a live ctx should verify")
	}
	if shouldVerifyPostRoll(context.Background(), monitoring.ErrCancelled) {
		t.Error("a user interrupt must skip verification")
	}
	if shouldVerifyPostRoll(context.Background(), fmt.Errorf("one or more nodegroup updates were cancelled")) {
		t.Error("a Cancelled EKS update must skip verification")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if shouldVerifyPostRoll(ctx, nil) {
		t.Error("a cancelled ctx must skip verification even without a monitor error")
	}
}
