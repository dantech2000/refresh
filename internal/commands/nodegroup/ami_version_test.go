package nodegroup

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	awsinternal "github.com/dantech2000/refresh/internal/aws"
)

// With the control plane on 1.32 and a nodegroup still on 1.31, the update
// (no explicit Version) can only roll to the latest 1.31 AMI. A nodegroup
// already on that AMI must be skipped; one on an older 1.31 AMI must not.
func TestLatestAMISkipPredicate_UsesNodegroupVersion(t *testing.T) {
	latest := awsinternal.NewLatestAMICache(func(_ context.Context, v string, _ ekstypes.AMITypes) (string, error) {
		return map[string]string{"1.31": "ami-131-latest", "1.32": "ami-132-latest"}[v], nil
	})
	current := map[string]string{
		"ng-latest":     "ami-131-latest",
		"ng-old":        "ami-131-older",
		"ng-no-version": "ami-132-latest",
	}
	skip := latestAMISkipPredicate(context.Background(), "1.32", latest,
		func(_ context.Context, ng *ekstypes.Nodegroup) string { return current[aws.ToString(ng.NodegroupName)] })

	cases := []struct {
		name    string
		version *string
		want    bool
	}{
		{"ng-latest", aws.String("1.31"), true},
		{"ng-old", aws.String("1.31"), false},
		{"ng-no-version", nil, true}, // falls back to the cluster version
	}
	for _, tc := range cases {
		ng := &ekstypes.Nodegroup{
			NodegroupName: aws.String(tc.name),
			Version:       tc.version,
			AmiType:       ekstypes.AMITypesAl2023X8664Standard,
		}
		if got := skip(ng); got != tc.want {
			t.Errorf("%s: skip = %v, want %v", tc.name, got, tc.want)
		}
	}
}
