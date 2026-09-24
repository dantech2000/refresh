package upgrade

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/aws/awserr"
)

// A run deadline (--wait-timeout) that passes mid-phase is a timeout, not an
// interrupt. The error carries the timeout hint that the command retargets
// to --wait-timeout, and it keeps the resume guidance.
func TestExecute_DeadlineSaysTimedOut(t *testing.T) {
	w := newWorld()
	w.hangUpdates = true // control-plane update never completes
	svc := newTestService(newWorldMock(w))

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	report, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "timed out during control plane") || strings.Contains(msg, "interrupted") {
		t.Fatalf("err = %q, want a timeout during the control-plane phase, not an interrupt", msg)
	}
	for _, want := range []string{awserr.TimeoutHint(), "continue server-side", "rerun the same command to resume"} {
		if !strings.Contains(msg, want) {
			t.Errorf("err = %q, want it to contain %q", msg, want)
		}
	}
	if got := awserr.RetargetDeadlineHint(err, "--wait-timeout").Error(); !strings.Contains(got, "(increase --wait-timeout to allow more time)") {
		t.Errorf("retargeted err = %q, want it to name --wait-timeout", got)
	}
	if !strings.Contains(report.FailedAt, "control plane") {
		t.Fatalf("failedAt = %q, want the control-plane phase", report.FailedAt)
	}
}

// Ctrl+C mid-phase (a cancelled ctx) still reads as an interrupt, with no
// timeout hint.
func TestExecute_CancelSaysInterrupted(t *testing.T) {
	w := newWorld()
	w.hangUpdates = true
	m := newWorldMock(w)
	svc := newTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	describe := m.DescribeUpdateFn
	m.DescribeUpdateFn = func(c context.Context, in *eks.DescribeUpdateInput, o ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		cancel() // the user presses Ctrl+C while the update is in progress
		return describe(c, in, o...)
	}

	_, err = svc.Execute(ctx, plan, ExecuteOptions{Yes: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "interrupted during control plane") || strings.Contains(msg, "timed out") || strings.Contains(msg, awserr.TimeoutHint()) {
		t.Fatalf("err = %q, want an interrupt during the control-plane phase without a timeout hint", msg)
	}
	if !strings.Contains(msg, "rerun the same command to resume") {
		t.Errorf("err = %q, want the resume guidance", msg)
	}
}

// The run deadline during the plan-time insights refresh is a timeout, not
// an interrupt.
func TestBuildPlan_DeadlineDuringRefreshSaysTimedOut(t *testing.T) {
	m := oneHopBuilder().
		WithInsightsRefresh(ekstypes.InsightsRefreshStatusInProgress).
		WithInsight("prod-east", "Kubelet version skew", ekstypes.InsightStatusValuePassing, "1.32").
		Build()
	svc := newStrictTestService(m)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	plan, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{})
	if !errors.Is(err, context.DeadlineExceeded) || plan != nil {
		t.Fatalf("BuildPlan = (%v, %v), want (nil, context.DeadlineExceeded)", plan, err)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "timed out while building the upgrade plan") || !strings.Contains(msg, awserr.TimeoutHint()) {
		t.Fatalf("err = %q, want a timeout with the timeout hint", msg)
	}
}

// stopped adds the timeout hint once: an AWS error formatted after the
// deadline already carries it.
func TestStopped_HintOnce(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now())
	defer cancel()
	<-ctx.Done()
	inner := awserr.FormatAWSError(ctx.Err(), "describing update u-1")
	err := stopped(ctx, "during addons", "rerun the same command to resume", inner)
	if n := strings.Count(err.Error(), awserr.TimeoutHint()); n != 1 {
		t.Fatalf("err = %q has the timeout hint %d times, want 1", err, n)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
}
