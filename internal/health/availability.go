package health

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

// Availability checks that EC2 offers every instance type of the target
// nodegroups in each availability zone of their subnets. New nodes cannot
// launch in a zone that does not offer their type, so a roll or a scale-up
// can hang there.
//
// It is advice beside the gate, and no part of Preflight: a change runs it
// on its own and shows a warning of it as a notice. Details has one line
// for each nodegroup with a missing offering. A read that fails gives no
// warning and no failure: the result is Skipped. It checks that a type is
// offered, and not that EC2 has capacity for it now: only a launch shows
// that.
//
// It makes one DescribeSubnets call and one paged
// DescribeInstanceTypeOfferings call for all the targets together.
func Availability() Check {
	return Check{Name: "InstanceTypes", Run: runAvailability}
}

func runAvailability(ctx context.Context, in *Input) Result {
	var targets []fleet.Nodegroup
	var subnetIDs, types []string
	for _, n := range in.Snapshot.Nodegroups {
		// A nodegroup with a custom launch template reports no instance
		// types.
		if !slices.Contains(in.Targets, n.Name) || len(n.InstanceTypes) == 0 || len(n.Subnets) == 0 {
			continue
		}
		targets = append(targets, n)
		subnetIDs = append(subnetIDs, n.Subnets...)
		types = append(types, n.InstanceTypes...)
	}
	if len(targets) == 0 {
		return Result{Outcome: Skipped, Message: "no target reports its instance types and its subnets"}
	}
	subnetIDs, types = sortedSet(subnetIDs), sortedSet(types)
	subnets, err := awsx.ListAll(ctx,
		func(ctx context.Context, token *string) (*ec2.DescribeSubnetsOutput, error) {
			return in.Clients.EC2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: subnetIDs, NextToken: token})
		},
		func(out *ec2.DescribeSubnetsOutput) ([]ec2types.Subnet, *string) { return out.Subnets, out.NextToken },
	)
	if err != nil {
		return Result{Outcome: Skipped, Message: "the subnets of the targets could not be read"}
	}
	zoneOf := map[string]string{}
	for _, s := range subnets {
		zoneOf[aws.ToString(s.SubnetId)] = aws.ToString(s.AvailabilityZone)
	}
	offerings, err := awsx.ListAll(ctx,
		func(ctx context.Context, token *string) (*ec2.DescribeInstanceTypeOfferingsOutput, error) {
			return in.Clients.EC2.DescribeInstanceTypeOfferings(ctx, &ec2.DescribeInstanceTypeOfferingsInput{
				LocationType: ec2types.LocationTypeAvailabilityZone,
				Filters:      []ec2types.Filter{{Name: aws.String("instance-type"), Values: types}},
				NextToken:    token,
			})
		},
		func(out *ec2.DescribeInstanceTypeOfferingsOutput) ([]ec2types.InstanceTypeOffering, *string) {
			return out.InstanceTypeOfferings, out.NextToken
		},
	)
	if err != nil {
		return Result{Outcome: Skipped, Message: "the instance-type offerings could not be read"}
	}
	offered := map[string]bool{}
	for _, o := range offerings {
		offered[string(o.InstanceType)+"\x00"+aws.ToString(o.Location)] = true
	}
	r := Result{Outcome: Pass, Message: "every instance type is offered in each availability zone"}
	for _, n := range targets {
		var zones []string
		for _, id := range n.Subnets {
			if az := zoneOf[id]; az != "" {
				zones = append(zones, az)
			}
		}
		var missing []string
		for _, t := range sortedSet(n.InstanceTypes) {
			for _, az := range sortedSet(zones) {
				if !offered[t+"\x00"+az] {
					missing = append(missing, t+" in "+az)
				}
			}
		}
		if len(missing) == 0 {
			continue
		}
		r.Findings = append(r.Findings, n.Name)
		r.Details = append(r.Details, "EC2 does not offer every instance type of nodegroup "+n.Name+" in each of its availability zones ("+
			strings.Join(missing, ", ")+"); new nodes may fail to launch there. This checks the offering, and not the capacity that is free now")
	}
	if len(r.Details) > 0 {
		r.Outcome, r.Message = Warn, strings.Join(r.Details, "; ")
	}
	return r
}

// sortedSet returns the distinct values of in, sorted.
func sortedSet(in []string) []string {
	return slices.Compact(slices.Sorted(slices.Values(in)))
}
