package nodegroup

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	asgtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks"
)

type fakeActivities struct {
	byGroup map[string][]asgtypes.Activity
	err     error
}

func (f fakeActivities) DescribeScalingActivities(_ context.Context, in *autoscaling.DescribeScalingActivitiesInput, _ ...func(*autoscaling.Options)) (*autoscaling.DescribeScalingActivitiesOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &autoscaling.DescribeScalingActivitiesOutput{Activities: f.byGroup[aws.ToString(in.AutoScalingGroupName)]}, nil
}

// A launch the vCPU quota refused is a failed activity: it is returned, on
// one line, with the other groups' failures in time order. Successful and
// older activities are not.
func TestScalingFailures(t *testing.T) {
	start := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	act := func(id string, at time.Time, code asgtypes.ScalingActivityStatusCode, msg string) asgtypes.Activity {
		return asgtypes.Activity{ActivityId: aws.String(id), StartTime: aws.Time(at), StatusCode: code, StatusMessage: aws.String(msg)}
	}
	api := fakeActivities{byGroup: map[string][]asgtypes.Activity{
		"asg-a": {
			act("a3", start.Add(3*time.Minute), asgtypes.ScalingActivityStatusCodeFailed, "Could not launch On-Demand Instances.\nVcpuLimitExceeded - You have requested more vCPU capacity than your current vCPU limit of 8 allows."),
			act("a2", start.Add(2*time.Minute), asgtypes.ScalingActivityStatusCodeSuccessful, "ok"),
			act("a0", start.Add(-time.Minute), asgtypes.ScalingActivityStatusCodeFailed, "before the roll"),
		},
		"asg-b": {act("b1", start.Add(time.Minute), asgtypes.ScalingActivityStatusCodeCancelled, "cancelled")},
	}}
	got, err := ScalingFailures(context.Background(), api, []string{"asg-a", "asg-b"}, start)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "b1" || got[1].ID != "a3" {
		t.Fatalf("failures = %+v, want b1 then a3", got)
	}
	if want := "Could not launch On-Demand Instances. VcpuLimitExceeded - You have requested more vCPU capacity than your current vCPU limit of 8 allows."; got[1].Message != want || got[1].Group != "asg-a" {
		t.Errorf("a3 = %+v", got[1])
	}

	_, err = ScalingFailures(context.Background(), fakeActivities{err: mocks.AccessDenied()}, []string{"asg-a"}, start)
	if diag.OperationOf(err) != diag.OpDescribeScalingActivities {
		t.Errorf("err = %v, want it tagged %s", err, diag.OpDescribeScalingActivities)
	}
}

// Each failure is reported once, however many reads return it, and a
// permanent error ends the watch.
func TestWatchScalingFailures(t *testing.T) {
	reads := [][]ScalingFailure{
		{{ID: "1"}},
		{{ID: "1"}, {ID: "2"}},
		{{ID: "1"}, {ID: "2"}},
	}
	var reported []string
	n := 0
	WatchScalingFailures(context.Background(), time.Millisecond, func(context.Context) ([]ScalingFailure, error) {
		if n == len(reads) {
			return nil, mocks.AccessDenied() // permanent: the watch ends
		}
		n++
		return reads[n-1], nil
	}, func(f ScalingFailure) { reported = append(reported, f.ID) })
	if len(reported) != 2 || reported[0] != "1" || reported[1] != "2" {
		t.Fatalf("reported = %v, want [1 2]", reported)
	}
}
