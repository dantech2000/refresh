package upgrade

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// captureNodegroupRolls records UpdateNodegroupVersion calls in order.
func captureNodegroupRolls(m *mocks.EKSAPI) *[]eks.UpdateNodegroupVersionInput {
	var mu sync.Mutex
	captured := &[]eks.UpdateNodegroupVersionInput{}
	m.UpdateNodegroupVersionFn = func(_ context.Context, in *eks.UpdateNodegroupVersionInput, _ ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error) {
		mu.Lock()
		*captured = append(*captured, *in)
		mu.Unlock()
		return &eks.UpdateNodegroupVersionOutput{Update: &ekstypes.Update{
			Id:     aws.String("u-" + aws.ToString(in.NodegroupName)),
			Status: ekstypes.UpdateStatusInProgress,
		}}, nil
	}
	return captured
}

// Acceptance (REF-101): a multi-nodegroup cluster rolls in order with gates,
// version and idempotency token set on each call.
func TestUpgradeNodegroups_RollsInOrderWithGates(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("workers-b", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("workers-c", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithDescribeUpdate(ekstypes.UpdateStatusSuccessful).
		Build()
	rolls := captureNodegroupRolls(m)

	var mu sync.Mutex
	var gated []string
	gate := func(_ context.Context, ng string) error {
		mu.Lock()
		gated = append(gated, ng)
		mu.Unlock()
		return nil
	}

	svc := newTestService(m)
	err := svc.UpgradeNodegroups(context.Background(), "prod-east", "1.32",
		NodegroupRollOptions{Gate: gate}, nil)
	if err != nil {
		t.Fatalf("UpgradeNodegroups: %v", err)
	}

	if len(*rolls) != 3 {
		t.Fatalf("rolls = %d, want 3", len(*rolls))
	}
	for i, want := range []string{"workers-a", "workers-b", "workers-c"} {
		in := (*rolls)[i]
		if aws.ToString(in.NodegroupName) != want {
			t.Fatalf("roll %d = %s, want %s (listing order)", i, aws.ToString(in.NodegroupName), want)
		}
		if aws.ToString(in.Version) != "1.32" {
			t.Fatalf("roll %d version = %q, want 1.32", i, aws.ToString(in.Version))
		}
		if in.ClientRequestToken == nil || *in.ClientRequestToken == "" {
			t.Fatalf("roll %d missing ClientRequestToken", i)
		}
	}
	// The gate ran before every roll.
	if len(gated) != 3 {
		t.Fatalf("gate ran %d times, want 3", len(gated))
	}
}

// Acceptance (REF-101): a custom-AMI nodegroup appears as a manual-action
// item, not an API call.
func TestUpgradeNodegroups_CustomAMIIsManualNotAPI(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithNodegroup("byo-ami", "1.31", ekstypes.AMITypesCustom).
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithDescribeUpdate(ekstypes.UpdateStatusSuccessful).
		Build()
	rolls := captureNodegroupRolls(m)

	var lines []string
	progress := func(format string, args ...any) {
		lines = append(lines, sprintf(format, args...))
	}

	svc := newTestService(m)
	if err := svc.UpgradeNodegroups(context.Background(), "prod-east", "1.32",
		NodegroupRollOptions{}, progress); err != nil {
		t.Fatalf("UpgradeNodegroups: %v", err)
	}

	if len(*rolls) != 1 || aws.ToString((*rolls)[0].NodegroupName) != "workers-a" {
		t.Fatalf("rolls = %+v, want only workers-a", rolls)
	}
	manualMentioned := false
	for _, l := range lines {
		if strings.Contains(l, "byo-ami") && strings.Contains(l, "MANUAL") {
			manualMentioned = true
		}
	}
	if !manualMentioned {
		t.Fatalf("progress lines %v should surface byo-ami as a manual action", lines)
	}
}

// A gate failure halts the remaining nodegroups with a clear message.
func TestUpgradeNodegroups_GateFailureHaltsRemaining(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("workers-b", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("workers-c", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithDescribeUpdate(ekstypes.UpdateStatusSuccessful).
		Build()
	rolls := captureNodegroupRolls(m)

	gate := func(_ context.Context, ng string) error {
		if ng == "workers-b" {
			return sprintfErr("nodegroup %s has 2 unhealthy nodes", ng)
		}
		return nil
	}

	svc := newTestService(m)
	err := svc.UpgradeNodegroups(context.Background(), "prod-east", "1.32",
		NodegroupRollOptions{Gate: gate}, nil)
	if err == nil || !strings.Contains(err.Error(), "workers-b") {
		t.Fatalf("err = %v, want gate failure naming workers-b", err)
	}
	if !strings.Contains(err.Error(), "remaining nodegroups not attempted") {
		t.Fatalf("err = %v, want halt message", err)
	}
	if len(*rolls) != 1 {
		t.Fatalf("rolls = %d, want 1 (workers-a only; halt before b, c never attempted)", len(*rolls))
	}
}

// The built-in gate blocks rolls of nodegroups that aren't ACTIVE.
func TestUpgradeNodegroups_DefaultGateChecksHealth(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithDescribeUpdate(ekstypes.UpdateStatusSuccessful).
		Build()
	// One degraded nodegroup, hand-wired (the builder always returns ACTIVE).
	m.ListNodegroupsFn = func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
		return &eks.ListNodegroupsOutput{Nodegroups: []string{"workers-a"}}, nil
	}
	m.DescribeNodegroupFn = func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{
			NodegroupName: in.NodegroupName,
			Version:       aws.String("1.31"),
			AmiType:       ekstypes.AMITypesAl2023X8664Standard,
			Status:        ekstypes.NodegroupStatusDegraded,
		}}, nil
	}
	rolls := captureNodegroupRolls(m)

	svc := newTestService(m)
	err := svc.UpgradeNodegroups(context.Background(), "prod-east", "1.32",
		NodegroupRollOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), "DEGRADED") {
		t.Fatalf("err = %v, want DEGRADED gate failure", err)
	}
	if len(*rolls) != 0 {
		t.Fatalf("rolls = %d, want 0", len(*rolls))
	}
}

// Skip patterns and already-current nodegroups are not rolled.
func TestUpgradeNodegroups_SkipAndAlreadyCurrent(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithNodegroup("workers-a", "1.32", ekstypes.AMITypesAl2023X8664Standard). // current
		WithNodegroup("spot-pool", "1.31", ekstypes.AMITypesAl2023X8664Standard). // skipped
		WithNodegroup("workers-b", "1.31", ekstypes.AMITypesAl2023X8664Standard). // rolled
		WithDescribeUpdate(ekstypes.UpdateStatusSuccessful).
		Build()
	rolls := captureNodegroupRolls(m)

	svc := newTestService(m)
	if err := svc.UpgradeNodegroups(context.Background(), "prod-east", "1.32",
		NodegroupRollOptions{SkipPatterns: []string{"spot"}}, nil); err != nil {
		t.Fatalf("UpgradeNodegroups: %v", err)
	}
	if len(*rolls) != 1 || aws.ToString((*rolls)[0].NodegroupName) != "workers-b" {
		t.Fatalf("rolls = %+v, want only workers-b", rolls)
	}
}

// Force passes through to the API call.
func TestUpgradeNodegroups_ForcePassthrough(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithDescribeUpdate(ekstypes.UpdateStatusSuccessful).
		Build()
	rolls := captureNodegroupRolls(m)

	svc := newTestService(m)
	if err := svc.UpgradeNodegroups(context.Background(), "prod-east", "1.32",
		NodegroupRollOptions{Force: true}, nil); err != nil {
		t.Fatalf("UpgradeNodegroups: %v", err)
	}
	if len(*rolls) != 1 || !(*rolls)[0].Force {
		t.Fatalf("rolls = %+v, want Force=true", rolls)
	}
}

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

func sprintfErr(format string, args ...any) error { return fmt.Errorf(format, args...) }
