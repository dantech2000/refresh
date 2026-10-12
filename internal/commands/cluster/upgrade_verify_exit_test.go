package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks"
	"github.com/dantech2000/refresh/internal/services/upgrade"
)

// A run that is cancelled while the verification reports its checks is an
// interrupted run. The engine's report says Interrupted, so the exit code is
// 1, not the 5 or 4 of the verification error the engine wraps.
func TestVerifyExit_InterruptDuringVerificationExitsOne(t *testing.T) {
	m, svc, plan := gateWorld(t)
	describe := m.DescribeNodegroupFn
	// After the roll the nodegroup cannot be read: without the interrupt the
	// run would exit 4.
	m.DescribeNodegroupFn = func(ctx context.Context, in *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		if m.Calls.UpdateNodegroupVersion > 0 {
			return nil, mocks.AccessDenied()
		}
		return describe(ctx, in, optFns...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := upgrade.ExecuteOptions{
		Yes:    true,
		Verify: &upgrade.PostRollVerify{Kube: func(context.Context) kubernetes.Interface { return nil }},
		Progress: func(format string, args ...any) {
			if strings.HasPrefix(fmt.Sprintf(format, args...), "post-roll verification:") {
				cancel() // Ctrl+C while the checks are reported
			}
		},
	}

	report, err := svc.Execute(ctx, plan, opts)
	if !errors.Is(err, upgrade.ErrVerifyIncomplete) {
		t.Fatalf("err = %v, want the engine to wrap the verification error", err)
	}
	if report.Status != upgrade.RunInterrupted {
		t.Fatalf("report.Status = %s, want Interrupted", report.Status)
	}
	if code := runner.ExitCodeOf(verifyExit(report, err)); code != runner.ExitError {
		t.Errorf("exit code = %d, want 1: the report says %s", code, report.Status)
	}
}

// verifyExit follows the report's status, for upgrade and rollback: only a
// VerifyFailed run exits 5, and only a Failed run whose verification could
// not read the nodegroup exits 4.
func TestVerifyExit_FollowsTheReport(t *testing.T) {
	failed := fmt.Errorf("nodegroup rolls failed: %w", upgrade.ErrVerifyFailed)
	unread := fmt.Errorf("nodegroup rolls failed: %w", upgrade.ErrVerifyIncomplete)
	for _, tc := range []struct {
		name   string
		status upgrade.RunStatus
		err    error
		want   int
	}{
		{"issues", upgrade.RunVerifyFailed, failed, runner.ExitVerifyFailed},
		{"unread nodegroup", upgrade.RunFailed, unread, runner.ExitIncomplete},
		{"interrupted with issues", upgrade.RunInterrupted, failed, runner.ExitError},
		{"timed out with issues", upgrade.RunTimedOut, failed, runner.ExitError},
		{"interrupted with an unread nodegroup", upgrade.RunInterrupted, unread, runner.ExitError},
		{"timed out with an unread nodegroup", upgrade.RunTimedOut, unread, runner.ExitError},
		{"another failure", upgrade.RunFailed, errors.New("roll failed"), runner.ExitError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code := runner.ExitCodeOf(verifyExit(&upgrade.Report{Status: tc.status}, tc.err)); code != tc.want {
				t.Errorf("exit code = %d, want %d", code, tc.want)
			}
		})
	}
}
