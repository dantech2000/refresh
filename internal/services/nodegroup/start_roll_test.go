package nodegroup

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// recordingUpdater throttles its first call and records every request.
type recordingUpdater struct {
	inputs []*eks.UpdateNodegroupVersionInput
}

func (r *recordingUpdater) UpdateNodegroupVersion(_ context.Context, in *eks.UpdateNodegroupVersionInput, _ ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error) {
	r.inputs = append(r.inputs, in)
	if len(r.inputs) == 1 {
		return nil, mocks.Throttling()
	}
	return &eks.UpdateNodegroupVersionOutput{Update: &ekstypes.Update{Id: aws.String("u-1")}}, nil
}

// A retried start is the same request: one idempotency token for every
// attempt, so a throttled or dropped call can't start a second roll.
func TestStartNodegroupRoll_RetriesKeepOneToken(t *testing.T) {
	api := &recordingUpdater{}
	update, err := StartNodegroupRoll(context.Background(), api, "prod", "ng-a", "1.31", true)
	if err != nil {
		t.Fatalf("StartNodegroupRoll: %v", err)
	}
	if aws.ToString(update.Id) != "u-1" {
		t.Errorf("update = %+v", update)
	}
	if len(api.inputs) != 2 {
		t.Fatalf("attempts = %d, want 2 (one throttled)", len(api.inputs))
	}
	first, second := api.inputs[0], api.inputs[1]
	if tok := aws.ToString(first.ClientRequestToken); tok == "" || tok != aws.ToString(second.ClientRequestToken) {
		t.Errorf("tokens = %q, %q; want one non-empty token for both attempts", tok, aws.ToString(second.ClientRequestToken))
	}
	if aws.ToString(first.Version) != "1.31" || !first.Force {
		t.Errorf("input = version %q force %v, want 1.31 true", aws.ToString(first.Version), first.Force)
	}
}

// Without a version to pin, no request is sent: EKS would read an omitted
// Version as the cluster's version and could upgrade the nodegroup's minor.
func TestStartNodegroupRoll_RefusesEmptyVersion(t *testing.T) {
	api := &recordingUpdater{}
	if _, err := StartNodegroupRoll(context.Background(), api, "prod", "ng-a", "", false); err == nil {
		t.Fatal("StartNodegroupRoll with no version: want an error")
	}
	if len(api.inputs) != 0 {
		t.Errorf("requests sent = %d, want 0", len(api.inputs))
	}
}

// nodegroup update pins the nodegroup's own version. A nodegroup that
// reports none (nil or "") is not rolled at all.
func TestStartVersionUpdate_NoNodegroupVersionStartsNothing(t *testing.T) {
	for _, v := range []*string{nil, aws.String("")} {
		api := mocks.NewEKSAPI().WithCluster("prod", "1.32").WithNodegroup("ng-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).Build()
		describe := api.DescribeNodegroupFn
		api.DescribeNodegroupFn = func(ctx context.Context, in *eks.DescribeNodegroupInput, o ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			out, err := describe(ctx, in, o...)
			if err == nil {
				ng := *out.Nodegroup
				ng.Version = v
				out = &eks.DescribeNodegroupOutput{Nodegroup: &ng}
			}
			return out, err
		}
		svc := newTestService(api)
		if _, err := svc.StartVersionUpdate(context.Background(), "prod", "ng-a", VersionUpdateOptions{}); err == nil {
			t.Errorf("version %v: want an error", v)
		}
		if n := api.Calls.UpdateNodegroupVersion; n != 0 {
			t.Errorf("version %v: UpdateNodegroupVersion calls = %d, want 0", v, n)
		}
	}
}

// Separate calls are separate requests, each with its own token.
func TestStartNodegroupRoll_NewTokenPerCall(t *testing.T) {
	a, b := &recordingUpdater{}, &recordingUpdater{}
	_, _ = StartNodegroupRoll(context.Background(), a, "prod", "ng-a", "1.31", false)
	_, _ = StartNodegroupRoll(context.Background(), b, "prod", "ng-a", "1.31", false)
	if aws.ToString(a.inputs[0].ClientRequestToken) == aws.ToString(b.inputs[0].ClientRequestToken) {
		t.Error("two calls shared one idempotency token")
	}
}
