package render

import (
	"slices"
	"testing"

	"github.com/dantech2000/refresh/internal/diag"
)

func TestFailureText(t *testing.T) {
	cases := []struct {
		f    diag.Failure
		want string
	}{
		{
			diag.Failure{Kind: diag.KindNodegroup, Name: "web", Cluster: "prod", Region: "us-east-1", Reason: diag.ReasonThrottled, Error: "ThrottlingException: Rate exceeded"},
			"nodegroup prod/web (us-east-1): Throttled: ThrottlingException: Rate exceeded",
		},
		{
			diag.Failure{Kind: diag.KindRegion, Name: "ap-east-1", Region: "ap-east-1", Reason: diag.ReasonRegionUnavailable, Error: "x"},
			"region ap-east-1: RegionUnavailable: x",
		},
		{
			diag.Failure{Kind: diag.KindAddon, Name: "vpc-cni", Reason: diag.ReasonNotAttempted},
			"addon vpc-cni: NotAttempted",
		},
	}
	for _, c := range cases {
		if got := FailureText(c.f); got != c.want {
			t.Errorf("FailureText = %q, want %q", got, c.want)
		}
	}
}

func TestFailureSection(t *testing.T) {
	th := New(ColorNone, false)
	if got := th.FailureSection(nil); got != nil {
		t.Errorf("no failures = %q, want nil", got)
	}
	fs := []diag.Failure{
		{Kind: diag.KindNodegroup, Name: "web", Cluster: "prod", Reason: diag.ReasonAccessDenied, Error: "denied"},
		{Kind: diag.KindCluster, Name: "prod", Reason: diag.ReasonThrottled, Error: "slow"},
	}
	want := []string{
		"",
		"INCOMPLETE DATA",
		"[?] cluster prod: Throttled: slow",
		"[?] nodegroup prod/web: AccessDenied: denied",
	}
	if got := th.FailureSection(fs); !slices.Equal(got, want) {
		t.Errorf("FailureSection =\n%q\nwant\n%q", got, want)
	}
	if fs[0].Kind != diag.KindNodegroup {
		t.Error("FailureSection reordered the caller's slice")
	}
}
