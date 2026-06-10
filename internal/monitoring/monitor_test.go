package monitoring

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
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
				StatusCode: 200,
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
				StatusCode: 200,
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
// waitWithContext
// ──────────────────────────────────────────────────────────────────────────────

func TestWaitWithContext_CompletesWhenTimerFires(t *testing.T) {
	ctx := context.Background()
	ok := waitWithContext(ctx, 1*time.Millisecond)
	if !ok {
		t.Error("wait should return true when timer fires normally")
	}
}

func TestWaitWithContext_ReturnsFalseOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	ok := waitWithContext(ctx, 10*time.Second)
	if ok {
		t.Error("wait should return false when context is already cancelled")
	}
}

func TestWaitWithContext_ContextCancelledDuringWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	ok := waitWithContext(ctx, 10*time.Second)
	if ok {
		t.Error("wait should return false when context is cancelled during wait")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// handleTimeout
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleTimeout_ReturnsError(t *testing.T) {
	cfg := refreshTypes.MonitorConfig{Quiet: true}
	err := handleTimeout(cfg)
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

func TestHandleUserCancellation_ReturnsNil(t *testing.T) {
	monitor := refreshTypes.NewProgressMonitor(true, false, 0)
	cfg := refreshTypes.MonitorConfig{Quiet: true}
	if err := handleUserCancellation(monitor, cfg); err != nil {
		t.Errorf("handleUserCancellation should return nil, got %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// DisplayCompletionSummary
// ──────────────────────────────────────────────────────────────────────────────

func testMonitorWithUpdates(statuses ...ekstypes.UpdateStatus) *refreshTypes.ProgressMonitor {
	pm := refreshTypes.NewProgressMonitor(false, false, 0)
	for i, status := range statuses {
		pm.AddUpdate(refreshTypes.UpdateProgress{
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

func TestDisplayCompletionSummary_CancelledDoesNotReturnError(t *testing.T) {
	// Cancelled is not the same as failed — it should not surface as an error.
	monitor := testMonitorWithUpdates(
		ekstypes.UpdateStatusSuccessful,
		ekstypes.UpdateStatusCancelled,
	)
	cfg := refreshTypes.MonitorConfig{Quiet: true}
	if err := DisplayCompletionSummary(monitor, cfg); err != nil {
		t.Errorf("cancelled update should not return error, got %v", err)
	}
}

func TestDisplayCompletionSummary_EmptyMonitorReturnsNil(t *testing.T) {
	monitor := refreshTypes.NewProgressMonitor(true, false, 0)
	cfg := refreshTypes.MonitorConfig{Quiet: true}
	if err := DisplayCompletionSummary(monitor, cfg); err != nil {
		t.Errorf("empty monitor: expected nil, got %v", err)
	}
}

func TestCheckUpdateWithRetrySuccessAndErrors(t *testing.T) {
	cfg := refreshTypes.MonitorConfig{MaxRetries: 2, BackoffMultiple: 1}
	update := refreshTypes.UpdateProgress{ClusterName: "cluster", NodegroupName: "ng", UpdateID: "upd-a"}

	out, err := checkUpdateWithRetry(context.Background(), fakeEKSDescribeUpdate(ekstypes.UpdateStatusSuccessful, ""), update, cfg)
	if err != nil {
		t.Fatalf("checkUpdateWithRetry success = %v", err)
	}
	if out.Update.Status != ekstypes.UpdateStatusSuccessful {
		t.Fatalf("status = %s", out.Update.Status)
	}

	out, err = checkUpdateWithRetry(context.Background(), fakeEKSErrorThenSuccess(), update, cfg)
	if err != nil || out.Update.Status != ekstypes.UpdateStatusSuccessful {
		t.Fatalf("retry success = %#v, %v", out, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = checkUpdateWithRetry(ctx, fakeEKSErrorThenSuccess(), update, cfg)
	if err == nil {
		t.Fatal("expected cancelled retry error")
	}
}

func TestCheckSingleUpdateAndAllUpdates(t *testing.T) {
	cfg := refreshTypes.MonitorConfig{Quiet: true, MaxRetries: 1, BackoffMultiple: 1}
	update := refreshTypes.UpdateProgress{ClusterName: "cluster", NodegroupName: "ng", UpdateID: "upd-a"}

	result := checkSingleUpdate(context.Background(), fakeEKSDescribeUpdate(ekstypes.UpdateStatusFailed, "boom"), update, cfg)
	if result.status != ekstypes.UpdateStatusFailed {
		t.Fatalf("checkSingleUpdate result = %+v", result)
	}

	errClient := eks.New(eks.Options{Region: "us-east-1", Credentials: aws.AnonymousCredentials{}, HTTPClient: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network")
	})})
	result = checkSingleUpdate(context.Background(), errClient, update, cfg)
	if result.err == nil {
		t.Fatal("expected single update error")
	}

	monitor := testMonitorWithUpdates(ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusSuccessful)
	allComplete, err := checkAllUpdatesWithChannels(context.Background(), fakeEKSDescribeUpdate(ekstypes.UpdateStatusSuccessful, ""), monitor, cfg)
	if err != nil || !allComplete {
		t.Fatalf("checkAllUpdatesWithChannels = %v, %v", allComplete, err)
	}
	if got := monitor.GetUpdates()[0]; got.Status != ekstypes.UpdateStatusSuccessful {
		t.Fatalf("monitor update not updated: %+v", got)
	}

	empty := refreshTypes.NewProgressMonitor(true, false, 0)
	allComplete, err = checkAllUpdatesWithChannels(context.Background(), fakeEKSDescribeUpdate(ekstypes.UpdateStatusSuccessful, ""), empty, cfg)
	if err != nil || allComplete {
		t.Fatalf("empty checkAllUpdatesWithChannels = %v, %v", allComplete, err)
	}
}

func TestMonitorUpdatesCompletesAndTimesOut(t *testing.T) {
	cfg := refreshTypes.MonitorConfig{
		Quiet:           true,
		PollInterval:    time.Millisecond,
		Timeout:         50 * time.Millisecond,
		MaxRetries:      1,
		BackoffMultiple: 1,
	}
	monitor := testMonitorWithUpdates(ekstypes.UpdateStatusInProgress)
	if err := MonitorUpdates(context.Background(), fakeEKSDescribeUpdate(ekstypes.UpdateStatusSuccessful, ""), monitor, cfg); err != nil {
		t.Fatalf("MonitorUpdates complete = %v", err)
	}

	timeoutCfg := cfg
	timeoutCfg.Timeout = 2 * time.Millisecond
	timeoutMonitor := testMonitorWithUpdates(ekstypes.UpdateStatusInProgress)
	if err := MonitorUpdates(context.Background(), fakeEKSDescribeUpdate(ekstypes.UpdateStatusInProgress, ""), timeoutMonitor, timeoutCfg); err == nil {
		t.Fatal("expected timeout")
	}
}
