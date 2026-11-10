//go:build livecheck

package health_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// TestLiveAvailability runs the instance-type check of a roll against the
// subnets of the default VPC, with a nodegroup of common types and one
// type that does not exist. A read that fails gives a command no notice,
// so here it fails the test. The
// type that does not exist must be reported in every zone. An account
// with no default VPC has nothing to check, and the test is skipped.
//
// It runs in us-east-1 with read-only calls that cost nothing:
//
//   - ec2:DescribeSubnets, for the subnets of the default VPC
//   - ec2:DescribeSubnets, for the subnets of the nodegroup (the check)
//   - ec2:DescribeInstanceTypeOfferings, paged (the check)
func TestLiveAvailability(t *testing.T) {
	cfg := awsxtest.LiveConfig(t, "us-east-1")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	clients := awsx.NewProvider(cfg).Clients("us-east-1")
	subnets, err := clients.EC2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{{Name: aws.String("default-for-az"), Values: []string{"true"}}},
	})
	if err != nil {
		t.Fatalf("ec2:DescribeSubnets: %v", err)
	}
	var ids []string
	for _, s := range subnets.Subnets {
		ids = append(ids, aws.ToString(s.SubnetId))
		t.Logf("subnet %s in %s", aws.ToString(s.SubnetId), aws.ToString(s.AvailabilityZone))
	}
	if len(ids) == 0 {
		t.Skip("the account has no default VPC in us-east-1: no subnets to check")
	}
	const unknown = "not-a-type.large"
	snap := &fleet.Snapshot{Nodegroups: []fleet.Nodegroup{{
		Name:          "live",
		InstanceTypes: []string{"m5.large", "t3.medium", "m7g.large", "p5.48xlarge", unknown},
		Subnets:       ids,
	}}}
	notices, err := health.InstanceTypeNotices(ctx, clients, snap, []string{"live"})
	if err != nil {
		t.Fatalf("a read failed: %v", err)
	}
	t.Logf("%q", notices)
	if len(notices) != 1 {
		t.Fatalf("notices = %q, want one for the nodegroup live: %s is offered nowhere", notices, unknown)
	}
	if n := strings.Count(notices[0], unknown+" in "); n != len(ids) {
		t.Errorf("the notice names %s in %d zones, want all %d", unknown, n, len(ids))
	}
	if !strings.Contains(notices[0], "nodegroup live ") {
		t.Errorf("the notice does not name the nodegroup live: %s", notices[0])
	}
}
