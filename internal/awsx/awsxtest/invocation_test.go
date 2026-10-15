package awsxtest_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// Each SDK call carries an invocation ID that its retries share, so a test
// can count calls rather than attempts. Two calls have two IDs.
func TestRequestsCarryTheInvocationID(t *testing.T) {
	srv := awsxtest.NewServer(fleettest.New())
	defer srv.Close()
	cfg := srv.Config("us-east-1")
	if _, err := eks.NewFromConfig(cfg).ListClusters(t.Context(), &eks.ListClustersInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ec2.NewFromConfig(cfg).DescribeInstances(t.Context(), &ec2.DescribeInstancesInput{}); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	for _, r := range reqs {
		if r.Invocation == "" {
			t.Errorf("%s has no invocation ID", r.Operation)
		}
	}
	if reqs[0].Invocation == reqs[1].Invocation {
		t.Errorf("two calls share the invocation ID %s", reqs[0].Invocation)
	}
}
