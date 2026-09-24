package health

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// Every failure in the verdict carries the checker's region, so the nested
// health.failures list matches the command's top-level failures and its
// stderr lines (output.md: a nested list is a subset of the top-level one).
func TestRunAllChecks_FailuresCarryRegion(t *testing.T) {
	api := mocks.NewEKSAPI().WithCluster("prod", "1.32").
		WithNodegroup("ng-a", "1.32", ekstypes.AMITypesAl2023X8664Standard).Build()
	api.DescribeNodegroupFn = func(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		return nil, mocks.AccessDenied()
	}
	hc := &HealthChecker{eksClient: api, region: "us-east-1"}

	summary := hc.RunAllChecks(context.Background(), "prod")
	if len(summary.Failures) == 0 {
		t.Fatal("want the DescribeNodegroup failure in the verdict")
	}
	for _, f := range summary.Failures {
		if f.Region != "us-east-1" {
			t.Errorf("failure %s %s: region = %q, want us-east-1", f.Kind, f.Name, f.Region)
		}
	}
}
