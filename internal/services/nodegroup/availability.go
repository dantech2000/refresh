package nodegroup

import (
	"context"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
)

// instanceOfferingsAPI is the slice of EC2 the availability pre-flight needs.
// The concrete *ec2.Client satisfies it; tests pass a fake.
type instanceOfferingsAPI interface {
	DescribeSubnets(ctx context.Context, in *ec2.DescribeSubnetsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeInstanceTypeOfferings(ctx context.Context, in *ec2.DescribeInstanceTypeOfferingsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstanceTypeOfferingsOutput, error)
}

// UnavailableOffering is an (instance type, AZ) pair the nodegroup spans but
// where EC2 doesn't offer that instance type.
type UnavailableOffering struct {
	InstanceType     string `json:"instanceType" yaml:"instanceType"`
	AvailabilityZone string `json:"availabilityZone" yaml:"availabilityZone"`
}

// CheckInstanceTypeAvailability reports (instance type, AZ) combinations the
// nodegroup spans where EC2 doesn't offer the type — a pre-flight that catches
// "the ASG can't launch nodes in AZ X" before a scale-up or roll silently
// stalls. NOTE: this checks whether a type is *offered* in an AZ, not whether
// there is spare *capacity* right now — only a launch attempt reveals
// InsufficientInstanceCapacity.
func (s *ServiceImpl) CheckInstanceTypeAvailability(ctx context.Context, clusterName, nodegroupName string) ([]UnavailableOffering, error) {
	desc, err := s.eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(nodegroupName),
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, "describing nodegroup")
	}
	if desc == nil || desc.Nodegroup == nil {
		return nil, nil
	}
	return checkInstanceTypeAvailability(ctx, s.ec2Client, desc.Nodegroup.InstanceTypes, desc.Nodegroup.Subnets)
}

func checkInstanceTypeAvailability(ctx context.Context, api instanceOfferingsAPI, instanceTypes, subnetIDs []string) ([]UnavailableOffering, error) {
	// Custom-launch-template nodegroups report no InstanceTypes; nothing to check.
	if len(instanceTypes) == 0 || len(subnetIDs) == 0 {
		return nil, nil
	}

	// Resolve the nodegroup's AZs from its subnets.
	subs, err := api.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: subnetIDs})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, "describing subnets")
	}
	azSet := make(map[string]bool, len(subs.Subnets))
	for _, sn := range subs.Subnets {
		if sn.AvailabilityZone != nil {
			azSet[*sn.AvailabilityZone] = true
		}
	}
	if len(azSet) == 0 {
		return nil, nil
	}

	var unavailable []UnavailableOffering
	for _, it := range instanceTypes {
		offerings, oerr := awsinternal.ListAllPages(ctx, "describing instance type offerings",
			func(rc context.Context, token *string) (*ec2.DescribeInstanceTypeOfferingsOutput, error) {
				return api.DescribeInstanceTypeOfferings(rc, &ec2.DescribeInstanceTypeOfferingsInput{
					LocationType: ec2types.LocationTypeAvailabilityZone,
					Filters:      []ec2types.Filter{{Name: aws.String("instance-type"), Values: []string{it}}},
					NextToken:    token,
				})
			},
			func(o *ec2.DescribeInstanceTypeOfferingsOutput) ([]ec2types.InstanceTypeOffering, *string) {
				return o.InstanceTypeOfferings, o.NextToken
			},
		)
		if oerr != nil {
			return nil, oerr
		}
		offered := make(map[string]bool, len(offerings))
		for _, off := range offerings {
			if off.Location != nil {
				offered[*off.Location] = true
			}
		}
		for az := range azSet {
			if !offered[az] {
				unavailable = append(unavailable, UnavailableOffering{InstanceType: it, AvailabilityZone: az})
			}
		}
	}

	sort.Slice(unavailable, func(i, j int) bool {
		if unavailable[i].InstanceType != unavailable[j].InstanceType {
			return unavailable[i].InstanceType < unavailable[j].InstanceType
		}
		return unavailable[i].AvailabilityZone < unavailable[j].AvailabilityZone
	})
	return unavailable, nil
}
