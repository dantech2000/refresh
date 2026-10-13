package change

import (
	"context"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
)

// availabilityNotice returns the notice about the instance types of a
// nodegroup that EC2 does not offer in an availability zone of its
// subnets, or "" when every type is offered everywhere. New nodes cannot
// launch in such a zone.
//
// It reads the subnets of the nodegroup and the offerings of its instance
// types. It is advice beside the gate, so a read that fails gives no
// notice and no failure. It checks that a type is offered, and not that
// EC2 has capacity for it now: only a launch shows that.
func availabilityNotice(ctx context.Context, c *awsx.Clients, n fleet.Nodegroup) string {
	// A nodegroup with a custom launch template reports no instance types.
	if len(n.InstanceTypes) == 0 || len(n.Subnets) == 0 {
		return ""
	}
	subnets, err := awsx.ListAll(ctx,
		func(ctx context.Context, token *string) (*ec2.DescribeSubnetsOutput, error) {
			return c.EC2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: n.Subnets, NextToken: token})
		},
		func(out *ec2.DescribeSubnetsOutput) ([]ec2types.Subnet, *string) { return out.Subnets, out.NextToken },
	)
	if err != nil {
		return ""
	}
	var zones []string
	for _, s := range subnets {
		if az := aws.ToString(s.AvailabilityZone); az != "" && !slices.Contains(zones, az) {
			zones = append(zones, az)
		}
	}
	if len(zones) == 0 {
		return ""
	}
	slices.Sort(zones)
	offerings, err := awsx.ListAll(ctx,
		func(ctx context.Context, token *string) (*ec2.DescribeInstanceTypeOfferingsOutput, error) {
			return c.EC2.DescribeInstanceTypeOfferings(ctx, &ec2.DescribeInstanceTypeOfferingsInput{
				LocationType: ec2types.LocationTypeAvailabilityZone,
				Filters:      []ec2types.Filter{{Name: aws.String("instance-type"), Values: n.InstanceTypes}},
				NextToken:    token,
			})
		},
		func(out *ec2.DescribeInstanceTypeOfferingsOutput) ([]ec2types.InstanceTypeOffering, *string) {
			return out.InstanceTypeOfferings, out.NextToken
		},
	)
	if err != nil {
		return ""
	}
	offered := map[string]bool{}
	for _, o := range offerings {
		offered[string(o.InstanceType)+"\x00"+aws.ToString(o.Location)] = true
	}
	types := slices.Sorted(slices.Values(n.InstanceTypes))
	var missing []string
	for _, t := range types {
		for _, az := range zones {
			if !offered[t+"\x00"+az] {
				missing = append(missing, t+" in "+az)
			}
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "EC2 does not offer every instance type of nodegroup " + n.Name + " in each of its availability zones (" +
		strings.Join(missing, ", ") + "); new nodes may fail to launch there. This checks the offering, and not the capacity that is free now"
}
