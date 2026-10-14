//go:build livecheck

package health_test

import (
	"context"
	"slices"
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
// type that does not exist. A read that fails makes the check Skipped,
// which a command shows as no warning, so here it fails the test. The
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
	results := health.Run(ctx, []health.Check{health.Availability()}, &health.Input{
		Snapshot: snap, Targets: []string{"live"}, Clients: clients, Now: time.Now(),
	})
	r := results[0]
	t.Logf("%s: %s", r.Outcome, r.Message)
	if r.Outcome != health.Warn {
		t.Fatalf("outcome %s (%s), want Warn: %s is offered nowhere", r.Outcome, r.Message, unknown)
	}
	if n := strings.Count(r.Message, unknown+" in "); n != len(ids) {
		t.Errorf("the check names %s in %d zones, want all %d", unknown, n, len(ids))
	}
	if !slices.Equal(r.Findings, []string{"live"}) {
		t.Errorf("findings = %v, want the nodegroup live", r.Findings)
	}
}
