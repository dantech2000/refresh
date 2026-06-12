package upgrade

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// controlPlaneWorld is a stateful mock cluster whose version flips once
// UpdateClusterVersion is called and its update polled.
type controlPlaneWorld struct {
	mu            sync.Mutex
	version       string
	status        ekstypes.ClusterStatus
	updateStarted bool
	capturedInput *eks.UpdateClusterVersionInput
}

func newControlPlaneMock(w *controlPlaneWorld) *mocks.EKSAPI {
	m := mocks.NewEKSAPI().Build()
	m.DescribeClusterFn = func(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{
			Name:    in.Name,
			Version: aws.String(w.version),
			Status:  w.status,
		}}, nil
	}
	m.UpdateClusterVersionFn = func(_ context.Context, in *eks.UpdateClusterVersionInput, _ ...func(*eks.Options)) (*eks.UpdateClusterVersionOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.capturedInput = in
		w.updateStarted = true
		w.status = ekstypes.ClusterStatusUpdating
		return &eks.UpdateClusterVersionOutput{Update: &ekstypes.Update{
			Id:     aws.String("update-cp-1"),
			Status: ekstypes.UpdateStatusInProgress,
			Type:   ekstypes.UpdateTypeVersionUpdate,
		}}, nil
	}
	m.DescribeUpdateFn = func(_ context.Context, in *eks.DescribeUpdateInput, _ ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		// First poll completes the update server-side.
		if w.updateStarted {
			w.version = aws.ToString(w.capturedInput.Version)
			w.status = ekstypes.ClusterStatusActive
		}
		return &eks.DescribeUpdateOutput{Update: &ekstypes.Update{
			Id:     in.UpdateId,
			Status: ekstypes.UpdateStatusSuccessful,
		}}, nil
	}
	return m
}

// Acceptance (REF-99): start → monitor → complete, with the idempotency
// token asserted on the mutating call.
func TestUpgradeControlPlane_StartMonitorComplete(t *testing.T) {
	w := &controlPlaneWorld{version: "1.31", status: ekstypes.ClusterStatusActive}
	m := newControlPlaneMock(w)
	svc := newTestService(m)

	if err := svc.UpgradeControlPlane(context.Background(), "prod-east", "1.32", nil); err != nil {
		t.Fatalf("UpgradeControlPlane: %v", err)
	}

	if m.Calls.UpdateClusterVersion != 1 {
		t.Fatalf("UpdateClusterVersion calls = %d, want 1", m.Calls.UpdateClusterVersion)
	}
	if w.capturedInput.ClientRequestToken == nil || *w.capturedInput.ClientRequestToken == "" {
		t.Fatal("ClientRequestToken must be set (idempotency across retries)")
	}
	if aws.ToString(w.capturedInput.Version) != "1.32" {
		t.Fatalf("requested version = %q, want 1.32", aws.ToString(w.capturedInput.Version))
	}
	if w.version != "1.32" {
		t.Fatalf("cluster version after upgrade = %q, want 1.32", w.version)
	}
	if m.Calls.DescribeUpdate == 0 {
		t.Fatal("expected DescribeUpdate polling")
	}
}

// Acceptance (REF-99): a Failed update surfaces its error details.
func TestUpgradeControlPlane_FailureSurfaces(t *testing.T) {
	w := &controlPlaneWorld{version: "1.31", status: ekstypes.ClusterStatusActive}
	m := newControlPlaneMock(w)
	m.DescribeUpdateFn = func(_ context.Context, in *eks.DescribeUpdateInput, _ ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		return &eks.DescribeUpdateOutput{Update: &ekstypes.Update{
			Id:     in.UpdateId,
			Status: ekstypes.UpdateStatusFailed,
			Errors: []ekstypes.ErrorDetail{{
				ErrorCode:    ekstypes.ErrorCodeOperationNotPermitted,
				ErrorMessage: aws.String("insufficient subnet IPs"),
			}},
		}}, nil
	}
	svc := newTestService(m)

	err := svc.UpgradeControlPlane(context.Background(), "prod-east", "1.32", nil)
	if err == nil || !strings.Contains(err.Error(), "insufficient subnet IPs") {
		t.Fatalf("err = %v, want the update's error details surfaced", err)
	}
}

// Acceptance (REF-99): an already-UPDATING cluster is attached and watched,
// not failed and not double-updated.
func TestUpgradeControlPlane_AttachesToInFlightUpdate(t *testing.T) {
	w := &controlPlaneWorld{version: "1.31", status: ekstypes.ClusterStatusUpdating}
	m := newControlPlaneMock(w)
	polls := 0
	m.DescribeClusterFn = func(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		polls++
		// The in-flight (externally started) upgrade completes after a couple
		// of polls.
		if polls >= 3 {
			w.version = "1.32"
			w.status = ekstypes.ClusterStatusActive
		}
		return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{
			Name:    in.Name,
			Version: aws.String(w.version),
			Status:  w.status,
		}}, nil
	}
	svc := newTestService(m)

	if err := svc.UpgradeControlPlane(context.Background(), "prod-east", "1.32", nil); err != nil {
		t.Fatalf("UpgradeControlPlane: %v", err)
	}
	if m.Calls.UpdateClusterVersion != 0 {
		t.Fatalf("UpdateClusterVersion calls = %d, want 0 (attached to in-flight update)", m.Calls.UpdateClusterVersion)
	}
}

// Idempotency: a control plane already at (or past) the target is a no-op.
func TestUpgradeControlPlane_AlreadyAtTargetIsNoOp(t *testing.T) {
	w := &controlPlaneWorld{version: "1.33", status: ekstypes.ClusterStatusActive}
	m := newControlPlaneMock(w)
	svc := newTestService(m)

	if err := svc.UpgradeControlPlane(context.Background(), "prod-east", "1.32", nil); err != nil {
		t.Fatalf("UpgradeControlPlane: %v", err)
	}
	if m.Calls.UpdateClusterVersion != 0 {
		t.Fatalf("UpdateClusterVersion calls = %d, want 0", m.Calls.UpdateClusterVersion)
	}
}

// SIGINT behavior: cancelling the context stops the watch and reports that
// the upgrade continues server-side (the engine wraps this for the user).
func TestUpgradeControlPlane_ContextCancelStopsWatching(t *testing.T) {
	w := &controlPlaneWorld{version: "1.31", status: ekstypes.ClusterStatusActive}
	m := newControlPlaneMock(w)
	m.DescribeUpdateFn = func(_ context.Context, in *eks.DescribeUpdateInput, _ ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		return &eks.DescribeUpdateOutput{Update: &ekstypes.Update{
			Id:     in.UpdateId,
			Status: ekstypes.UpdateStatusInProgress, // never completes
		}}, nil
	}
	svc := newTestService(m)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := svc.UpgradeControlPlane(ctx, "prod-east", "1.32", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// The mutation was still issued exactly once before cancellation.
	if m.Calls.UpdateClusterVersion != 1 {
		t.Fatalf("UpdateClusterVersion calls = %d, want 1", m.Calls.UpdateClusterVersion)
	}
}
