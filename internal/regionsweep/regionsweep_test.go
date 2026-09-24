package regionsweep

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks"
)

// fixture answers us-east-1 and eu-west-1, is throttled in ap-south-1, and
// is closed (AccessDenied from an SCP) in sa-east-1 and af-south-1.
func fixture(_ context.Context, region string) (string, error) {
	switch region {
	case "us-east-1", "eu-west-1":
		return "clusters-" + region, nil
	case "ap-south-1":
		return "", mocks.Throttling()
	default:
		return "", mocks.AccessDenied()
	}
}

var regions = []string{"us-east-1", "sa-east-1", "ap-south-1", "af-south-1", "eu-west-1"}

func TestRun_DefaultSweepSkipsClosedRegions(t *testing.T) {
	res := Run(context.Background(), regions, Options{SkipInaccessible: true}, fixture)

	var answered []string
	for _, a := range res.Answered {
		answered = append(answered, a.Region)
		if a.Value != "clusters-"+a.Region {
			t.Errorf("%s: value = %q", a.Region, a.Value)
		}
	}
	if !slices.Equal(answered, []string{"us-east-1", "eu-west-1"}) {
		t.Errorf("answered = %v, want region order", answered)
	}
	if !slices.Equal(res.Skipped, []string{"af-south-1", "sa-east-1"}) {
		t.Errorf("skipped = %v, want sorted closed regions", res.Skipped)
	}
	for _, r := range res.Skipped {
		if res.SkipErrors[r] == nil {
			t.Errorf("%s: no skip error recorded", r)
		}
	}
	if len(res.Failed) != 1 || len(res.Errors) != 1 {
		t.Fatalf("failed = %+v, errors = %v; want one of each", res.Failed, res.Errors)
	}
	f := res.Failed[0]
	if f.Kind != diag.KindRegion || f.Name != "ap-south-1" || f.Region != "ap-south-1" ||
		f.Operation != diag.OpListClusters || f.Reason != diag.ReasonThrottled {
		t.Errorf("failure = %+v", f)
	}
	var apiErr smithy.APIError
	if !errors.As(res.Errors[0], &apiErr) || apiErr.ErrorCode() != "ThrottlingException" {
		t.Errorf("error = %v, want the region's own throttling error", res.Errors[0])
	}
}

// A region the user named never disappears: without SkipInaccessible a
// closed region is a failure, and failures pair with their errors in order.
func TestRun_NamedRegionsNeverSkip(t *testing.T) {
	res := Run(context.Background(), regions, Options{}, fixture)
	if len(res.Skipped) != 0 {
		t.Errorf("skipped = %v, want none", res.Skipped)
	}
	var failed []string
	for i, f := range res.Failed {
		failed = append(failed, f.Name)
		if res.Errors[i] == nil {
			t.Errorf("%s: no error paired with the failure", f.Name)
		}
	}
	if !slices.Equal(failed, []string{"sa-east-1", "ap-south-1", "af-south-1"}) {
		t.Errorf("failed = %v, want region order", failed)
	}
}

// Regions the sweep never started because ctx ended are NotAttempted
// failures, so an interrupted sweep never looks complete.
func TestRun_CancelledSweepMarksRegionsNotAttempted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := Run(ctx, regions, Options{SkipInaccessible: true}, fixture)
	if len(res.Answered) != 0 || len(res.Failed) != len(regions) || len(res.Errors) != len(regions) {
		t.Fatalf("answered = %d, failed = %d, errors = %d; want every region failed", len(res.Answered), len(res.Failed), len(res.Errors))
	}
	for i, f := range res.Failed {
		if f.Reason != diag.ReasonNotAttempted || f.Name != regions[i] || !errors.Is(res.Errors[i], context.Canceled) {
			t.Errorf("failure %d = %+v, err = %v", i, f, res.Errors[i])
		}
	}
}
