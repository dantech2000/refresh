package monitoring

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
	"github.com/fatih/color"
	"go.uber.org/goleak"

	"github.com/dantech2000/refresh/internal/mocks"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func fakeEKSDescribeUpdate(status ekstypes.UpdateStatus, errMsg string) *eks.Client {
	return eks.New(eks.Options{
		Region:      "us-east-1",
		Credentials: aws.AnonymousCredentials{},
		HTTPClient: roundTripFunc(func(*http.Request) (*http.Response, error) {
			body := `{"update":{"id":"upd-a","status":"` + string(status) + `"}`
			if errMsg != "" {
				body += `,"errors":[{"errorMessage":"` + errMsg + `"}]`
			}
			body += `}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	})
}

func fakeEKSErrorThenSuccess() *eks.Client {
	calls := 0
	return eks.New(eks.Options{
		Region:      "us-east-1",
		Credentials: aws.AnonymousCredentials{},
		HTTPClient: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("temporary")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"update":{"id":"upd-a","status":"Successful"}}`)),
			}, nil
		}),
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// isUpdateComplete
// ──────────────────────────────────────────────────────────────────────────────

func TestIsUpdateComplete_Successful(t *testing.T) {
	if !isUpdateComplete(ekstypes.UpdateStatusSuccessful) {
		t.Error("Successful should be complete")
	}
}

func TestIsUpdateComplete_Failed(t *testing.T) {
	if !isUpdateComplete(ekstypes.UpdateStatusFailed) {
		t.Error("Failed should be complete")
	}
}

func TestIsUpdateComplete_Cancelled(t *testing.T) {
	if !isUpdateComplete(ekstypes.UpdateStatusCancelled) {
		t.Error("Cancelled should be complete")
	}
}

func TestIsUpdateComplete_InProgress(t *testing.T) {
	if isUpdateComplete(ekstypes.UpdateStatusInProgress) {
		t.Error("InProgress should not be complete")
	}
}

func TestIsUpdateComplete_Degraded(t *testing.T) {
	// Any unknown/non-terminal status should not be complete.
	if isUpdateComplete("SOME_OTHER_STATUS") {
		t.Error("unknown status should not be complete")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// handleTimeout
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleTimeout_ReturnsError(t *testing.T) {
	cfg := refreshTypes.MonitorConfig{Quiet: true}
	err := handleTimeout(&refreshTypes.ProgressMonitor{}, cfg)
	if err == nil {
		t.Fatal("handleTimeout should return a non-nil error")
	}
	if err.Error() == "" {
		t.Error("error message should not be empty")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// handleUserCancellation
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleUserCancellation_ReturnsErrCancelled(t *testing.T) {
	monitor := &refreshTypes.ProgressMonitor{}
	cfg := refreshTypes.MonitorConfig{Quiet: true}
	if err := handleUserCancellation(monitor, cfg); !errors.Is(err, ErrCancelled) {
		t.Errorf("handleUserCancellation should return ErrCancelled, got %v", err)
	}
}

// Ctrl+C cancels the root context in main; MonitorUpdates must report that as
// ErrCancelled (not nil, not a timeout) so callers skip verification.
func TestMonitorUpdates_ParentCancelReturnsErrCancelled(t *testing.T) {
	cfg := refreshTypes.MonitorConfig{
		Quiet:        true,
		PollInterval: time.Hour,
		Timeout:      time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	monitor := testMonitorWithUpdates(ekstypes.UpdateStatusInProgress)
	err := MonitorUpdates(ctx, fakeEKSDescribeUpdate(ekstypes.UpdateStatusInProgress, ""), monitor, cfg)
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("MonitorUpdates on cancelled ctx = %v, want ErrCancelled", err)
	}
}

// An EKS update that ends Cancelled must surface as an error from the monitor.
func TestMonitorUpdates_CancelledUpdateReturnsError(t *testing.T) {
	cfg := refreshTypes.MonitorConfig{
		Quiet:        true,
		PollInterval: time.Millisecond,
		Timeout:      time.Second,
	}
	monitor := testMonitorWithUpdates(ekstypes.UpdateStatusInProgress)
	err := MonitorUpdates(context.Background(), fakeEKSDescribeUpdate(ekstypes.UpdateStatusCancelled, ""), monitor, cfg)
	if err == nil {
		t.Fatal("a Cancelled EKS update should return an error")
	}
	if errors.Is(err, ErrCancelled) {
		t.Errorf("an EKS-side cancellation is not a user interrupt, got %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// DisplayCompletionSummary
// ──────────────────────────────────────────────────────────────────────────────

func testMonitorWithUpdates(statuses ...ekstypes.UpdateStatus) *refreshTypes.ProgressMonitor {
	pm := &refreshTypes.ProgressMonitor{}
	for i, status := range statuses {
		pm.Updates = append(pm.Updates, refreshTypes.UpdateProgress{
			NodegroupName: "ng",
			ClusterName:   "my-cluster",
			UpdateID:      "upd-" + string(rune('a'+i)),
			Status:        status,
			StartTime:     time.Now().Add(-30 * time.Second),
			LastChecked:   time.Now(),
		})
	}
	return pm
}

func TestDisplayCompletionSummary_AllSuccessfulReturnsNil(t *testing.T) {
	monitor := testMonitorWithUpdates(
		ekstypes.UpdateStatusSuccessful,
		ekstypes.UpdateStatusSuccessful,
	)
	cfg := refreshTypes.MonitorConfig{Quiet: true}
	if err := DisplayCompletionSummary(monitor, cfg); err != nil {
		t.Errorf("all successful: expected nil error, got %v", err)
	}
}

func TestDisplayCompletionSummary_AnyFailedReturnsError(t *testing.T) {
	monitor := testMonitorWithUpdates(
		ekstypes.UpdateStatusSuccessful,
		ekstypes.UpdateStatusFailed,
	)
	cfg := refreshTypes.MonitorConfig{Quiet: true}
	if err := DisplayCompletionSummary(monitor, cfg); err == nil {
		t.Error("a failed update should cause DisplayCompletionSummary to return an error")
	}
}

// The returned error must carry the nodegroup and AWS error, since the live
// roll panel suppresses the monitor's own output while the roll runs.
func TestDisplayCompletionSummary_FailedErrorIncludesAWSMessage(t *testing.T) {
	monitor := testMonitorWithUpdates(ekstypes.UpdateStatusFailed)
	monitor.Updates[0].ErrorMessage = "PodEvictionFailure: Reached max retries"
	err := DisplayCompletionSummary(monitor, refreshTypes.MonitorConfig{Quiet: true})
	if err == nil || !strings.Contains(err.Error(), "ng: PodEvictionFailure: Reached max retries") {
		t.Fatalf("err = %v, want it to name the nodegroup and AWS error", err)
	}
}

// After a quiet run under the live panel, the caller prints the banner the
// monitor held back: timeout on ErrMonitorTimeout, cancellation on ErrCancelled.
func TestDisplayStopped_PrintsHeldBanner(t *testing.T) {
	origColor := color.Output
	t.Cleanup(func() { color.Output = origColor })
	capture := func(fn func()) string {
		r, w, _ := os.Pipe()
		old := os.Stdout
		os.Stdout, color.Output = w, w
		fn()
		_ = w.Close()
		os.Stdout, color.Output = old, origColor
		b, _ := io.ReadAll(r)
		return string(b)
	}
	monitor := testMonitorWithUpdates(ekstypes.UpdateStatusInProgress)
	cfg := refreshTypes.MonitorConfig{Timeout: 40 * time.Minute}

	out := capture(func() { DisplayStopped(monitor, cfg, ErrMonitorTimeout) })
	if !strings.Contains(out, "Monitoring timeout reached after 40m0s") {
		t.Errorf("timeout banner missing; got:\n%s", out)
	}
	out = capture(func() { DisplayStopped(monitor, cfg, ErrCancelled) })
	if !strings.Contains(out, "Monitoring cancelled by user") {
		t.Errorf("cancellation banner missing; got:\n%s", out)
	}
	if out = capture(func() { DisplayStopped(monitor, cfg, nil) }); out != "" {
		t.Errorf("nil error printed a banner:\n%s", out)
	}
	cfg.Quiet = true
	if out = capture(func() { DisplayStopped(monitor, cfg, ErrMonitorTimeout) }); out != "" {
		t.Errorf("quiet config printed:\n%s", out)
	}
}

func TestAllComplete(t *testing.T) {
	if AllComplete(testMonitorWithUpdates()) {
		t.Error("no updates must not count as complete")
	}
	if AllComplete(testMonitorWithUpdates(ekstypes.UpdateStatusFailed, ekstypes.UpdateStatusInProgress)) {
		t.Error("an in-progress update must not count as complete")
	}
	if !AllComplete(testMonitorWithUpdates(ekstypes.UpdateStatusFailed, ekstypes.UpdateStatusSuccessful, ekstypes.UpdateStatusCancelled)) {
		t.Error("all terminal updates must count as complete")
	}
}

func TestDisplayCompletionSummary_CancelledReturnsError(t *testing.T) {
	// A Cancelled update did not roll: it is counted as failed in the summary
	// and must return an error so the run exits non-zero and skips verification.
	monitor := testMonitorWithUpdates(
		ekstypes.UpdateStatusSuccessful,
		ekstypes.UpdateStatusCancelled,
	)
	cfg := refreshTypes.MonitorConfig{Quiet: true}
	if err := DisplayCompletionSummary(monitor, cfg); err == nil {
		t.Error("a cancelled update should cause DisplayCompletionSummary to return an error")
	}
}

func TestDisplayCompletionSummary_EmptyMonitorReturnsNil(t *testing.T) {
	monitor := &refreshTypes.ProgressMonitor{}
	cfg := refreshTypes.MonitorConfig{Quiet: true}
	if err := DisplayCompletionSummary(monitor, cfg); err != nil {
		t.Errorf("empty monitor: expected nil, got %v", err)
	}
}

// describeUpdateSequence returns a DescribeUpdate mock that fails with errs in
// order, then reports status.
func describeUpdateSequence(status ekstypes.UpdateStatus, errs ...error) *mocks.EKSAPI {
	var mu sync.Mutex
	calls := 0
	return &mocks.EKSAPI{
		DescribeUpdateFn: func(_ context.Context, in *eks.DescribeUpdateInput, _ ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls <= len(errs) {
				return nil, errs[calls-1]
			}
			return &eks.DescribeUpdateOutput{Update: &ekstypes.Update{Id: in.UpdateId, Status: status}}, nil
		},
	}
}

var (
	errThrottled    = &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}
	errAccessDenied = &smithy.GenericAPIError{
		Code:    "AccessDeniedException",
		Message: "User arn:aws:iam::123:user/x is not authorized to perform eks:DescribeUpdate",
	}
)

// Throttling is retried within one status check (common.WithRetry).
func TestCheckSingleUpdate_RetriesThrottling(t *testing.T) {
	update := &refreshTypes.UpdateProgress{ClusterName: "cluster", NodegroupName: "ng", UpdateID: "upd-a"}
	m := describeUpdateSequence(ekstypes.UpdateStatusSuccessful, errThrottled, errThrottled)
	result := checkSingleUpdate(context.Background(), m, update)
	if result.err != nil || result.status != ekstypes.UpdateStatusSuccessful {
		t.Fatalf("result = %+v, want Successful", result)
	}
	if m.Calls.DescribeUpdate != 3 {
		t.Errorf("DescribeUpdate calls = %d, want 3", m.Calls.DescribeUpdate)
	}
}

// AccessDenied is not retried; the update is settled as unmonitored and the
// monitor returns a formatted error that names the IAM action instead of
// polling forever.
func TestMonitorUpdates_AccessDeniedStopsWithFormattedError(t *testing.T) {
	update := &refreshTypes.UpdateProgress{ClusterName: "cluster", NodegroupName: "ng", UpdateID: "upd-a"}
	m := describeUpdateSequence(ekstypes.UpdateStatusSuccessful, errAccessDenied)
	if result := checkSingleUpdate(context.Background(), m, update); result.err == nil {
		t.Fatal("want an AccessDenied error")
	}
	if m.Calls.DescribeUpdate != 1 {
		t.Errorf("DescribeUpdate calls = %d, want 1 (AccessDenied is not retryable)", m.Calls.DescribeUpdate)
	}

	denied := &mocks.EKSAPI{
		DescribeUpdateFn: func(context.Context, *eks.DescribeUpdateInput, ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
			return nil, errAccessDenied
		},
	}
	cfg := refreshTypes.MonitorConfig{Quiet: true, PollInterval: time.Millisecond, Timeout: time.Minute}
	err := MonitorUpdates(context.Background(), denied, testMonitorWithUpdates(ekstypes.UpdateStatusInProgress), cfg)
	if !errors.Is(err, ErrUnmonitored) || errors.Is(err, ErrCancelled) || errors.Is(err, ErrMonitorTimeout) {
		t.Fatalf("err = %v, want ErrUnmonitored", err)
	}
	for _, want := range []string{"permissions", "eks:DescribeUpdate", "continue in the background"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%s", want, err)
		}
	}
}

// One update failing permanently (AccessDenied) must not stop monitoring of
// the others: they are polled to Successful and shown in the final display,
// and the error names only the update that could not be monitored.
func TestMonitorUpdates_OneUnmonitoredUpdateDoesNotStopTheOthers(t *testing.T) {
	var mu sync.Mutex
	polls := map[string]int{}
	m := &mocks.EKSAPI{
		DescribeUpdateFn: func(_ context.Context, in *eks.DescribeUpdateInput, _ ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
			ng := aws.ToString(in.NodegroupName)
			if ng == "ng-denied" {
				return nil, errAccessDenied
			}
			mu.Lock()
			defer mu.Unlock()
			polls[ng]++
			status := ekstypes.UpdateStatusInProgress
			if polls[ng] >= 3 { // still rolling after the denied update settles
				status = ekstypes.UpdateStatusSuccessful
			}
			return &eks.DescribeUpdateOutput{Update: &ekstypes.Update{Id: in.UpdateId, Status: status}}, nil
		},
	}
	monitor := &refreshTypes.ProgressMonitor{StartTime: time.Now()}
	for _, ng := range []string{"ng-a", "ng-denied", "ng-b"} {
		monitor.Updates = append(monitor.Updates, refreshTypes.UpdateProgress{
			NodegroupName: ng, ClusterName: "prod", UpdateID: "upd-" + ng,
			Status: ekstypes.UpdateStatusInProgress, StartTime: time.Now(),
		})
	}
	cfg := refreshTypes.MonitorConfig{PollInterval: time.Millisecond, Timeout: time.Minute}

	var err error
	out := captureStdout(func() { err = MonitorUpdates(context.Background(), m, monitor, cfg) })

	for _, i := range []int{0, 2} {
		if got := monitor.Updates[i].Status; got != ekstypes.UpdateStatusSuccessful {
			t.Errorf("%s status = %s, want Successful", monitor.Updates[i].NodegroupName, got)
		}
	}
	if monitor.Updates[1].MonitorErr == nil || monitor.Updates[1].Status == ekstypes.UpdateStatusFailed {
		t.Errorf("ng-denied = %+v, want MonitorErr set and not EKS Failed", monitor.Updates[1])
	}
	if !errors.Is(err, ErrUnmonitored) {
		t.Fatalf("err = %v, want ErrUnmonitored", err)
	}
	if !strings.Contains(err.Error(), "ng-denied (upd-ng-denied)") || strings.Contains(err.Error(), "ng-a") || strings.Contains(err.Error(), "ng-b") {
		t.Errorf("error must name only ng-denied:\n%s", err)
	}
	// The final display lists both completed rolls and the unmonitored one.
	summary := out[strings.LastIndex(out, "Monitoring finished"):]
	for _, want := range []string{" SUCCESSFUL ng-a", " SUCCESSFUL ng-b", " MONITORING FAILED ng-denied", "2 successful, 0 failed, 1 not monitored"} {
		if !strings.Contains(summary, want) {
			t.Errorf("final display missing %q:\n%s", want, summary)
		}
	}
}

// A transport error (no typed AWS error) stays transient: it is recorded on
// the update and polled through.
func TestCheckAllUpdates_TransportErrorIsPolledThrough(t *testing.T) {
	m := describeUpdateSequence(ekstypes.UpdateStatusSuccessful, errors.New("dial tcp: connection refused"))
	monitor := testMonitorWithUpdates(ekstypes.UpdateStatusInProgress)
	cfg := refreshTypes.MonitorConfig{Quiet: true}
	if done := checkAllUpdatesWithChannels(context.Background(), m, monitor, cfg); done || monitor.Updates[0].LastCheckError == "" || monitor.Updates[0].MonitorErr != nil {
		t.Fatalf("first poll: done %v, update %+v; want not done, transient error recorded", done, monitor.Updates[0])
	}
	if done := checkAllUpdatesWithChannels(context.Background(), m, monitor, cfg); !done || monitor.Updates[0].LastCheckError != "" {
		t.Fatalf("second poll: done %v, LastCheckError %q; want done, cleared", done, monitor.Updates[0].LastCheckError)
	}
}

// Cancelling ctx while the monitor waits (main does this on Ctrl+C) returns
// ErrCancelled. The monitor has no signal handler of its own.
func TestMonitorUpdates_CancelDuringWaitReturnsErrCancelled(t *testing.T) {
	defer goleak.VerifyNone(t)
	cfg := refreshTypes.MonitorConfig{Quiet: true, PollInterval: time.Hour, Timeout: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)
	m := describeUpdateSequence(ekstypes.UpdateStatusInProgress)
	err := MonitorUpdates(ctx, m, testMonitorWithUpdates(ekstypes.UpdateStatusInProgress), cfg)
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v, want ErrCancelled", err)
	}
}

// A zero or negative poll interval must not panic time.NewTicker: the monitor
// falls back to the default interval.
func TestMonitorUpdates_NonPositivePollIntervalDoesNotPanic(t *testing.T) {
	for _, pi := range []time.Duration{0, -time.Second} {
		cfg := refreshTypes.MonitorConfig{Quiet: true, PollInterval: pi, Timeout: time.Hour}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		err := MonitorUpdates(ctx, describeUpdateSequence(ekstypes.UpdateStatusInProgress), testMonitorWithUpdates(ekstypes.UpdateStatusInProgress), cfg)
		cancel()
		// The parent deadline is not a plain cancel, so it reports a timeout.
		if !errors.Is(err, ErrMonitorTimeout) {
			t.Errorf("PollInterval %v: err = %v, want ErrMonitorTimeout", pi, err)
		}
	}
}

func TestCheckSingleUpdateAndAllUpdates(t *testing.T) {
	cfg := refreshTypes.MonitorConfig{Quiet: true}
	update := &refreshTypes.UpdateProgress{ClusterName: "cluster", NodegroupName: "ng", UpdateID: "upd-a"}

	result := checkSingleUpdate(context.Background(), fakeEKSErrorThenSuccess(), update)
	if result.err != nil || result.status != ekstypes.UpdateStatusSuccessful {
		t.Fatalf("SDK transport retry: result = %+v", result)
	}

	result = checkSingleUpdate(context.Background(), fakeEKSDescribeUpdate(ekstypes.UpdateStatusFailed, "boom"), update)
	if result.status != ekstypes.UpdateStatusFailed {
		t.Fatalf("checkSingleUpdate result = %+v", result)
	}

	errClient := eks.New(eks.Options{Region: "us-east-1", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1, HTTPClient: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network")
	})})
	result = checkSingleUpdate(context.Background(), errClient, update)
	if result.err == nil {
		t.Fatal("expected single update error")
	}

	monitor := testMonitorWithUpdates(ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusSuccessful)
	if !checkAllUpdatesWithChannels(context.Background(), fakeEKSDescribeUpdate(ekstypes.UpdateStatusSuccessful, ""), monitor, cfg) {
		t.Fatal("checkAllUpdatesWithChannels: want all complete")
	}
	if monitor.Updates[0].Status != ekstypes.UpdateStatusSuccessful {
		t.Fatalf("monitor update not updated: %+v", monitor.Updates[0])
	}

	empty := &refreshTypes.ProgressMonitor{}
	if checkAllUpdatesWithChannels(context.Background(), fakeEKSDescribeUpdate(ekstypes.UpdateStatusSuccessful, ""), empty, cfg) {
		t.Fatal("empty checkAllUpdatesWithChannels: want not complete")
	}
}

func TestMonitorUpdatesCompletesAndTimesOut(t *testing.T) {
	// The completion case must not race the clock: a generous timeout means
	// it passes however slowly the machine polls.
	cfg := refreshTypes.MonitorConfig{
		Quiet:        true,
		PollInterval: time.Millisecond,
		Timeout:      time.Minute,
	}
	monitor := testMonitorWithUpdates(ekstypes.UpdateStatusInProgress)
	if err := MonitorUpdates(context.Background(), fakeEKSDescribeUpdate(ekstypes.UpdateStatusSuccessful, ""), monitor, cfg); err != nil {
		t.Fatalf("MonitorUpdates complete = %v", err)
	}

	// The update never finishes, so the only way out is the timeout.
	timeoutCfg := cfg
	timeoutCfg.Timeout = 2 * time.Millisecond
	timeoutMonitor := testMonitorWithUpdates(ekstypes.UpdateStatusInProgress)
	if err := MonitorUpdates(context.Background(), fakeEKSDescribeUpdate(ekstypes.UpdateStatusInProgress, ""), timeoutMonitor, timeoutCfg); !errors.Is(err, ErrMonitorTimeout) {
		t.Fatalf("err = %v, want ErrMonitorTimeout", err)
	}
}

// A zero (or negative) --timeout means "no monitor timeout": monitoring must
// keep polling through several IN_PROGRESS answers until the update
// finishes, and no poll may run under a deadline. A monitor that turned 0
// into an expired or short deadline fails here: either it stops before the
// sixth poll, or a poll sees a deadline.
func TestMonitorUpdates_ZeroTimeoutMeansNoLimit(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			const inProgressPolls = 5
			script := make([]ekstypes.UpdateStatus, 0, inProgressPolls+1)
			for range inProgressPolls {
				script = append(script, ekstypes.UpdateStatusInProgress)
			}
			script = append(script, ekstypes.UpdateStatusSuccessful)
			m := mocks.NewEKSAPI().WithUpdateStatuses("upd-a", script...).Build()

			var mu sync.Mutex
			polls, deadlines := 0, 0
			describe := m.DescribeUpdateFn
			m.DescribeUpdateFn = func(ctx context.Context, in *eks.DescribeUpdateInput, opts ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
				mu.Lock()
				polls++
				if _, ok := ctx.Deadline(); ok {
					deadlines++
				}
				mu.Unlock()
				return describe(ctx, in, opts...)
			}

			cfg := refreshTypes.MonitorConfig{Quiet: true, PollInterval: time.Millisecond, Timeout: timeout}
			monitor := testMonitorWithUpdates(ekstypes.UpdateStatusInProgress)
			if err := MonitorUpdates(context.Background(), m, monitor, cfg); err != nil {
				t.Fatalf("MonitorUpdates = %v, want nil (no limit)", err)
			}
			if polls != inProgressPolls+1 {
				t.Fatalf("polls = %d, want %d (every IN_PROGRESS answer, then SUCCESSFUL)", polls, inProgressPolls+1)
			}
			if deadlines != 0 {
				t.Fatalf("%d poll(s) ran under a deadline; timeout %v must mean no limit", deadlines, timeout)
			}
			if monitor.Updates[0].Status != ekstypes.UpdateStatusSuccessful {
				t.Fatalf("status = %s, want Successful", monitor.Updates[0].Status)
			}
		})
	}
}

func TestMonitorContext(t *testing.T) {
	ctx, cancel := monitorContext(context.Background(), 0)
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("timeout 0: want no deadline")
	}
	ctx2, cancel2 := monitorContext(context.Background(), time.Minute)
	defer cancel2()
	if _, ok := ctx2.Deadline(); !ok {
		t.Fatal("timeout 1m: want a deadline")
	}
	if got := formatMonitorTimeout(0); got != "none" {
		t.Fatalf("formatMonitorTimeout(0) = %q, want none", got)
	}
}
