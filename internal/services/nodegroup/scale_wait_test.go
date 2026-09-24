package nodegroup

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// ──────────────────────────────────────────────────────────────────────────────
// --min/--max without --desired
// ──────────────────────────────────────────────────────────────────────────────

func TestScale_MaxOnlyBelowCurrentFails(t *testing.T) {
	// Even with --force, a --max below the current desired size needs an
	// explicit --desired: nothing is sent to EKS.
	for _, opts := range []ScaleOptions{{}, {CheckPDBs: true}, {CheckPDBs: true, Force: true}} {
		svc, api := scaleGateService(3, blockedCluster())

		err := svc.Scale(context.Background(), "prod", "workers", nil, nil, aws.Int32(1), opts)
		if err == nil || err.Error() != "--max 1 is below the current desired size 3; pass --desired to change the node count" {
			t.Fatalf("%+v: want the --max bounds error, got %v", opts, err)
		}
		var blocked *ScaleDownBlockedError
		if errors.As(err, &blocked) {
			t.Errorf("%+v: a bounds error is not a PDB refusal", opts)
		}
		if api.Calls.UpdateNodegroupConfig != 0 {
			t.Errorf("%+v: UpdateNodegroupConfig called %d times, want 0", opts, api.Calls.UpdateNodegroupConfig)
		}
	}
}

func TestScale_MinOnlyAboveCurrentFails(t *testing.T) {
	svc, api := scaleGateService(3, blockedCluster())

	err := svc.Scale(context.Background(), "prod", "workers", nil, aws.Int32(5), nil, ScaleOptions{})
	if err == nil || err.Error() != "--min 5 is above the current desired size 3; pass --desired to change the node count" {
		t.Fatalf("want the --min bounds error, got %v", err)
	}
	if api.Calls.UpdateNodegroupConfig != 0 {
		t.Errorf("UpdateNodegroupConfig called %d times, want 0", api.Calls.UpdateNodegroupConfig)
	}
}

func TestScale_MaxOnlyAboveCurrentProceeds(t *testing.T) {
	for _, maxSize := range []int32{3, 8} {
		svc, api := scaleGateService(3, blockedCluster())

		if err := svc.Scale(context.Background(), "prod", "workers", nil, nil, aws.Int32(maxSize), ScaleOptions{CheckPDBs: true}); err != nil {
			t.Fatalf("--max %d at or above the current desired size should proceed, got %v", maxSize, err)
		}
		if api.Calls.UpdateNodegroupConfig != 1 {
			t.Errorf("--max %d: UpdateNodegroupConfig called %d times, want 1", maxSize, api.Calls.UpdateNodegroupConfig)
		}
	}
}

func TestScale_MinOnlyBelowCurrentProceeds(t *testing.T) {
	for _, minSize := range []int32{0, 3} {
		svc, api := scaleGateService(3, blockedCluster())

		if err := svc.Scale(context.Background(), "prod", "workers", nil, aws.Int32(minSize), nil, ScaleOptions{CheckPDBs: true}); err != nil {
			t.Fatalf("--min %d at or below the current desired size should proceed, got %v", minSize, err)
		}
		if api.Calls.UpdateNodegroupConfig != 1 {
			t.Errorf("--min %d: UpdateNodegroupConfig called %d times, want 1", minSize, api.Calls.UpdateNodegroupConfig)
		}
	}
}

func TestScale_DesiredWithLowerMaxIsGated(t *testing.T) {
	// With --desired the bounds check steps aside and the PDB gate decides.
	svc, api := scaleGateService(3, blockedCluster())

	err := svc.Scale(context.Background(), "prod", "workers", aws.Int32(1), nil, aws.Int32(1), ScaleOptions{CheckPDBs: true})
	var blocked *ScaleDownBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("want *ScaleDownBlockedError, got %v", err)
	}
	if api.Calls.UpdateNodegroupConfig != 0 {
		t.Errorf("UpdateNodegroupConfig called %d times, want 0", api.Calls.UpdateNodegroupConfig)
	}
}

func TestScale_BoundsCheckDescribeErrorFails(t *testing.T) {
	svc, api := scaleGateService(3, blockedCluster())
	api.DescribeNodegroupFn = func(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		return nil, mocks.AccessDenied()
	}

	if err := svc.Scale(context.Background(), "prod", "workers", nil, nil, aws.Int32(1), ScaleOptions{}); err == nil {
		t.Fatal("a failed DescribeNodegroup must fail the bounds check")
	}
	if api.Calls.UpdateNodegroupConfig != 0 {
		t.Errorf("UpdateNodegroupConfig called %d times, want 0", api.Calls.UpdateNodegroupConfig)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// --wait: follow the EKS update, then verify the scaling config
// ──────────────────────────────────────────────────────────────────────────────

// scaleWaitFixture wires a service whose UpdateNodegroupConfig returns update
// u-1 and whose DescribeNodegroup reports cfg (ACTIVE). statuses script
// DescribeUpdate for u-1; tests may replace api.DescribeUpdateFn instead.
type scaleWaitFixture struct {
	svc *ServiceImpl
	api *mocks.EKSAPI

	mu sync.Mutex
	// describeNGAfterUpdates records how many DescribeUpdate calls had been
	// made at the first DescribeNodegroup of the wait (-1 = never). The
	// pre-change bounds check (before any DescribeUpdate) does not count.
	describeNGAfterUpdates int
	updates                int
}

func newScaleWaitFixture(cfg ekstypes.NodegroupScalingConfig, statuses ...ekstypes.UpdateStatus) *scaleWaitFixture {
	f := &scaleWaitFixture{describeNGAfterUpdates: -1}
	b := mocks.NewEKSAPI()
	if len(statuses) > 0 {
		b = b.WithUpdateStatuses("u-1", statuses...)
	}
	api := b.Build()
	scripted := api.DescribeUpdateFn
	api.DescribeUpdateFn = func(ctx context.Context, in *eks.DescribeUpdateInput, opts ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		f.mu.Lock()
		f.updates++
		f.mu.Unlock()
		if aws.ToString(in.Name) != "prod" || aws.ToString(in.NodegroupName) != "workers" {
			return nil, mocks.APIError("InvalidParameterException", "DescribeUpdate needs Name and NodegroupName for a nodegroup update")
		}
		return scripted(ctx, in, opts...)
	}
	api.UpdateNodegroupConfigFn = func(context.Context, *eks.UpdateNodegroupConfigInput, ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error) {
		return &eks.UpdateNodegroupConfigOutput{Update: &ekstypes.Update{Id: aws.String("u-1"), Status: ekstypes.UpdateStatusInProgress}}, nil
	}
	api.DescribeNodegroupFn = func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		f.mu.Lock()
		if f.describeNGAfterUpdates < 0 && f.updates > 0 {
			f.describeNGAfterUpdates = f.updates
		}
		f.mu.Unlock()
		c := cfg
		return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{
			NodegroupName: in.NodegroupName,
			Status:        ekstypes.NodegroupStatusActive,
			ScalingConfig: &c,
		}}, nil
	}
	f.api = api
	f.svc = newTestService(api)
	f.svc.scalePollInterval = time.Millisecond
	return f
}

func sizes(desired, mn, mx int32) ekstypes.NodegroupScalingConfig {
	return ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(desired), MinSize: aws.Int32(mn), MaxSize: aws.Int32(mx)}
}

func waitOpts() ScaleOptions { return ScaleOptions{Wait: true, Timeout: 10 * time.Second} }

func TestScaleWait_FailedUpdateErrors(t *testing.T) {
	f := newScaleWaitFixture(sizes(3, 1, 8))
	f.api.DescribeUpdateFn = func(context.Context, *eks.DescribeUpdateInput, ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		return &eks.DescribeUpdateOutput{Update: &ekstypes.Update{
			Id:     aws.String("u-1"),
			Status: ekstypes.UpdateStatusFailed,
			Errors: []ekstypes.ErrorDetail{{ErrorCode: ekstypes.ErrorCode("AsgInstanceLaunchFailures"), ErrorMessage: aws.String("no capacity")}},
		}}, nil
	}

	err := f.svc.Scale(context.Background(), "prod", "workers", nil, nil, aws.Int32(8), waitOpts())
	if err == nil {
		t.Fatal("a Failed update must fail the wait")
	}
	for _, want := range []string{"u-1", "Failed", "no capacity"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

func TestScaleWait_CancelledUpdateErrors(t *testing.T) {
	f := newScaleWaitFixture(sizes(3, 1, 8), ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusCancelled)

	err := f.svc.Scale(context.Background(), "prod", "workers", nil, nil, aws.Int32(8), waitOpts())
	if err == nil || !strings.Contains(err.Error(), "Cancelled") {
		t.Fatalf("a Cancelled update must fail the wait, got %v", err)
	}
}

func TestScaleWait_MaxOnlySucceedsOnlyAfterSuccessful(t *testing.T) {
	f := newScaleWaitFixture(sizes(3, 1, 8),
		ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusSuccessful)

	if err := f.svc.Scale(context.Background(), "prod", "workers", nil, nil, aws.Int32(8), waitOpts()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updates != 3 {
		t.Errorf("DescribeUpdate called %d times, want 3 (poll until Successful)", f.updates)
	}
	if f.describeNGAfterUpdates != 3 {
		t.Errorf("DescribeNodegroup first called after %d DescribeUpdate calls, want 3 (only after Successful)", f.describeNGAfterUpdates)
	}
}

func TestScaleWait_SuccessfulButConfigMismatchErrors(t *testing.T) {
	// EKS reports Successful, but the nodegroup still has max 5.
	f := newScaleWaitFixture(sizes(3, 1, 5), ekstypes.UpdateStatusSuccessful)

	err := f.svc.Scale(context.Background(), "prod", "workers", aws.Int32(3), aws.Int32(1), aws.Int32(8), waitOpts())
	if err == nil {
		t.Fatal("a config mismatch after Successful must fail the wait")
	}
	if !strings.Contains(err.Error(), "max size is 5, not 8") {
		t.Errorf("error should name the mismatch: %v", err)
	}
}

func TestScaleWait_NetworkErrorKeepsPolling(t *testing.T) {
	f := newScaleWaitFixture(sizes(4, 1, 8))
	var mu sync.Mutex
	calls := 0
	netErr := &url.Error{Op: "Post", URL: "https://eks.us-east-1.amazonaws.com", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "eks.us-east-1.amazonaws.com", IsNotFound: true},
	}}
	f.api.DescribeUpdateFn = func(context.Context, *eks.DescribeUpdateInput, ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		switch calls {
		case 1:
			return &eks.DescribeUpdateOutput{Update: &ekstypes.Update{Id: aws.String("u-1"), Status: ekstypes.UpdateStatusInProgress}}, nil
		case 2:
			return nil, netErr
		default:
			return &eks.DescribeUpdateOutput{Update: &ekstypes.Update{Id: aws.String("u-1"), Status: ekstypes.UpdateStatusSuccessful}}, nil
		}
	}

	if err := f.svc.Scale(context.Background(), "prod", "workers", aws.Int32(4), nil, nil, waitOpts()); err != nil {
		t.Fatalf("a network blip must not fail the wait, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 3 {
		t.Errorf("DescribeUpdate called %d times, want at least 3", calls)
	}
}

func TestScaleWait_AccessDeniedFailsFast(t *testing.T) {
	f := newScaleWaitFixture(sizes(3, 1, 8))
	f.api.DescribeUpdateFn = func(context.Context, *eks.DescribeUpdateInput, ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		return nil, mocks.AccessDenied()
	}

	start := time.Now()
	err := f.svc.Scale(context.Background(), "prod", "workers", nil, nil, aws.Int32(8), waitOpts())
	if err == nil {
		t.Fatal("AccessDenied must fail the wait")
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Errorf("AccessDenied should fail fast, not time out: %v", err)
	}
	if f.api.Calls.DescribeUpdate != 1 {
		t.Errorf("DescribeUpdate called %d times, want 1 (no retries on a permanent error)", f.api.Calls.DescribeUpdate)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("AccessDenied took %s; it should fail at once", time.Since(start))
	}
}

func TestScaleWait_MissingUpdateIDErrors(t *testing.T) {
	f := newScaleWaitFixture(sizes(3, 1, 8), ekstypes.UpdateStatusSuccessful)
	f.api.UpdateNodegroupConfigFn = func(context.Context, *eks.UpdateNodegroupConfigInput, ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error) {
		return &eks.UpdateNodegroupConfigOutput{}, nil
	}

	err := f.svc.Scale(context.Background(), "prod", "workers", nil, nil, aws.Int32(8), waitOpts())
	if err == nil || !strings.Contains(err.Error(), "no update ID") {
		t.Fatalf("want a missing update ID error, got %v", err)
	}
}

func TestScaleWait_TimeoutWhileInProgress(t *testing.T) {
	f := newScaleWaitFixture(sizes(3, 1, 8), ekstypes.UpdateStatusInProgress)

	err := f.svc.Scale(context.Background(), "prod", "workers", nil, nil, aws.Int32(8), ScaleOptions{Wait: true, Timeout: 50 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out waiting for nodegroup") || !strings.Contains(err.Error(), "last status InProgress") {
		t.Fatalf("want a timeout naming the last status, got %v", err)
	}
}

// Ctrl+C during the wait is an interrupt, not a timeout: the error says
// "interrupted while waiting", and only a passed deadline says "timed out".
func TestScaleWait_CancelIsNotATimeout(t *testing.T) {
	f := newScaleWaitFixture(sizes(3, 1, 8), ekstypes.UpdateStatusInProgress)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scripted := f.api.DescribeUpdateFn
	f.api.DescribeUpdateFn = func(c context.Context, in *eks.DescribeUpdateInput, opts ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		cancel() // the user presses Ctrl+C during the first poll
		return scripted(c, in, opts...)
	}

	err := f.svc.Scale(ctx, "prod", "workers", nil, nil, aws.Int32(8), ScaleOptions{Wait: true, Timeout: time.Hour})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "interrupted while waiting for nodegroup") {
		t.Fatalf("want an interrupt error, got %v", err)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Errorf("a cancelled wait is reported as a timeout: %v", err)
	}
}

func TestScaleWait_DesiredWaitsForActive(t *testing.T) {
	f := newScaleWaitFixture(sizes(5, 1, 8), ekstypes.UpdateStatusSuccessful)
	var mu sync.Mutex
	polls := 0
	f.api.DescribeNodegroupFn = func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		polls++
		status := ekstypes.NodegroupStatusUpdating
		if polls >= 3 {
			status = ekstypes.NodegroupStatusActive
		}
		cfg := sizes(5, 1, 8)
		return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{NodegroupName: in.NodegroupName, Status: status, ScalingConfig: &cfg}}, nil
	}

	if err := f.svc.Scale(context.Background(), "prod", "workers", aws.Int32(5), nil, nil, waitOpts()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if polls != 3 {
		t.Errorf("DescribeNodegroup called %d times, want 3 (until ACTIVE)", polls)
	}
}
