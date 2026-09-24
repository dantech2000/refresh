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

// An empty version leaves Version out of the request.
func TestStartNodegroupRoll_EmptyVersionOmitted(t *testing.T) {
	api := &recordingUpdater{}
	if _, err := StartNodegroupRoll(context.Background(), api, "prod", "ng-a", "", false); err != nil {
		t.Fatalf("StartNodegroupRoll: %v", err)
	}
	if v := api.inputs[len(api.inputs)-1].Version; v != nil {
		t.Errorf("Version = %q, want nil", *v)
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
